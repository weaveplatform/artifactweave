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
	"regexp"
	"strings"
)

const (
	MaxCrate       = 64 << 20
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

// Inspect reads only the package manifest from a bounded .crate gzip tarball.
// The archive root and manifest identity must match the canonical item path.
func Inspect(ctx context.Context, body io.Reader, size int64, p string) (string, string, error) {
	name, version, ok := ParseCratePath(p)
	if !ok || size <= 0 || size > MaxCrate {
		return "", "", ErrInvalid
	}
	z, err := gzip.NewReader(io.LimitReader(body, MaxCrate+1))
	if err != nil {
		return "", "", fmt.Errorf("%w: gzip: %v", ErrInvalid, err)
	}
	defer z.Close()
	t := tar.NewReader(z)
	manifest := name + "-" + version + "/Cargo.toml"
	for i := 0; i < 10000; i++ {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("%w: tar: %v", ErrInvalid, err)
		}
		if h.Name != manifest {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA || h.Size < 1 || h.Size > 1<<20 {
			return "", "", ErrInvalid
		}
		raw, err := io.ReadAll(io.LimitReader(t, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return "", "", ErrInvalid
		}
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
		if gotName != name || gotVersion != version {
			return "", "", ErrInvalid
		}
		return name, version, nil
	}
	return "", "", ErrInvalid
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

func (r Record) Valid() bool {
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
	if !r.Valid() {
		return "", ErrInvalid
	}
	b, err := json.Marshal(r)
	return string(b), err
}
