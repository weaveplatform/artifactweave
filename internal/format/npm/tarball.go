package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// ErrNotTarball is content the format cannot index: no tar or tar.gz, no
// readable package.json in its top-level folder, a name npm would not
// accept, a version that is not SemVer, or more than limits allow. Such a
// .tgz is stored but not indexed (decision 0034, strict layout).
var ErrNotTarball = errors.New("npm: not a usable package tarball")

// limits bound reading a tarball. The largest npm packages hold tens of
// thousands of files and unpack to a few gigabytes; a tarball past these
// is stored but not indexed, rather than decompressed without end on a
// gzip bomb. The package.json bound is archive.DefaultLimits'.
var limits = archive.Limits{Entries: 1 << 20, Inflated: 16 << 30, File: 1 << 20}

// Tarball is what the format reads from a package tarball.
type Tarball struct {
	// SHA256 is the content read, so a hook can tell the inspection of its
	// own write from one of other content.
	SHA256 string
	// Name is package.json's; Version is package.json's as CleanVersion
	// leaves it.
	Name, Version string
	// Manifest is package.json, compacted.
	Manifest json.RawMessage
	// Description and Keywords are package.json's, as npm normalises them:
	// a description that is no string is none, and keywords given as one
	// string are split at commas.
	Description string
	Keywords    []string
	// FileCount and UnpackedSize count the regular files in the tarball,
	// as npm's dist.fileCount and dist.unpackedSize do.
	FileCount    int
	UnpackedSize int64
	// HasInstallScript is whether installing runs a script: a preinstall,
	// install or postinstall script, or a binding.gyp, for which npm runs
	// node-gyp rebuild unless gypfile is false.
	HasInstallScript bool
}

// CanonicalPath is where a version's tarball is stored: the path npm names
// in its publish body and the JFrog CLI deploys to, so a scoped package's
// file name keeps its scope (@scope/name/-/@scope/name-1.0.0.tgz).
func CanonicalPath(name, version string) string {
	return name + "/-/" + name + "-" + version + ".tgz"
}

// candidate reports whether p could be a canonical path, which is cheaper
// than reading the content to find out.
func candidate(p string) bool {
	return strings.HasSuffix(p, ".tgz") && strings.Contains(p, "/-/")
}

// ReadTarball reads the package tarball in ra, of size bytes, in one pass:
// package.json from its top-level folder, whatever that folder is called,
// and the counts of its files. ErrNotTarball when the format cannot index
// it; any other error is a failure to read the content.
func ReadTarball(ra io.ReaderAt, size int64) (*Tarball, error) {
	src := &firstErr{ra: ra}
	kind := archive.KindOf(src, "package.tgz")
	if kind != archive.TarGz && kind != archive.Tar {
		return nil, fmt.Errorf("%w: neither tar nor tar.gz", ErrNotTarball)
	}
	var (
		t   Tarball
		pkg []byte
		gyp bool
	)
	err := archive.Scan(src, size, kind, limits, func(e archive.Entry) error {
		n, err := archive.Clean(e.Name)
		// npm's extraction skips an entry that would leave the package's
		// folder, so it is no file of the package.
		if err != nil || !e.Regular {
			return nil //nolint:nilerr // see above
		}
		t.FileCount++
		t.UnpackedSize += e.Size
		// npm strips the top-level folder, so package.json and binding.gyp
		// are one level below it, and a later entry of the same name
		// replaces an earlier one, as extracting it would.
		if strings.Count(n, "/") != 1 {
			return nil
		}
		switch path.Base(n) {
		case "package.json":
			pkg, err = archive.ReadEntry(e, limits)
			return err //nolint:wrapcheck // archive names its own
		case "binding.gyp":
			gyp = true
		}
		return nil
	})
	switch {
	case src.err != nil:
		return nil, fmt.Errorf("npm: read tarball: %w", src.err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotTarball, err)
	case pkg == nil:
		return nil, fmt.Errorf("%w: no package.json in its top-level folder", ErrNotTarball)
	}
	if err := t.parse(pkg, gyp); err != nil {
		return nil, err
	}
	return &t, nil
}

// firstErr remembers the first read error that is not the end of the
// content. Without it, a blob store failing mid-read would look like
// content that is no tarball, and the version would silently go unindexed.
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

// utf8BOM starts some package.json files; npm's JSON parser skips it.
var utf8BOM = []byte("\xef\xbb\xbf")

