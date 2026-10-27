package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// ErrNotChart is content the format cannot index: no tar or tar.gz, no
// readable Chart.yaml where a chart keeps it, one that is no YAML mapping of
// Helm's fields, or one without a name or a version. Such a file is stored
// but not indexed (decision 0039).
var ErrNotChart = errors.New("helm: not a usable chart archive")

// limits bound reading a chart archive. They are Helm's own: its loader
// refuses a chart that decompresses past 100 MiB or holds a file past
// 5 MiB (pkg/chart/loader, MaxDecompressedChartSize and
// MaxDecompressedFileSize), so a chart past them is one helm could not
// install either. Helm sets no bound on the number of entries.
var limits = archive.Limits{Entries: 1 << 20, Inflated: 100 << 20, File: 5 << 20}

// APIVersionV1 is the apiVersion Helm gives a Chart.yaml that names none
// (pkg/chart/loader: "if c.Metadata.APIVersion == "" { ... APIVersionV1 }").
const APIVersionV1 = "v1"

// Chart is a chart's Chart.yaml as Helm's chart.Metadata reads it: the
// fields of its model, under the names Helm writes in index.yaml, and no
// other. Helm reads an index strictly, so a key outside the model would make
// a whole index.yaml unreadable (decision 0039); unknown keys are dropped
// here. A scalar read into a string keeps its text as written, so a version
// of 1.10 stays "1.10" and an appVersion of 504e199 stays a string
// (RTFACT-20334).
//
//nolint:tagliatelle // Helm's Chart.yaml and index.yaml names
type Chart struct {
	Name         string            `json:"name,omitempty"         yaml:"name"`
	Home         string            `json:"home,omitempty"         yaml:"home"`
	Sources      []string          `json:"sources,omitempty"      yaml:"sources"`
	Version      string            `json:"version,omitempty"      yaml:"version"`
	Description  string            `json:"description,omitempty"  yaml:"description"`
	Keywords     []string          `json:"keywords,omitempty"     yaml:"keywords"`
	Maintainers  []Maintainer      `json:"maintainers,omitempty"  yaml:"maintainers"`
	Icon         string            `json:"icon,omitempty"         yaml:"icon"`
	APIVersion   string            `json:"apiVersion,omitempty"   yaml:"apiVersion"`
	Condition    string            `json:"condition,omitempty"    yaml:"condition"`
	Tags         string            `json:"tags,omitempty"         yaml:"tags"`
	AppVersion   string            `json:"appVersion,omitempty"   yaml:"appVersion"`
	Deprecated   bool              `json:"deprecated,omitempty"   yaml:"deprecated"`
	Annotations  map[string]string `json:"annotations,omitempty"  yaml:"annotations"`
	KubeVersion  string            `json:"kubeVersion,omitempty"  yaml:"kubeVersion"`
	Dependencies []Dependency      `json:"dependencies,omitempty" yaml:"dependencies"`
	Type         string            `json:"type,omitempty"         yaml:"type"`
}

// Maintainer is Helm's chart.Maintainer.
type Maintainer struct {
	Name  string `json:"name,omitempty"  yaml:"name"`
	Email string `json:"email,omitempty" yaml:"email"`
	URL   string `json:"url,omitempty"   yaml:"url"`
}

// Dependency is Helm's chart.Dependency, alias included: 7.x strips it from
// its index (RTFACT-30329), which Helm's own index keeps (decision 0039).
//
//nolint:tagliatelle // Helm's Chart.yaml and index.yaml names
type Dependency struct {
	Name       string   `json:"name"                yaml:"name"`
	Version    string   `json:"version,omitempty"   yaml:"version"`
	Repository string   `json:"repository"          yaml:"repository"`
	Condition  string   `json:"condition,omitempty" yaml:"condition"`
	Tags       []string `json:"tags,omitempty"      yaml:"tags"`
	Enabled    bool     `json:"enabled,omitempty"   yaml:"enabled"`
	// ImportValues holds strings and child/parent mappings, which Helm
	// keeps as it reads them.
	ImportValues []any  `json:"import-values,omitempty" yaml:"import-values"`
	Alias        string `json:"alias,omitempty"         yaml:"alias"`
}

// IsChartPath reports whether p names a file the format reads as a chart
// archive: one whose name ends .tgz or .tar.gz, as 6.20's
// HelmAddon.isHelmFile reads it (research 30, section 3). Anything else,
// a .prov included, is an ordinary file.
func IsChartPath(p string) bool {
	return strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".tar.gz")
}

// stem is a chart file's name without its directory and extension, as the
// Enforce Layout messages print it.
func stem(p string) string {
	base := path.Base(p)
	if s, ok := strings.CutSuffix(base, ".tar.gz"); ok {
		return s
	}
	return strings.TrimSuffix(base, ".tgz")
}

// extension is a chart file's extension, .tgz or .tar.gz.
func extension(p string) string {
	if strings.HasSuffix(p, ".tar.gz") {
		return ".tar.gz"
	}
	return ".tgz"
}

