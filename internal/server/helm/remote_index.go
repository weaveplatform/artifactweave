package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/authz"
	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteIndexLimit = 32 << 20

type remoteIndex struct {
	node     *yaml.Node
	entries  *yaml.Node
	versions map[string][]remoteVersion
}

type remoteVersion struct {
	node    *yaml.Node
	chart   helmfmt.ChartVersion
	targets []remoteTarget
}

type remoteTarget struct {
	path string
	url  *url.URL
}

func remoteError() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Helm response")
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// Retain the YAML tree so rewriting locations does not discard upstream fields,
// public signing keys or annotations. The cached representation remains raw.
func readRemoteIndex(body io.Reader) (remoteIndex, error) {
	var out remoteIndex
	data, err := io.ReadAll(io.LimitReader(body, remoteIndexLimit+1))
	if err != nil || len(data) > remoteIndexLimit {
		return out, remoteError()
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 ||
		doc.Content[0].Kind != yaml.MappingNode {
		return out, remoteError()
	}
	var extra yaml.Node
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return out, remoteError()
	}
	var index helmfmt.Index
	if doc.Decode(&index) != nil || index.APIVersion != "v1" || index.Entries == nil {
		return out, remoteError()
	}
	entries := mappingValue(doc.Content[0], "entries")
	if entries == nil || entries.Kind != yaml.MappingNode {
		return out, remoteError()
	}
	return remoteIndex{node: &doc, entries: entries, versions: map[string][]remoteVersion{}}, nil
}

func decodeRemoteIndex(c repo.Config, responseBase string, body io.Reader) (remoteIndex, error) {
	out, err := readRemoteIndex(body)
	if err != nil {
		return remoteIndex{}, err
	}
	entries := out.entries
	seen := map[string]helmfmt.ChartVersion{}
	locations := map[string]string{}
	count := 0
	for i := 0; i+1 < len(entries.Content); i += 2 {
		name, versions := entries.Content[i].Value, entries.Content[i+1]
		if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") ||
			versions.Kind != yaml.SequenceNode {
			return remoteIndex{}, remoteError()
		}
		for _, node := range versions.Content {
			var entry helmfmt.ChartVersion
			count++
			if count > 100000 || node.Kind != yaml.MappingNode || node.Decode(&entry) != nil ||
				entry.Name != name || !remoteVersionValid(entry.Version) || len(entry.URLs) == 0 {
				return remoteIndex{}, remoteError()
			}
			sums, checksumErr := item.ParseChecksums(types.Checksums{SHA256: entry.Digest})
			if checksumErr != nil {
				return remoteIndex{}, remoteError()
			}
			entry.Digest = sums.SHA256
			v := remoteVersion{node: node, chart: entry}
			for _, raw := range entry.URLs {
				target, targetErr := chartTarget(c, responseBase, raw)
				if targetErr != nil {
					return remoteIndex{}, targetErr
				}
				location := target.path
				if prior, ok := locations[location]; ok && prior != target.url.String() {
					return remoteIndex{}, remoteError()
				}
				locations[location] = target.url.String()
				if prior, ok := seen[target.url.String()]; ok &&
					(prior.Name != entry.Name || prior.Version != entry.Version || prior.Digest != entry.Digest) {
					return remoteIndex{}, remoteError()
				}
				seen[target.url.String()] = entry
				v.targets = append(v.targets, target)
			}
			out.versions[name] = append(out.versions[name], v)
		}
		slices.SortStableFunc(out.versions[name], func(a, b remoteVersion) int {
			return -types.CompareSemver(a.chart.Version, b.chart.Version)
		})
	}
	return out, nil
}

func validateRemoteIndex(
	c repo.Config,
) func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64, response source.MetadataResponse) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("helm index validation: %w", err)
		}
		if size < 0 || size > remoteIndexLimit {
			return remoteError()
		}
		_, err := decodeRemoteIndex(c, response.BaseURL, body)
		return err
	}
}

func parsedChartURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(raw, "\\\r\n\x00") {
		return nil, remoteError()
	}
	// ResolveReference removes dot segments. Refuse them before that operation
	// so an index cannot silently traverse outside its configured chart base.
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return nil, remoteError()
		}
	}
	return u, nil
}

type chartScope struct {
	config                       repo.Config
	base, responseDir, chartBase *url.URL
}

func newChartScope(c repo.Config, responseBase string) (chartScope, error) {
	out := chartScope{config: c}
	base, err := parsedChartURL(strings.TrimRight(c.URL, "/") + "/")
	if err != nil {
		return out, err
	}
	response, err := parsedChartURL(responseBase)
	if err != nil || response.Scheme == "" || response.Host == "" {
		return out, remoteError()
	}
	out.base, out.responseDir, out.chartBase = base, response.ResolveReference(
		&url.URL{Path: "."},
	), response
	if c.ChartsBaseURL != "" {
		out.chartBase, err = parsedChartURL(strings.TrimRight(c.ChartsBaseURL, "/") + "/")
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s chartScope) location(u *url.URL) (string, error) {
	if u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" ||
		s.base.Scheme == "https" && u.Scheme != "https" {
		return "", remoteError()
	}
	inBase := func(root *url.URL) bool {
		return u.Scheme == root.Scheme && strings.EqualFold(u.Host, root.Host) &&
			strings.HasPrefix(u.Path, root.Path)
	}
	if inBase(s.base) {
		p := strings.TrimPrefix(u.Path, s.base.Path)
		if p == "" || strings.HasPrefix(p, "/") {
			return "", remoteError()
		}
		return p, nil
	}
	allowed := inBase(s.responseDir) || s.config.ChartsBaseURL != "" && inBase(s.chartBase)
	if !allowed && s.config.ExternalDependenciesEnabled != nil &&
		*s.config.ExternalDependenciesEnabled {
		for _, pattern := range s.config.ExternalDependenciesPatterns {
			if pattern != "" && authz.CompilePattern(pattern).Match(u.String()) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return "", remoteError()
	}
	return "_external/" + u.Scheme + "/" + u.Host + u.Path, nil
}

func chartTarget(c repo.Config, responseBase, raw string) (remoteTarget, error) {
	scope, err := newChartScope(c, responseBase)
	if err != nil {
		return remoteTarget{}, err
	}
	ref, err := parsedChartURL(raw)
	if err != nil {
		return remoteTarget{}, err
	}
	target := scope.chartBase.ResolveReference(ref)
	if !helmfmt.IsChartPath(target.Path) {
		return remoteTarget{}, remoteError()
	}
	local, err := scope.location(target)
	if err != nil {
		return remoteTarget{}, err
	}
	return remoteTarget{path: local, url: target}, nil
}

func chartURLGuard(
	c repo.Config,
	responseBase string,
	provenance bool,
) func(context.Context, *url.URL) error {
	scope, scopeErr := newChartScope(c, responseBase)
	return func(ctx context.Context, u *url.URL) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("helm destination: %w", err)
		}
		if scopeErr != nil {
			return scopeErr
		}
		checked, err := parsedChartURL(u.String())
		if err != nil {
			return err
		}
		// Provenance accompanies the chart selected by the index. Its suffix does
		// not create a broader destination grant, including on redirected requests.
		if provenance {
			checked.Path = strings.TrimSuffix(checked.Path, ".prov")
			checked.RawPath = ""
		}
		_, err = scope.location(checked)
		return err
	}
}

// Helm's semver parser accepts abbreviated cores and a leading v. Preserve the
// original text while checking a padded core. The local strict layout policy is
// separate from the remote index's native-client version grammar.
func remoteVersionValid(version string) bool {
	v := strings.TrimPrefix(version, "v")
	core, suffix := v, ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		core, suffix = v[:i], v[i:]
	}
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return false
	}
	for i, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
		parts[i] = strings.TrimLeft(part, "0")
		if parts[i] == "" {
			parts[i] = "0"
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return helmfmt.IsSemver(strings.Join(parts, ".") + suffix)
}
