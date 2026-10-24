package gems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	MaxGemBytes      = 256 << 20
	maxMetadataBytes = 2 << 20
)

var (
	namePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9_.-]{0,127}$`)
)

func ValidName(name string) bool {
	return namePattern.MatchString(name) && !strings.Contains(name, "..")
}

func ValidVersion(version string) bool {
	return versionPattern.MatchString(version) && !strings.Contains(version, "..")
}

type Dependency struct {
	Name        string `json:"name"`
	Requirement string `json:"requirements"`
}

type Package struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Platform     string       `json:"platform"`
	Summary      string       `json:"summary"`
	Description  string       `json:"description"`
	Homepage     string       `json:"homepage"`
	Authors      []string     `json:"authors"`
	Licenses     []string     `json:"licenses"`
	Ruby         string       `json:"ruby"`
	RubyGems     string       `json:"rubygems"`
	Dependencies []Dependency `json:"dependencies"`
	Path         string       `json:"path"`
	SHA256       string       `json:"sha256"`
}

func Filename(p Package) string {
	name := p.Name + "-" + p.Version
	if p.Platform != "" && p.Platform != "ruby" {
		name += "-" + p.Platform
	}
	return name + ".gem"
}

func ValidPath(path string) bool {
	if !strings.HasPrefix(path, "gems/") || strings.Count(path, "/") != 1 ||
		!strings.HasSuffix(path, ".gem") {
		return false
	}
	base := strings.TrimSuffix(strings.TrimPrefix(path, "gems/"), ".gem")
	return namePattern.MatchString(base) && !strings.Contains(base, "..")
}

func Read(ctx context.Context, r io.Reader, size int64, path string) (Package, error) {
	if size <= 0 || size > MaxGemBytes || (path != "" && !ValidPath(path)) {
		return Package{}, errors.New("invalid gem size or path")
	}
	all, err := io.ReadAll(io.LimitReader(r, MaxGemBytes+1))
	if err != nil || int64(len(all)) != size {
		return Package{}, errors.New("truncated gem archive")
	}
	tr := tar.NewReader(bytes.NewReader(all))
	parts := map[string][]byte{}
	for {
		if err := ctx.Err(); err != nil {
			return Package{}, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil {
			return Package{}, errors.New("invalid gem tar")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return Package{}, errors.New("non-file gem entry")
		}
		if h.Name != "metadata.gz" && h.Name != "data.tar.gz" && h.Name != "checksums.yaml.gz" {
			return Package{}, errors.New("unexpected gem entry")
		}
		if _, exists := parts[h.Name]; exists || h.Size < 0 || h.Size > MaxGemBytes {
			return Package{}, errors.New("duplicate or oversized gem entry")
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxGemBytes+1))
		if err != nil || int64(len(b)) != h.Size {
			return Package{}, errors.New("truncated gem entry")
		}
		parts[h.Name] = b
	}
	if len(parts["metadata.gz"]) == 0 || len(parts["data.tar.gz"]) == 0 {
		return Package{}, errors.New("missing gem metadata or data")
	}
	if err := validateData(parts["data.tar.gz"]); err != nil {
		return Package{}, err
	}
	if len(parts["checksums.yaml.gz"]) > 0 {
		checks, err := unzip(parts["checksums.yaml.gz"], maxMetadataBytes)
		if err != nil {
			return Package{}, err
		}
		var sums struct {
			SHA256 map[string]string `yaml:"SHA256"`
		}
		if err := yaml.Unmarshal(checks, &sums); err != nil || len(sums.SHA256) != 2 {
			return Package{}, errors.New("invalid gem checksums")
		}
		for _, key := range []string{"metadata.gz", "data.tar.gz"} {
			sum := sha256.Sum256(parts[key])
			if sums.SHA256[key] != hex.EncodeToString(sum[:]) {
				return Package{}, fmt.Errorf("gem %s checksum mismatch", key)
			}
		}
	}
	meta, err := unzip(parts["metadata.gz"], maxMetadataBytes)
	if err != nil {
		return Package{}, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(meta, &root); err != nil || len(root.Content) != 1 {
		return Package{}, errors.New("invalid gemspec YAML")
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode || node.Tag != "!ruby/object:Gem::Specification" {
		return Package{}, errors.New("missing Gem::Specification")
	}
	p := Package{Path: path, Platform: "ruby"}
	fields := map[string]*yaml.Node{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		fields[node.Content[i].Value] = node.Content[i+1]
	}
	p.Name = scalar(fields["name"])
	p.Version = scalar(field(fields["version"], "version"))
	p.Platform = scalar(fields["platform"])
	if p.Platform == "" {
		p.Platform = "ruby"
	}
	p.Summary = scalar(fields["summary"])
	p.Description = scalar(fields["description"])
	p.Homepage = scalar(fields["homepage"])
	p.Authors = stringsOf(fields["authors"])
	p.Licenses = stringsOf(fields["licenses"])
	p.Ruby = requirements(fields["required_ruby_version"])
	p.RubyGems = requirements(fields["required_rubygems_version"])
	if deps := fields["dependencies"]; deps != nil && deps.Kind == yaml.SequenceNode {
		for _, d := range deps.Content {
			if d.Kind != yaml.MappingNode || d.Tag != "!ruby/object:Gem::Dependency" {
				return Package{}, errors.New("invalid gem dependency")
			}
			m := map[string]*yaml.Node{}
			for i := 0; i+1 < len(d.Content); i += 2 {
				m[d.Content[i].Value] = d.Content[i+1]
			}
			if typ := scalar(m["type"]); typ != ":development" {
				dep := Dependency{
					Name:        scalar(m["name"]),
					Requirement: requirements(m["requirement"]),
				}
				if !ValidName(dep.Name) {
					return Package{}, errors.New("invalid gem dependency name")
				}
				p.Dependencies = append(p.Dependencies, dep)
			}
		}
	}
	if !ValidName(p.Name) || !ValidVersion(p.Version) || !ValidName(p.Platform) ||
		(path != "" && path != "gems/"+Filename(p)) {
		return Package{}, errors.New("gem identity differs from path")
	}
	sum := sha256.Sum256(all)
	p.SHA256 = hex.EncodeToString(sum[:])
	return p, nil
}

func validateData(compressed []byte) error {
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return errors.New("invalid gem data gzip")
	}
	defer z.Close()
	limited := io.LimitReader(z, MaxGemBytes*4+1)
	tr := tar.NewReader(limited)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil {
			return errors.New("invalid gem data tar")
		}
		if h.Size < 0 || h.Size > MaxGemBytes*4 || len(h.Name) > 4096 ||
			strings.HasPrefix(h.Name, "/") ||
			strings.Contains(h.Name, "../") ||
			h.Name == ".." || strings.HasSuffix(h.Name, "/..") {
			return errors.New("unsafe gem data entry")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir &&
			h.Typeflag != tar.TypeSymlink {
			return errors.New("unsupported gem data entry")
		}
		if h.Typeflag == tar.TypeSymlink &&
			(strings.HasPrefix(h.Linkname, "/") || strings.Contains(h.Linkname, "../") ||
				h.Linkname == ".." || strings.HasSuffix(h.Linkname, "/..")) {
			return errors.New("unsafe gem data symlink")
		}
		n, err := io.Copy(io.Discard, io.LimitReader(tr, MaxGemBytes*4-total+1))
		if err != nil {
			return errors.New("truncated gem data entry")
		}
		total += n
		if total > MaxGemBytes*4 {
			return errors.New("gem data exceeds limit")
		}
	}
	if err := z.Close(); err != nil {
		return errors.New("truncated gem data gzip")
	}
	return nil
}

func unzip(b []byte, limit int64) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid gem gzip")
	}
	defer z.Close()
	out, err := io.ReadAll(io.LimitReader(z, limit+1))
	if err != nil || int64(len(out)) > limit {
		return nil, errors.New("gem metadata exceeds limit")
	}
	if err := z.Close(); err != nil {
		return nil, errors.New("truncated gem gzip")
	}
	return out, nil
}

func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func stringsOf(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, v := range n.Content {
		out = append(out, scalar(v))
	}
	return out
}

func requirements(n *yaml.Node) string {
	reqs := field(n, "requirements")
	if reqs == nil || reqs.Kind != yaml.SequenceNode {
		return ""
	}
	var out []string
	for _, row := range reqs.Content {
		if row.Kind != yaml.SequenceNode || len(row.Content) != 2 {
			continue
		}
		out = append(out, scalar(row.Content[0])+" "+scalar(field(row.Content[1], "version")))
	}
	return strings.Join(out, ", ")
}
