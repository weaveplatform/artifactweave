// Package cargo validates crates and supplies Cargo sparse-index records.
package cargo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	MaxCrate       = 64 << 20
	maxExpanded    = 512 << 20
	MaxIndex       = 16 << 20
	RecordProperty = "cargo.index"
	YankedProperty = "cargo.yanked"
)

var (
	namePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	versionPattern = regexp.MustCompile(
		`^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?(\+[A-Za-z0-9.-]+)?$`,
	)
	manifestValue = regexp.MustCompile(`^\s*(name|version)\s*=\s*"([^"\r\n]+)"\s*(#.*)?$`)
	ErrInvalid    = errors.New("invalid Cargo crate")
)

func ValidName(s string) bool    { return namePattern.MatchString(s) }
func ValidVersion(s string) bool { return versionPattern.MatchString(s) }

func IndexPath(name string) string {
	n := strings.ToLower(name)
	switch len(n) {
	case 1:
		return "1/" + n
	case 2:
		return "2/" + n
	case 3:
		return "3/" + n[:1] + "/" + n
	default:
		return n[:2] + "/" + n[2:4] + "/" + n
	}
}

func CratePath(name, version string) string {
	return "crates/" + name + "/" + name + "-" + version + ".crate"
}

func ParseCratePath(p string) (name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != "crates" || !ValidName(parts[1]) ||
		!strings.HasSuffix(parts[2], ".crate") {
		return "", "", false
	}
	name = parts[1]
	version = strings.TrimSuffix(strings.TrimPrefix(parts[2], name+"-"), ".crate")
	return name, version, ValidVersion(version) && parts[2] == name+"-"+version+".crate"
}

type countingReader struct {
	io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += int64(n)
	return n, err //nolint:wrapcheck // Preserve io.EOF for archive/tar.Reader.
}

func validCrateEntry(h *tar.Header, root string, seen map[string]bool, declared *int64) bool {
	clean := strings.TrimSuffix(h.Name, "/")
	if clean != path.Clean(clean) || strings.Contains(clean, "\\") ||
		(clean != strings.TrimSuffix(root, "/") && !strings.HasPrefix(clean, root)) || seen[clean] {
		return false
	}
	seen[clean] = true
	if h.Size < 0 || h.Size > maxExpanded-*declared {
		return false
	}
	*declared += h.Size
	switch h.Typeflag {
	case tar.TypeDir:
		return h.Size == 0
	case tar.TypeReg, 0: // The historic zero type flag still denotes a regular file.
		return clean != strings.TrimSuffix(root, "/")
	default:
		return false
	}
}

func validCrateManifest(raw []byte, name, version string) bool {
	gotName, gotVersion := "", ""
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if section != "[package]" {
			continue
		}
		m := manifestValue.FindStringSubmatch(line)
		if len(m) >= 3 {
			if m[1] == "name" {
				gotName = m[2]
			} else {
				gotVersion = m[2]
			}
		}
	}
	return gotName == name && gotVersion == version
}

func validCrateTail(expanded *io.LimitedReader) bool {
	// tar.Reader stops at the terminator. Drain the gzip member to validate
	// its checksum and reject nonzero bytes after the tar terminator.
	var tail [32 << 10]byte
	for {
		n, err := expanded.Read(tail[:])
		for _, b := range tail[:n] {
			if b != 0 {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			return expanded.N > 0
		}
		if err != nil || expanded.N == 0 {
			return false
		}
	}
}

// Inspect validates the complete bounded .crate gzip tarball. Every entry
// must be a unique path under the archive root, and the manifest identity
// must match the canonical item path.
func Inspect(ctx context.Context, body io.Reader, size int64, p string) (string, string, error) {
	name, version, ok := ParseCratePath(p)
	if !ok || size <= 0 || size > MaxCrate {
		return "", "", ErrInvalid
	}
	z, err := gzip.NewReader(io.LimitReader(body, size))
	if err != nil {
		return "", "", fmt.Errorf("%w: gzip: %w", ErrInvalid, err)
	}
	defer z.Close()
	// Bound decompression as well as the compressed bytes on the wire.
	expanded := &io.LimitedReader{R: z, N: maxExpanded + 1}
	counted := &countingReader{Reader: expanded}
	t := tar.NewReader(counted)
	root := name + "-" + version + "/"
	manifest := root + "Cargo.toml"
	seen := map[string]bool{}
	found := false
	var declared int64
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return "", "", fmt.Errorf("cargo: inspect canceled: %w", err)
		}
		before := counted.n
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			// A valid tar terminates with two 512-byte zero blocks. Go's
			// tar.Reader also accepts EOF without them, so enforce them here.
			if counted.n-before < 1024 {
				return "", "", ErrInvalid
			}
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("%w: tar: %w", ErrInvalid, err)
		}
		if i >= 10000 {
			return "", "", ErrInvalid
		}
		if !validCrateEntry(h, root, seen, &declared) {
			return "", "", ErrInvalid
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Name != manifest {
			if n, err := io.CopyN(io.Discard, t, h.Size); err != nil || n != h.Size {
				return "", "", ErrInvalid
			}
			continue
		}
		if h.Size < 1 || h.Size > 1<<20 {
			return "", "", ErrInvalid
		}
		raw, err := io.ReadAll(io.LimitReader(t, h.Size+1))
		if err != nil || int64(len(raw)) != h.Size {
			return "", "", ErrInvalid
		}
		if !validCrateManifest(raw, name, version) {
			return "", "", ErrInvalid
		}
		found = true
	}
	if !found {
		return "", "", ErrInvalid
	}
	if !validCrateTail(expanded) {
		return "", "", ErrInvalid
	}
	return name, version, nil
}

type Dependency struct {
	Name            string   `json:"name"`
	Req             string   `json:"req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          *string  `json:"target"`
	Kind            string   `json:"kind"`
	Registry        *string  `json:"registry"`
	Package         *string  `json:"package"`
}

type Record struct {
	Name        string              `json:"name"`
	Version     string              `json:"vers"`
	Deps        []Dependency        `json:"deps"`
	Checksum    string              `json:"cksum"`
	Features    map[string][]string `json:"features"`
	Yanked      bool                `json:"yanked"`
	Links       *string             `json:"links,omitempty"`
	RustVersion *string             `json:"rust_version,omitempty"`
}

func (r Record) IsValid() bool {
	if !ValidName(r.Name) || !ValidVersion(r.Version) || len(r.Checksum) != 64 || r.Deps == nil ||
		r.Features == nil {
		return false
	}
	for _, c := range r.Checksum {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	for _, d := range r.Deps {
		if !ValidName(d.Name) || d.Req == "" || len(d.Req) > 256 {
			return false
		}
	}
	return true
}

func Encode(r Record) (string, error) {
	if !r.IsValid() {
		return "", ErrInvalid
	}
	b, err := json.Marshal(r)
	return string(b), err
}