// parse reads package.json into t, as npm's normalisation reads it.
func (t *Tarball) parse(pkg []byte, gyp bool) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimPrefix(pkg, utf8BOM)); err != nil {
		return fmt.Errorf("%w: package.json: %w", ErrNotTarball, err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(compact.Bytes(), &fields) != nil || fields == nil {
		return fmt.Errorf("%w: package.json is no JSON object", ErrNotTarball)
	}
	var name, version string
	if json.Unmarshal(fields["name"], &name) != nil || !ValidName(name) {
		return fmt.Errorf("%w: package.json names no valid package", ErrNotTarball)
	}
	if json.Unmarshal(fields["version"], &version) != nil {
		return fmt.Errorf("%w: package.json has no version", ErrNotTarball)
	}
	clean, ok := CleanVersion(version)
	if !ok {
		return fmt.Errorf("%w: %q is not a SemVer version", ErrNotTarball, version)
	}
	t.Name, t.Version, t.Manifest = name, clean, compact.Bytes()
	_ = json.Unmarshal(fields["description"], &t.Description)
	t.Keywords = keywords(fields["keywords"])
	t.HasInstallScript = installs(fields["scripts"]) ||
		gyp && string(fields["gypfile"]) != "false"
	return nil
}

// keywords normalises package.json's keywords as npm does: one string is
// split at commas, and entries that are no non-empty string are dropped.
func keywords(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		raw, _ = json.Marshal(commas.Split(one, -1))
	}
	var list []any
	_ = json.Unmarshal(raw, &list)
	var out []string
	for _, k := range list {
		if s, ok := k.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

var commas = regexp.MustCompile(`,\s+`)

// installs reports whether scripts runs one of npm's install hooks.
func installs(raw json.RawMessage) bool {
	var scripts map[string]any
	_ = json.Unmarshal(raw, &scripts)
	for _, k := range []string{"preinstall", "install", "postinstall"} {
		if s, ok := scripts[k].(string); ok && s != "" {
			return true
		}
	}
	return false
}

// readStored reads the stored content sha as a tarball; ok is false when
// it is none the format can index.
func readStored(ctx context.Context, blobs format.Blobs, sha string) (*Tarball, bool, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return nil, false, fmt.Errorf("npm: open %s: %w", sha, err)
	}
	defer func() { _ = rc.Close() }()
	t, err := ReadTarball(archive.ReaderAt(rc), info.Size)
	switch {
	case errors.Is(err, ErrNotTarball):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	t.SHA256 = sha
	return t, true, nil
}

// MaxVersionLength is the longest version string npm's semver parses.
const MaxVersionLength = 256

// semverPattern is SemVer 2.0 as npm's semver reads it without its loose
// option: no leading zeros in numeric identifiers.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][a-zA-Z0-9-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][a-zA-Z0-9-]*))*))?` +
	`(?:\+[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)*)?$`)

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER, above which npm's
// semver refuses a major, minor or patch number.
const maxSafeInteger = "9007199254740991"

// CleanVersion is npm's semver.clean: surrounding space and any leading
// "=" and "v" dropped, then a SemVer version without its build metadata,
// which npm ignores, so 1.0.0+build and 1.0.0 are one version. ok is false
// when v is no SemVer version.
func CleanVersion(v string) (string, bool) {
	s := strings.TrimLeft(strings.TrimSpace(v), "=v")
	if len(s) > MaxVersionLength {
		return "", false
	}
	m := semverPattern.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	for _, n := range m[1:4] {
		if len(n) > len(maxSafeInteger) || len(n) == len(maxSafeInteger) && n > maxSafeInteger {
			return "", false
		}
	}
	out := m[1] + "." + m[2] + "." + m[3]
	if m[4] != "" {
		out += "-" + m[4]
	}
	return out, true
}

// ValidName reports whether npm accepts name for a package that exists,
// as validate-npm-package-name's validForOldPackages does: a name, or
// @scope/name, whose parts need no URL encoding, that starts with neither
// "." nor "_", and is not one npm reserves. Rules npm adds for new names,
// such as lower case, are left to the client, so older packages index.
func ValidName(name string) bool {
	if name == "" || name[0] == '.' || name[0] == '_' ||
		name == "node_modules" || name == "favicon.ico" {
		return false
	}
	scope, pkg, scoped := strings.Cut(name, "/")
	if !scoped {
		return urlSafe(name)
	}
	user, ok := strings.CutPrefix(scope, "@")
	return ok && urlSafe(user) && urlSafe(pkg)
}

// urlSafe reports whether s is non-empty and unchanged by JavaScript's
// encodeURIComponent.
func urlSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_.!~*'()", r):
		default:
			return false
		}
	}
	return true
}
