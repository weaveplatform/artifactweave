package machinelearning

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The layout's two file names and the manifest limits. The limits are the
// ones docs/operations/machinelearning.md documents, so a change to one is
// a change to the other.
const (
	// ManifestName is a published version's manifest, directly under its
	// prefix (Identity.Manifest).
	ManifestName = "model-manifest.json"
	// TransactionName is the record that starting a transaction stores in
	// its staging folder; the principal who created it owns the
	// transaction (CheckTransaction).
	TransactionName = ".transaction.json"
	// MaxManifestSize is the largest manifest read or committed, 4 MiB.
	MaxManifestSize = 4 << 20
	// MaxFiles is the most files one manifest may declare, its model,
	// dependency and code artifacts together.
	MaxFiles = 10000
	// MaxFileSize is the largest file a manifest may declare or an upload
	// may stage, 10 GiB.
	MaxFileSize = 10 << 30
)

// Identity names a model, optionally within one SDK namespace, and its version.
type Identity struct{ Name, Version string }

// Name reports whether s is a model name: one segment, or a namespace and a
// name, each a Segment other than tmp, which the staging layout reserves.
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

// Segment reports whether s is one name, namespace or version segment: 1 to
// 60 ASCII letters, digits, underscores, hyphens and dots, and neither "."
// nor "..".
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

// File reports whether s is a declared file's path within its category: at
// most 1,024 bytes of UTF-8, in segments of 1 to 255 bytes that are neither
// "." nor ".." and hold no control character, with no backslash, "?", "#",
// "%" or ";" and no "!/" archive-entry delimiter anywhere, so the path can
// neither leave the version's folder nor be read as an escape, a query, a
// fragment, a matrix parameter or an archive entry.
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

// Transaction reports whether s is a transaction ID: a positive 13-digit
// decimal with no leading zero, the Unix millisecond time that starting a
// transaction assigns.
func Transaction(s string) bool {
	if len(s) != 13 {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

// Prefix is the version's published folder, models/<name>/<version>.
func (id Identity) Prefix() string { return "models/" + id.Name + "/" + id.Version }

// Manifest is the path of the version's published manifest.
func (id Identity) Manifest() string { return id.Prefix() + "/" + ManifestName }

// Staging is transaction tx's private folder,
// models/<name>/tmp/<version>/<tx>, which no published version's folder
// can be, because names and versions both reserve tmp.
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
