// Package chef validates Chef cookbook archives and maintains their native index.
package chef

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	MaxArchiveBytes  = 64 << 20
	MaxUnpackedBytes = 128 << 20
	MaxManifestBytes = 1 << 20
	MaxFiles         = 4096
)

var (
	ErrArchive  = errors.New("chef: invalid cookbook archive")
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
)

// Manifest is the client-visible, validated cookbook identity and dependencies.
type Manifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Maintainer   string            `json:"maintainer,omitempty"`
	Description  string            `json:"description,omitempty"`
	License      string            `json:"license,omitempty"`
	SourceURL    string            `json:"source_url,omitempty"`
	Dependencies map[string]string `json:"dependencies"`
	Platforms    map[string]string `json:"platforms"`
}
type Metadata struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Manifest Manifest `json:"manifest"`
	Path     string   `json:"path"`
	SHA256   string   `json:"sha256"`
	Size     int64    `json:"size"`
}

var (
	versionPattern = regexp.MustCompile(
		`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(\.(0|[1-9][0-9]{0,8}))?$`,
	)
	constraintPattern = regexp.MustCompile(
		`^(?:(?:>=|<=|>|<|=|~>)\s*)?[0-9]+\.[0-9]+(?:\.[0-9]+)?$`,
	)
)

func ValidateManifest(m Manifest) bool {
	if !ValidName(m.Name) || !ValidVersion(m.Version) || len(m.Dependencies) > 256 ||
		len(m.Platforms) > 256 {
		return false
	}
	for name, constraint := range m.Dependencies {
		if !ValidName(name) || name == m.Name || len(constraint) > 128 ||
			!constraintPattern.MatchString(constraint) {
			return false
		}
	}
	for name, constraint := range m.Platforms {
		if !ValidName(name) || len(constraint) > 128 || !constraintPattern.MatchString(constraint) {
			return false
		}
	}
	return true
}

func ValidName(name string) bool { return namePattern.MatchString(name) }

func ValidVersion(version string) bool { return versionPattern.MatchString(version) }

func Path(name, version string) string {
	return name + "/" + name + "-" + version + ".tar.gz"
}

func ParsePath(p string) (name, version string, ok bool) {
	name, file, found := strings.Cut(p, "/")
	if !found || !ValidName(name) || !strings.HasPrefix(file, name+"-") ||
		!strings.HasSuffix(file, ".tar.gz") {
		return "", "", false
	}
	version = strings.TrimSuffix(strings.TrimPrefix(file, name+"-"), ".tar.gz")
	return name, version, ValidVersion(version) && p == Path(name, version)
}

// ReadArchive validates the entire gzip and TAR stream, including metadata.json.
// It rejects duplicate and unsafe entries, links, trailing data, oversized
// decompression, and a manifest whose declared identity is invalid.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size <= 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	compressed, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || int64(len(compressed)) != size || len(compressed) > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	input := bytes.NewReader(compressed)
	gz, err := gzip.NewReader(input)
	if err != nil {
		return Metadata{}, ErrArchive
	}
	gz.Multistream(false)
	plain, err := io.ReadAll(io.LimitReader(gz, MaxUnpackedBytes+1))
	closeErr := gz.Close()
	if err != nil || closeErr != nil || len(plain) > MaxUnpackedBytes || input.Len() != 0 {
		return Metadata{}, ErrArchive
	}
	manifest, root, err := readTarManifest(plain)
	if err != nil {
		return Metadata{}, err
	}
	var m Manifest
	if json.Unmarshal(manifest, &m) != nil || !ValidateManifest(m) {
		return Metadata{}, ErrArchive
	}
	if root != m.Name {
		return Metadata{}, ErrArchive
	}
	return Metadata{Name: m.Name, Version: m.Version, Manifest: m, Size: size}, nil
}

func readTarManifest(plain []byte) ([]byte, string, error) {
	content := bytes.NewReader(plain)
	archive := tar.NewReader(content)
	seen := map[string]bool{}
	var manifest []byte
	var root string
	var err error
	for count := 0; ; count++ {
		entry, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil || count >= MaxFiles || !validEntry(entry, seen) {
			return nil, "", ErrArchive
		}
		part, _, _ := strings.Cut(strings.TrimSuffix(entry.Name, "/"), "/")
		if root == "" {
			root = part
		}
		if part != root {
			return nil, "", ErrArchive
		}
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Name == root+"/metadata.json" {
			if entry.Size <= 0 || entry.Size > MaxManifestBytes {
				return nil, "", ErrArchive
			}
			manifest, err = io.ReadAll(io.LimitReader(archive, MaxManifestBytes+1))
			if err != nil || int64(len(manifest)) != entry.Size {
				return nil, "", ErrArchive
			}
		}
	}
	if len(manifest) == 0 {
		return nil, "", ErrArchive
	}
	// TAR terminators and padding may be zero-filled; any other data after
	// the first terminator is an unvalidated second archive.
	for content.Len() > 0 {
		b, _ := content.ReadByte()
		if b != 0 {
			return nil, "", ErrArchive
		}
	}
	return manifest, root, nil
}

func validEntry(entry *tar.Header, seen map[string]bool) bool {
	if entry == nil || entry.Name == "" || strings.HasPrefix(entry.Name, "/") ||
		strings.ContainsAny(entry.Name, "\\\x00") ||
		path.Clean(entry.Name) != strings.TrimSuffix(entry.Name, "/") ||
		strings.HasPrefix(entry.Name, "../") || entry.Name == ".." ||
		seen[entry.Name] || entry.Size < 0 {
		return false
	}
	seen[entry.Name] = true
	// The zero type flag is the historical regular-file encoding in USTAR.
	return entry.Typeflag == tar.TypeReg || entry.Typeflag == 0 ||
		entry.Typeflag == tar.TypeDir
}

// UnmarshalJSON refuses duplicate or case-aliased keys before the cookbook
// identity and dependency maps reach a native client with different JSON rules.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	if err := UniqueFields(
		data,
		"name",
		"version",
		"maintainer",
		"description",
		"license",
		"source_url",
		"dependencies",
		"platforms",
	); err != nil {
		return err
	}
	type plain Manifest
	var value plain
	if json.Unmarshal(data, &value) != nil {
		return ErrArchive
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields) // UniqueFields already validated the object syntax.
	for _, key := range []string{"dependencies", "platforms"} {
		if raw, ok := fields[key]; ok && string(raw) != "null" {
			if err := UniqueFields(raw); err != nil {
				return err
			}
		}
	}
	*m = Manifest(value)
	return nil
}

func UniqueFields(data []byte, known ...string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return ErrArchive
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ErrArchive
		}
		name, ok := key.(string)
		if !ok || seen[strings.ToLower(name)] {
			return ErrArchive
		}
		// encoding/json folds struct field names, including Unicode aliases.
		// Chef reads exact JSON keys, so known fields must use their tag spelling.
		for _, field := range known {
			if name != field && strings.EqualFold(name, field) {
				return ErrArchive
			}
		}
		seen[strings.ToLower(name)] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ErrArchive
		}
	}
	if _, err = dec.Token(); err != nil {
		return ErrArchive
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return ErrArchive
	}
	return nil
}
