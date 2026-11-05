package types

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidItemPath reports a repository path that cannot name an item.
var ErrInvalidItemPath = errors.New("invalid item path")

// RootPath is the parent path of an item that sits at the repository root.
// AQL reports it as "." and the item table stores it the same way, so a root
// listing and a nested listing are the same query (decision 0004).
const RootPath = "."

// Checksums are hex digests of an item's content.
type Checksums struct {
	SHA256 string `json:"sha256,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
}

// SplitItemPath turns a repository-relative path into the canonical parent,
// name and depth. Leading and trailing slashes are ignored, so folder paths
// from deploys ("a/b/") and storage paths ("/a/b") both work; empty, "." and
// ".." segments are refused because they would let a path escape or alias
// another item.
func SplitItemPath(full string) (parent, name string, depth int, err error) {
	full = strings.Trim(full, "/")
	if full == "" {
		return "", "", 0, fmt.Errorf("%w: empty", ErrInvalidItemPath)
	}
	segments := strings.Split(full, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
			return "", "", 0, fmt.Errorf("%w: %q", ErrInvalidItemPath, full)
		}
	}
	name = segments[len(segments)-1]
	parent = RootPath
	if len(segments) > 1 {
		parent = strings.Join(segments[:len(segments)-1], "/")
	}
	return parent, name, len(segments), nil
}
