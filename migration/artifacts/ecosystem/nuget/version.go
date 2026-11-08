package nuget

import (
	"strconv"
	"strings"
)

// Version is a NuGet version as NuGet.Versioning's NuGetVersion.TryParse
// reads it: one to four numeric parts, an optional pre-release label of
// dot-separated parts, and optional build metadata.
type Version struct {
	// Numbers are the numeric parts, four of them, the missing ones 0: NuGet
	// reads 1, 1.0, 1.0.0 and 1.0.0.0 as one version.
	Numbers [4]int
	// Labels are the pre-release label's parts, with their case.
	Labels []string
	// Metadata is the build metadata, without its "+".
	Metadata string
	// Original is the version as it was written, less surrounding space.
	Original string
}

// maxPart is the largest numeric part NuGet reads: System.Version's
// components are 32-bit integers.
const maxPart = 1<<31 - 1

// ParseVersion reads v as NuGet does. Each numeric part is digits alone,
// leading zeros allowed; each pre-release part is letters, digits and
// hyphens, with no leading zero in an all-digit part; each metadata part
// is the same, leading zeros allowed (NuGet's IsValidPart). ok is false
// for anything else, such as an empty label or a trailing "-" or "+".
// [I] NuGet also lets System.Version's parser take space around a numeric
// part ("1. 0"); no client writes one, and it is refused here.
func ParseVersion(v string) (Version, bool) {
	s := strings.TrimSpace(v)
	out := Version{Original: s}
	core, rest := s, ""
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		core, rest = s[:i], s[i:]
	}
	if label, ok := strings.CutPrefix(rest, "-"); ok {
		label, meta, hasMeta := strings.Cut(label, "+")
		out.Labels = strings.Split(label, ".")
		if !validParts(out.Labels, false) {
			return Version{}, false
		}
		rest = ""
		if hasMeta {
			rest = "+" + meta
		}
	}
	if meta, ok := strings.CutPrefix(rest, "+"); ok {
		if !validParts(strings.Split(meta, "."), true) {
			return Version{}, false
		}
		out.Metadata = meta
	}
	parts := strings.Split(core, ".")
	if len(parts) > len(out.Numbers) {
		return Version{}, false
	}
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return Version{}, false
		}
		out.Numbers[i] = n
	}
	return out, true
}

// number reads one numeric part.
func number(p string) (int, bool) {
	if p == "" || strings.TrimLeft(p, "0123456789") != "" {
		return 0, false
	}
	// Leading zeros cannot overflow, so they go before the size check.
	p = strings.TrimLeft(p, "0")
	if p == "" {
		return 0, true
	}
	n, err := strconv.Atoi(p)
	if err != nil || n > maxPart {
		return 0, false
	}
	return n, true
}

// validParts reports whether each of parts is one NuGet accepts in a
// pre-release label, or in build metadata when leadingZeros is set.
func validParts(parts []string, leadingZeros bool) bool {
	for _, p := range parts {
		if p == "" || strings.Trim(p, alphanumeric+"-") != "" {
			return false
		}
		if !leadingZeros && len(p) > 1 && p[0] == '0' && strings.Trim(p, "0123456789") == "" {
			return false
		}
	}
	return true
}

const alphanumeric = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Release is the version's numeric parts as NuGet normalises them: leading
// zeros dropped, at least three parts, and a fourth part of 0 dropped.
func (v Version) Release() string {
	n := v.Numbers
	s := strconv.Itoa(n[0]) + "." + strconv.Itoa(n[1]) + "." + strconv.Itoa(n[2])
	if n[3] > 0 {
		s += "." + strconv.Itoa(n[3])
	}
	return s
}

// Normalized is the version as NuGet normalises it (Microsoft's "Normalized
// version numbers", JFrog's migration tool): Release, then the pre-release
// label with its case, and no build metadata. 1.0 is 1.0.0, 01.02.03 is
// 1.2.3, 1.2.3.0 is 1.2.3, 1.2.3.1 stays, and 1.0.7+r3456 is 1.0.7.
func (v Version) Normalized() string {
	if len(v.Labels) == 0 {
		return v.Release()
	}
	return v.Release() + "-" + strings.Join(v.Labels, ".")
}

// IsPrerelease reports a version with a pre-release label.
func (v Version) IsPrerelease() bool { return len(v.Labels) > 0 }

// IsSemVer2 reports a version only SemVer 2.0.0 clients read: one whose
// pre-release label has more than one part, or that has build metadata
// (Microsoft's "Semantic Versioning 2.0.0").
func (v Version) IsSemVer2() bool { return len(v.Labels) > 1 || v.Metadata != "" }

// NormalizeVersion is v normalised, as the flat container, the
// registration and the v2 feed are asked for it; ok is false when v is no
// NuGet version.
func NormalizeVersion(v string) (string, bool) {
	parsed, ok := ParseVersion(v)
	if !ok {
		return "", false
	}
	return parsed.Normalized(), true
}

// rangeIsSemVer2 reports a dependency range with a bound only SemVer 2.0.0
// clients read, which makes the package that declares it one too. A range
// is a version, the minimum, or an interval such as [1.0,2.0); a bound that
// is no version counts for nothing, as no client could read it either.
func rangeIsSemVer2(r string) bool {
	r = strings.TrimSpace(r)
	if strings.HasPrefix(r, "[") || strings.HasPrefix(r, "(") {
		r = strings.TrimRight(strings.TrimLeft(r, "[("), "])")
	}
	for b := range strings.SplitSeq(r, ",") {
		if v, ok := ParseVersion(b); ok && v.IsSemVer2() {
			return true
		}
	}
	return false
}
