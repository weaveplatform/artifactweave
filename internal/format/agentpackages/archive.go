// Package agentpackages validates and indexes APM Agent Packages ZIP archives.
package agentpackages

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	MaxArchiveBytes  = 64 << 20
	MaxManifestBytes = 1 << 20
	MaxFiles         = 512
)

var (
	ErrArchive = errors.New("invalid Agent Package archive")
	component  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Metadata carries bounded descriptor fields; the canonical path supplies
// identity and version after validation.
type Metadata struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Types       []string `json:"types,omitempty"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
}

func ValidComponent(v string) bool { return component.MatchString(v) }

func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && !strings.HasPrefix(v, "v") &&
		strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

func Path(owner, name, version string) string {
	return owner + "/" + name + "/" + name + "-" + version + ".zip"
}

func ParsePath(p string) (owner, name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidComponent(parts[0]) || !ValidComponent(parts[1]) {
		return "", "", "", false
	}
	prefix := parts[1] + "-"
	if !strings.HasPrefix(parts[2], prefix) || !strings.HasSuffix(parts[2], ".zip") {
		return "", "", "", false
	}
	v := strings.TrimSuffix(strings.TrimPrefix(parts[2], prefix), ".zip")
	if !ValidVersion(v) || p != Path(parts[0], parts[1], v) {
		return "", "", "", false
	}
	return parts[0], parts[1], v, true
}

// ReadArchive checks all ZIP members, including CRC and inflated size. An
// apm.yml takes precedence over the documented lockfile fallback.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size < 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || int64(len(data)) != size || len(data) > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	z, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil || len(z.File) == 0 || len(z.File) > MaxFiles {
		return Metadata{}, ErrArchive
	}
	manifest, lock, types, err := scanEntries(z.File)
	if err != nil || len(manifest) == 0 && len(lock) == 0 {
		return Metadata{}, ErrArchive
	}
	if len(manifest) == 0 {
		var doc map[string]any
		if yaml.Unmarshal(lock, &doc) != nil || len(doc) == 0 {
			return Metadata{}, ErrArchive
		}
		return Metadata{Types: types}, nil
	}
	var doc struct {
		Name        string   `yaml:"name"`
		Version     string   `yaml:"version"`
		Description string   `yaml:"description"`
		Tags        []string `yaml:"tags"`
	}
	if yaml.Unmarshal(manifest, &doc) != nil ||
		strings.TrimSpace(doc.Name) == "" || !ValidVersion(doc.Version) {
		return Metadata{}, ErrArchive
	}
	return Metadata{
		Name: doc.Name, Version: doc.Version,
		Description: doc.Description, Tags: doc.Tags, Types: types,
	}, nil
}

func scanEntries(files []*zip.File) (manifest, lock []byte, types []string, err error) {
	seen := map[string]bool{}
	var inflated uint64
	primitiveTypes := map[string]bool{}
	for _, f := range files {
		if !safeName(f.Name) || seen[f.Name] || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return nil, nil, nil, ErrArchive
		}
		seen[f.Name] = true
		if f.FileInfo().IsDir() {
			continue
		}
		if parts := strings.Split(f.Name, "/"); len(parts) >= 3 && parts[0] == ".apm" {
			kind := strings.TrimSuffix(parts[1], "s")
			if kind != "" {
				primitiveTypes[kind] = true
			}
		}
		inflated += f.UncompressedSize64
		if inflated > MaxArchiveBytes {
			return nil, nil, nil, ErrArchive
		}
		content, err := readEntry(f, f.Name == "apm.yml" || f.Name == "apm.lock.yaml")
		if err != nil {
			return nil, nil, nil, ErrArchive
		}
		switch f.Name {
		case "apm.yml":
			manifest = content
		case "apm.lock.yaml":
			lock = content
		}
	}
	types = make([]string, 0, len(primitiveTypes))
	for kind := range primitiveTypes {
		types = append(types, kind)
	}
	slices.Sort(types)
	return manifest, lock, types, nil
}

func readEntry(f *zip.File, keep bool) ([]byte, error) {
	if f.UncompressedSize64 > MaxArchiveBytes || keep && f.UncompressedSize64 > MaxManifestBytes {
		return nil, ErrArchive
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrArchive
	}
	out := io.Discard
	var buf bytes.Buffer
	if keep {
		out = &buf
	}
	n, readErr := io.Copy(out, io.LimitReader(rc, MaxArchiveBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		return nil, ErrArchive
	}
	//nolint:gosec // io.Copy returns a nonnegative byte count
	if uint64(n) != f.UncompressedSize64 {
		return nil, ErrArchive
	}
	return buf.Bytes(), nil
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
