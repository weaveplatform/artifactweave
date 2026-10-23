// Package agentplugins validates and indexes local Agent Plugin ZIP bundles.
package agentplugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	MaxArchiveBytes  = 64 << 20
	MaxManifestBytes = 1 << 20
	MaxFiles         = 512
)

var (
	ErrArchive   = errors.New("invalid Agent Plugin bundle")
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	harnessPaths = map[string]string{
		"claude": ".claude-plugin/plugin.json",
		"cursor": ".cursor-plugin/plugin.json",
		"codex":  ".codex-plugin/plugin.json",
	}
)

// Metadata records only bounded manifest data. Path identity replaces any
// conflicting manifest name or version before it enters the package index.
type Metadata struct {
	Name      string                     `json:"name"`
	Version   string                     `json:"version"`
	Path      string                     `json:"path"`
	SHA256    string                     `json:"sha256"`
	SHA1      string                     `json:"sha1,omitempty"`
	Harnesses []string                   `json:"harnesses"`
	Manifests map[string]json.RawMessage `json:"manifests"`
	Root      bool                       `json:"root"`
}

func ValidName(v string) bool { return namePattern.MatchString(v) }
func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && !strings.HasPrefix(v, "v") &&
		strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

func Path(name, version string) string {
	return name + "/" + version + "/" + name + "-" + version + ".zip"
}

func ParsePath(p string) (name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidName(parts[0]) || !ValidVersion(parts[1]) ||
		p != Path(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ReadArchive checks every ZIP member, including CRC and total decompressed
// size. The pinned 7.161.32 target indexes harness manifests; root-only
// Agent Plugins Standard bundles arrived in a later JFrog release.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size < 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || len(data) > MaxArchiveBytes || int64(len(data)) != size {
		return Metadata{}, ErrArchive
	}
	z, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil || len(z.File) == 0 || len(z.File) > MaxFiles {
		return Metadata{}, ErrArchive
	}
	meta := Metadata{Manifests: map[string]json.RawMessage{}}
	seen := map[string]bool{}
	var total uint64
	for _, f := range z.File {
		if !safeName(f.Name) || seen[f.Name] || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return Metadata{}, ErrArchive
		}
		seen[f.Name] = true
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if total > MaxArchiveBytes {
			return Metadata{}, ErrArchive
		}
		if err := validateEntry(f); err != nil {
			return Metadata{}, ErrArchive
		}
		for harness, p := range harnessPaths {
			if f.Name != p {
				continue
			}
			content, err := readEntry(f)
			if err != nil || !validManifest(content) {
				return Metadata{}, ErrArchive
			}
			meta.Manifests[harness] = content
			meta.Harnesses = append(meta.Harnesses, harness)
		}
		if f.Name == "plugin.json" {
			content, err := readEntry(f)
			if err != nil || !validManifest(content) {
				return Metadata{}, ErrArchive
			}
			meta.Root = true
		}
	}
	if len(meta.Harnesses) == 0 && !meta.Root {
		return Metadata{}, ErrArchive
	}
	slices.Sort(meta.Harnesses)
	return meta, nil
}

func validateEntry(f *zip.File) error {
	if f.UncompressedSize64 > MaxArchiveBytes {
		return ErrArchive
	}
	rc, err := f.Open()
	if err != nil {
		return ErrArchive
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(rc, MaxArchiveBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || n > MaxArchiveBytes ||
		uint64(n) != f.UncompressedSize64 { //nolint:gosec // n cannot be negative
		return ErrArchive
	}
	return nil
}

func readEntry(f *zip.File) (json.RawMessage, error) {
	if f.UncompressedSize64 > MaxManifestBytes {
		return nil, ErrArchive
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrArchive
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, MaxManifestBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || len(data) > MaxManifestBytes ||
		uint64(len(data)) != f.UncompressedSize64 {
		return nil, ErrArchive
	}
	return data, nil
}

func validManifest(data []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return false
	}
	name, ok := m["name"].(string)
	return ok && strings.TrimSpace(name) != ""
}

func safeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") ||
		strings.ContainsRune(name, 0) || path.Clean(name) != strings.TrimSuffix(name, "/") {
		return false
	}
	for part := range strings.SplitSeq(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
