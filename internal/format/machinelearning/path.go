// Package machinelearning owns FrogML's versioned model layout and publication
// invariants. Temporary uploads are never published model versions.
package machinelearning

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ManifestName    = "model-manifest.json"
	TransactionName = ".transaction.json"
	MaxManifestSize = 4 << 20
	MaxFiles        = 10000
	MaxFileSize     = 10 << 30
)

// Identity names a model, optionally within one SDK namespace, and its version.
type Identity struct{ Name, Version string }

func Name(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !Segment(p) || p == "tmp" {
			return false
		}
	}
	return true
}

func Segment(s string) bool {
	if s == "" || len(s) > 60 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' ||
			c == '-' ||
			c == '.' {
			continue
		}
		return false
	}
	return true
}

// Version excludes layout markers: with an optional namespace, category names
// would make one model's manifest indistinguishable from another model's file.
func Version(s string) bool {
	return Segment(s) && s != "tmp" && s != "files" && s != "dependencies" && s != "code"
}

func File(s string) bool {
	if s == "" || len(s) > 1024 || !utf8.ValidString(s) || strings.Contains(s, "!/") ||
		strings.ContainsAny(s, "\\?#%;") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return false
		}
		for _, c := range part {
			if unicode.IsControl(c) {
				return false
			}
		}
	}
	return true
}

func Transaction(s string) bool {
	if len(s) != 13 {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

func (id Identity) Prefix() string   { return "models/" + id.Name + "/" + id.Version }
func (id Identity) Manifest() string { return id.Prefix() + "/" + ManifestName }
func (id Identity) Staging(tx string) string {
	return "models/" + id.Name + "/tmp/" + id.Version + "/" + tx
}

// ParsePath distinguishes committed files from one transaction's private files.
func ParsePath(p string) (id Identity, tx, rest string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 4 || parts[0] != "models" {
		return id, "", "", false
	}
	for names := 1; names <= 2; names++ {
		if len(parts) < names+3 {
			continue
		}
		candidate := Identity{Name: strings.Join(parts[1:1+names], "/"), Version: parts[1+names]}
		start := 2 + names
		transaction := ""
		if candidate.Version == "tmp" {
			if len(parts) < start+3 {
				continue
			}
			candidate.Version, transaction = parts[start], parts[start+1]
			start += 2
			if !Transaction(transaction) {
				continue
			}
		}
		if !Name(candidate.Name) || !Version(candidate.Version) {
			continue
		}
		file := strings.Join(parts[start:], "/")
		if file == ManifestName && transaction == "" ||
			file == TransactionName && transaction != "" {
			return candidate, transaction, file, true
		}
		category, suffix, found := strings.Cut(file, "/")
		if found && (category == "files" || category == "dependencies" || category == "code") &&
			File(suffix) {
			return candidate, transaction, file, true
		}
	}
	return id, "", "", false
}

// Private identifies staging records and manifests, which must not bypass
// transaction ownership or the native manifest's per-artifact authorization.
func Private(p string) bool {
	_, tx, rest, ok := ParsePath(p)
	return ok && (tx != "" || rest == ManifestName)
}
