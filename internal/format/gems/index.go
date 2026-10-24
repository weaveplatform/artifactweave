package gems

import (
	"crypto/md5" //nolint:gosec // Compact Index uses MD5 only as an info-file cache validator.
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const MaxIndexBytes = 32 << 20

var errInvalidIndex = errors.New("invalid RubyGems index")

func invalidIndex(detail string) error { return fmt.Errorf("%w: %s", errInvalidIndex, detail) }

// InfoMD5 is the Compact Index's required cache validator, not an integrity digest.
func InfoMD5(body []byte) string {
	sum := md5.Sum(body) //nolint:gosec // The RubyGems compact index protocol requires MD5 here.
	return hex.EncodeToString(sum[:])
}

// CompareVersions compares the numeric and prerelease parts used by RubyGems.
// A release ranks above a prerelease at the same numeric version.
func CompareVersions(a, b string) int {
	parts := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	}
	aa, bb := parts(a), parts(b)
	for i := 0; i < len(aa) || i < len(bb); i++ {
		if i >= len(aa) {
			if _, err := strconv.ParseUint(bb[i], 10, 64); err != nil {
				return 1
			}
			return -1
		}
		if i >= len(bb) {
			if _, err := strconv.ParseUint(aa[i], 10, 64); err != nil {
				return -1
			}
			return 1
		}
		an, ae := strconv.ParseUint(aa[i], 10, 64)
		bn, be := strconv.ParseUint(bb[i], 10, 64)
		switch {
		case ae == nil && be == nil:
			if an < bn {
				return -1
			}
			if an > bn {
				return 1
			}
		case ae == nil:
			return 1
		case be == nil:
			return -1
		default:
			if n := strings.Compare(aa[i], bb[i]); n != 0 {
				return n
			}
		}
	}
	return 0
}

