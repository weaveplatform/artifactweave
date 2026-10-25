package gems

import (
	"crypto/md5" //nolint:gosec // Compact Index uses MD5 only as an info-file cache validator.
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const MaxIndexBytes = 32 << 20

var errInvalidIndex = errors.New("invalid RubyGems index")

var versionSegmentPattern = regexp.MustCompile(`[0-9]+|[a-z]+`)

func invalidIndex(detail string) error { return fmt.Errorf("%w: %s", errInvalidIndex, detail) }

// InfoMD5 is the Compact Index's required cache validator, not an integrity digest.
//
//nolint:gosec // RubyGems compact index requires MD5 as a cache validator.
func InfoMD5(body []byte) string {
	sum := md5.Sum(body) //nolint:gosec // The RubyGems compact index protocol requires MD5 here.
	return hex.EncodeToString(sum[:])
}

// CompareVersions compares the numeric and prerelease parts used by RubyGems.
// A release ranks above a prerelease at the same numeric version.
func CompareVersions(a, b string) int {
	parts := func(s string) []string {
		return versionSegmentPattern.FindAllString(
			strings.ToLower(strings.ReplaceAll(s, "-", ".pre.")),
			-1,
		)
	}
	aa, bb := parts(a), parts(b)
	if slices.Equal(canonicalVersionSegments(aa), canonicalVersionSegments(bb)) {
		return 0
	}
	for i := 0; i < len(aa) || i < len(bb); i++ {
		left, right := "0", "0"
		if i < len(aa) {
			left = aa[i]
		}
		if i < len(bb) {
			right = bb[i]
		}
		an, bn := numericVersionSegment(left), numericVersionSegment(right)
		switch {
		case an && bn:
			al, bl := strings.TrimLeft(left, "0"), strings.TrimLeft(right, "0")
			if len(al) != len(bl) {
				return sign(len(al) - len(bl))
			}
			if n := strings.Compare(al, bl); n != 0 {
				return n
			}
		case an:
			return 1
		case bn:
			return -1
		default:
			if n := strings.Compare(left, right); n != 0 {
				return n
			}
		}
	}
	return 0
}

func numericVersionSegment(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

func sign(n int) int {
	if n < 0 {
		return -1
	}
	return 1
}

func canonicalVersionSegments(parts []string) []string {
	firstText := len(parts)
	for i, part := range parts {
		if !numericVersionSegment(part) {
			firstText = i
			break
		}
	}
	trimZeroes := func(in []string) []string {
		for len(in) > 0 && numericVersionSegment(in[len(in)-1]) &&
			strings.TrimLeft(in[len(in)-1], "0") == "" {
			in = in[:len(in)-1]
		}
		return in
	}
	out := append([]string{}, trimZeroes(parts[:firstText])...)
	return append(out, trimZeroes(parts[firstText:])...)
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
		entry, ok := out[parts[0]]
		if !ok {
			entry.Versions = map[string]bool{}
		}
		entry.MD5 = parts[2]
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
		if p.SHA256 == "" || !ValidVersion(p.Version) ||
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