// semverPattern is SemVer 2.0.0 as semver.org gives it: three numbers
// without leading zeros and no leading "v", then an optional pre-release and
// build. Enforce Chart Name and Version holds a chart to it ("adhere to
// Semantic Versioning (SemVer2) standards", kubernetes-helm-chart-repositories).
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][a-zA-Z0-9-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][a-zA-Z0-9-]*))*))?` +
	`(?:\+[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*)?$`)

// IsSemver reports whether v is a SemVer 2.0.0 version.
func IsSemver(v string) bool { return semverPattern.MatchString(v) }

// ReadChart reads the chart archive in ra, of size bytes: the Chart.yaml at
// its root, which a chart packed by hand may have (RTFACT-16103), or else
// the one directly inside a top-level folder, where helm package puts it,
// never a subchart's under charts/ (RTFACT-17119). A requirements.yaml
// beside it is read into it afterwards, as Helm's loader reads a v1 chart's
// dependencies (pkg/chart/loader, LoadFiles). Within one place a later entry
// replaces an earlier one, as extracting the archive would. ErrNotChart when
// the format cannot index the content; any other error is a failure to read
// it.
func ReadChart(ra io.ReaderAt, size int64) (*Chart, error) {
	src := &firstErr{ra: ra}
	kind := archive.KindOf(src, "chart.tgz")
	if kind != archive.KindTarGz && kind != archive.KindTar {
		return nil, fmt.Errorf("%w: neither tar nor tar.gz", ErrNotChart)
	}
	// found holds, by the folder they are in ("" for the root), the
	// Chart.yaml and requirements.yaml read; first is the first top-level
	// folder that holds a Chart.yaml.
	found := map[string]*[2][]byte{}
	first := ""
	err := archive.Scan(src, size, kind, limits, func(e archive.Entry) error {
		n, err := archive.Clean(e.Name)
		if err != nil || !e.Regular || strings.Count(n, "/") > 1 {
			// An entry that would leave the chart's folder, or is no file,
			// or lies deeper than a chart keeps its metadata, is none of
			// the chart's own.
			return nil //nolint:nilerr // see above
		}
		dir, name := path.Split(n)
		slot := 0
		switch name {
		case "Chart.yaml":
		case "requirements.yaml":
			slot = 1
		default:
			return nil
		}
		dir = strings.TrimSuffix(dir, "/")
		body, err := archive.ReadEntry(e, limits)
		if err != nil {
			return err //nolint:wrapcheck // archive names its own
		}
		if found[dir] == nil {
			found[dir] = &[2][]byte{}
		}
		found[dir][slot] = body
		if slot == 0 && dir != "" && first == "" {
			first = dir
		}
		return nil
	})
	switch {
	case src.err != nil:
		return nil, fmt.Errorf("helm: read chart: %w", src.err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotChart, err)
	}
	// A Chart.yaml at the root makes the archive a chart packed by hand, so
	// a top-level folder's is no metadata of its.
	place := found[""]
	if place == nil || place[0] == nil {
		place = found[first]
	}
	if place == nil || place[0] == nil {
		return nil, fmt.Errorf("%w: no Chart.yaml at its root or in its top-level folder",
			ErrNotChart)
	}
	return parseChart(place[0], place[1])
}

// utf8BOM starts some Chart.yaml files written on Windows.
var utf8BOM = []byte("\xef\xbb\xbf")

// parseChart reads Chart.yaml, and requirements.yaml over it when the
// chart has one, as Helm's chart.Metadata. A document that does not read
// into the model, such as keywords given as one string or a key given
// twice, is malformed, as it is to helm, which refuses to load the chart.
func parseChart(chartYAML, requirements []byte) (*Chart, error) {
	var c Chart
	if err := yaml.Unmarshal(bytes.TrimPrefix(chartYAML, utf8BOM), &c); err != nil {
		return nil, fmt.Errorf("%w: Chart.yaml: %w", ErrNotChart, err)
	}
	if requirements != nil {
		if err := yaml.Unmarshal(bytes.TrimPrefix(requirements, utf8BOM), &c); err != nil {
			return nil, fmt.Errorf("%w: requirements.yaml: %w", ErrNotChart, err)
		}
	}
	if c.Name == "" || c.Version == "" {
		return nil, fmt.Errorf("%w: Chart.yaml names no chart or no version", ErrNotChart)
	}
	if c.APIVersion == "" {
		c.APIVersion = APIVersionV1
	}
	for i := range c.Dependencies {
		for j, v := range c.Dependencies[i].ImportValues {
			c.Dependencies[i].ImportValues[j] = jsonable(v)
		}
	}
	return &c, nil
}

// jsonable is v with every mapping keyed by strings, as Helm reads YAML
// through JSON (sigs.k8s.io/yaml): a key such as 1 becomes "1". The YAML
// decoder gives a mapping with a key that is no string a key type JSON
// cannot encode.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = jsonable(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = jsonable(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = jsonable(e)
		}
		return out
	}
	return v
}

// firstErr remembers the first read error that is not the end of the
// content. Without it, a blob store failing mid-read would look like
// content that is no chart, and the chart would silently go unindexed.
type firstErr struct {
	ra  io.ReaderAt
	err error
}

func (r *firstErr) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ra.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && r.err == nil {
		r.err = err
	}
	return n, err //nolint:wrapcheck // io.ReaderAt contract
}

// ReadStored reads the stored content sha as a chart archive: nil when it
// is none the format can index.
func ReadStored(ctx context.Context, blobs format.Blobs, sha string) (*Chart, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("helm: open %s: %w", sha, err)
	}
	defer func() { _ = rc.Close() }()
	c, err := ReadChart(archive.ReaderAt(rc), info.Size)
	if errors.Is(err, ErrNotChart) {
		return nil, nil //nolint:nilnil // content that is no chart is no failure
	}
	return c, err
}
