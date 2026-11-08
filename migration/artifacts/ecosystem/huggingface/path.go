// Package huggingface owns the current Machine Learning layout used by
// Hugging Face model and dataset repositories.
package huggingface

import (
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Identity is one model or dataset in the current ML layout.
type Identity struct {
	Kind, Owner, Name string
}

func (id Identity) RepoID() string { return id.Owner + "/" + id.Name }
func (id Identity) Prefix() string {
	return id.Kind + "--" + id.Owner + "--" + id.Name
}

// ParseIdentity accepts the names documented for Artifactory's Hugging Face
// repositories. A model and a dataset with the same Hub ID stay disjoint.
func ParseIdentity(kind, owner, name string) (Identity, bool) {
	if kind != "models" && kind != "datasets" || len(owner) == 0 || len(owner) > 96 ||
		len(name) == 0 || len(name) > 96 || !validOwner(owner) || !validName(name) {
		return Identity{}, false
	}
	return Identity{Kind: kind, Owner: owner, Name: name}, true
}

func validOwner(s string) bool {
	if s[0] == '-' || s[len(s)-1] == '-' || strings.Contains(s, "--") {
		return false
	}
	letters := false
	for _, ch := range s {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' {
			letters = true
		} else if ch < '0' || ch > '9' {
			if ch != '-' {
				return false
			}
		}
	}
	return letters
}

func validName(s string) bool {
	if s == "." || s == ".." || strings.Contains(s, "..") || strings.Contains(s, "--") ||
		s[0] == '.' || s[0] == '-' || s[len(s)-1] == '.' || s[len(s)-1] == '-' {
		return false
	}
	for _, ch := range s {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

// Revision accepts immutable full commit IDs. main is a mutable ref resolved
// before a snapshot path is built. The wire permits a branch or tag name, but
// only a published ref is resolved; it is never treated as a file-system path.
func Revision(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// RefName is the documented branch/tag grammar. A ref points to an immutable
// full revision; its name is never interpreted as a filesystem traversal.
func RefName(s string) bool {
	if s == "" || len(s) > 128 || strings.HasPrefix(s, ".") ||
		strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "--") {
		return false
	}
	for _, ch := range s {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func RefPath(id Identity, name string) (string, bool) {
	if !RefName(name) || Revision(name) {
		return "", false
	}
	return id.Prefix() + "/refs/" + name, true
}

func ParseRefPath(path string) (Identity, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[1] != "refs" {
		return Identity{}, "", false
	}
	base := strings.SplitN(parts[0], "--", 3)
	if len(base) != 3 {
		return Identity{}, "", false
	}
	id, ok := ParseIdentity(base[0], base[1], base[2])
	_, valid := RefPath(id, parts[2])
	return id, parts[2], ok && valid
}

// FilePath refuses traversal and control characters while preserving valid
// Unicode filenames used by model repositories.
func FilePath(s string) bool {
	if s == "" || len(s) > 1024 || !utf8.ValidString(s) || strings.HasPrefix(s, "/") ||
		strings.HasSuffix(s, "/") || strings.Contains(s, "\\") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > 255 {
			return false
		}
		for _, ch := range seg {
			if unicode.IsControl(ch) {
				return false
			}
		}
	}
	return true
}

// SnapshotPath returns the canonical item path for one file.
func SnapshotPath(id Identity, revision, file string) (string, bool) {
	if !Revision(revision) || !FilePath(file) {
		return "", false
	}
	return id.Prefix() + "/snapshots/" + revision + "/" + file, true
}

// ParseSnapshotPath is shared by the format hook, native client routes and
// lifecycle reindex. It never accepts the superseded legacy layout.
func ParseSnapshotPath(p string) (Identity, string, string, bool) {
	parts := strings.SplitN(p, "/", 4)
	if len(parts) != 4 || parts[1] != "snapshots" || !Revision(parts[2]) ||
		!FilePath(parts[3]) {
		return Identity{}, "", "", false
	}
	base := strings.SplitN(parts[0], "--", 3)
	if len(base) != 3 {
		return Identity{}, "", "", false
	}
	id, ok := ParseIdentity(base[0], base[1], base[2])
	return id, parts[2], parts[3], ok
}