func CompactNames(packages []Package) []byte {
	names := map[string]bool{}
	for _, p := range packages {
		names[p.Name] = true
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return []byte("---\n" + strings.Join(keys, "\n") + "\n")
}

func ParseCompactNames(b []byte) ([]string, error) {
	if len(b) > MaxIndexBytes || len(b) < 4 || !strings.HasPrefix(string(b), "---\n") ||
		b[len(b)-1] != '\n' {
		return nil, invalidIndex("invalid compact names")
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range strings.Split(string(b[4:]), "\n") {
		if name == "" {
			continue
		}
		if !ValidName(name) || seen[name] {
			return nil, invalidIndex("invalid compact name")
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func CompactInfo(name string, packages []Package) []byte {
	var rows []Package
	for _, p := range packages {
		if p.Name == name {
			rows = append(rows, p)
		}
	}
	slices.SortFunc(rows, func(a, b Package) int {
		if n := CompareVersions(a.Version, b.Version); n != 0 {
			return n
		}
		return strings.Compare(a.Platform, b.Platform)
	})
	var b strings.Builder
	b.WriteString("---\n")
	for _, p := range rows {
		b.WriteString(p.Version)
		if p.Platform != "" && p.Platform != "ruby" {
			b.WriteByte('-')
			b.WriteString(p.Platform)
		}
		b.WriteByte(' ')
		for i, d := range p.Dependencies {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(d.Name)
			b.WriteByte(':')
			b.WriteString(strings.Join(strings.Split(d.Requirement, ", "), "&"))
		}
		b.WriteString("|checksum:")
		b.WriteString(p.SHA256)
		if p.Ruby != "" {
			b.WriteString(",ruby:")
			b.WriteString(strings.Join(strings.Split(p.Ruby, ", "), "&"))
		}
		if p.RubyGems != "" {
			b.WriteString(",rubygems:")
			b.WriteString(strings.Join(strings.Split(p.RubyGems, ", "), "&"))
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func CompactVersions(packages []Package) []byte {
	names := map[string][]string{}
	for _, p := range packages {
		v := p.Version
		if p.Platform != "" && p.Platform != "ruby" {
			v += "-" + p.Platform
		}
		names[p.Name] = append(names[p.Name], v)
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString("created_at: 1970-01-01T00:00:00Z\n---\n")
	for _, name := range keys {
		slices.Sort(names[name])
		names[name] = slices.Compact(names[name])
		fmt.Fprintf(
			&b,
			"%s %s %s\n",
			name,
			strings.Join(names[name], ","),
			InfoMD5(CompactInfo(name, packages)),
		)
	}
	return []byte(b.String())
}

// CompactVersionEntry binds the listed versions to their compact info body.
type CompactVersionEntry struct {
	MD5      string
	Versions map[string]bool
}

func VersionToken(p Package) string {
	if p.Platform != "" && p.Platform != "ruby" {
		return p.Version + "-" + p.Platform
	}
	return p.Version
}

// ParseCompactVersions accepts only bounded, complete index rows. The MD5 is
// checked against each info file before virtual metadata can disclose it.
func ParseCompactVersions(b []byte) (map[string]CompactVersionEntry, error) {
	if len(b) > MaxIndexBytes || len(b) == 0 || b[len(b)-1] != '\n' {
		return nil, invalidIndex("invalid compact versions length")
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, line := range lines {
		if line == "---" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, invalidIndex("compact versions missing delimiter")
	}
	out := map[string]CompactVersionEntry{}
	for _, line := range lines[start:] {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) != 3 || !ValidName(parts[0]) || len(parts[2]) != 32 {
			return nil, invalidIndex("invalid compact versions row")
		}
		if parts[1] == "" {
			return nil, invalidIndex("empty compact versions row")
		}
		entry := CompactVersionEntry{MD5: parts[2], Versions: map[string]bool{}}
		for _, token := range strings.Split(parts[1], ",") {
			version := strings.TrimPrefix(token, "-") // Yanked versions are tombstones.
			if version == "" || !versionPattern.MatchString(version) ||
				strings.Contains(version, "..") {
				return nil, invalidIndex("invalid compact version")
			}
			if strings.HasPrefix(token, "-") {
				delete(entry.Versions, version)
			} else {
				entry.Versions[version] = true
			}
		}
		if _, err := hex.DecodeString(parts[2]); err != nil {
			return nil, fmt.Errorf("compact versions info MD5: %w", err)
		}
		if _, ok := out[parts[0]]; ok {
			return nil, invalidIndex("duplicate compact versions name")
		}
		out[parts[0]] = entry
	}
	return out, nil
}

//nolint:gocyclo // A single bounded row parser validates each wire field before returning packages.
func ParseCompactInfo(name string, b []byte) ([]Package, error) {
	if !ValidName(name) || len(b) > MaxIndexBytes || len(b) < 4 ||
		!strings.HasPrefix(string(b), "---\n") ||
		b[len(b)-1] != '\n' {
		return nil, invalidIndex("invalid compact info")
	}
	var out []Package
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b[4:]), "\n") {
		if line == "" {
			continue
		}
		v, rest, ok := strings.Cut(line, " ")
		if !ok || !versionPattern.MatchString(v) {
			return nil, invalidIndex("invalid compact info version")
		}
		deps, extra, ok := strings.Cut(rest, "|")
		if !ok {
			return nil, invalidIndex("invalid compact info requirements")
		}
		p := Package{Name: name, Version: v, Platform: "ruby"}
		// The version token may include a platform. A version can itself have
		// dashes; the ordinary RubyGems convention uses the final platform.
		if version, platform, hasPlatform := strings.Cut(v, "-"); hasPlatform {
			p.Version = version
			p.Platform = platform
		}
		if deps != "" {
			for _, d := range strings.Split(deps, ",") {
				n, req, ok := strings.Cut(d, ":")
				if !ok || !namePattern.MatchString(n) {
					return nil, invalidIndex("invalid compact dependency")
				}
				p.Dependencies = append(
					p.Dependencies,
					Dependency{Name: n, Requirement: strings.ReplaceAll(req, "&", ", ")},
				)
			}
		}
		for _, part := range strings.Split(extra, ",") {
			key, value, ok := strings.Cut(part, ":")
			if !ok {
				return nil, invalidIndex("invalid compact requirement")
			}
			switch key {
			case "checksum":
				if len(value) != 64 {
					return nil, invalidIndex("invalid gem checksum")
				}
				if _, err := hex.DecodeString(value); err != nil {
					return nil, fmt.Errorf("compact info SHA256: %w", err)
				}
				p.SHA256 = value
			case "ruby":
				p.Ruby = strings.ReplaceAll(value, "&", ", ")
			case "rubygems":
				p.RubyGems = strings.ReplaceAll(value, "&", ", ")
			case "created_at":
			default:
				return nil, invalidIndex("unknown compact requirement")
			}
		}
		if p.SHA256 == "" || !versionPattern.MatchString(p.Version) ||
			!namePattern.MatchString(p.Platform) ||
			strings.Contains(p.Version, "..") ||
			strings.Contains(p.Platform, "..") {
			return nil, invalidIndex("invalid compact gem identity")
		}
		p.Path = "gems/" + Filename(p)
		if !ValidPath(p.Path) || seen[p.Path] {
			return nil, invalidIndex("duplicate or invalid compact gem path")
		}
		seen[p.Path] = true
		out = append(out, p)
	}
	return out, nil
}
