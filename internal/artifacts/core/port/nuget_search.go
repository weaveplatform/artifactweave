package port

import (
	"unicode"
	"unicode/utf8"
)

// NuGetSearch is what NuGet's search, autocomplete and v2 feeds ask the
// package index for (PackageStore.SearchNuGet): the packages of RepoKey
// whose names start with Prefix that Terms may match, as npm_search.go
// defines a text search's candidates.
type NuGetSearch struct {
	RepoKey string
	// Terms are the search's terms, lower case as strings.ToLower makes
	// them. With none, every package with a version is a candidate.
	Terms []string
	// Prefix, when not empty, keeps only the names that start with it,
	// in bytes: an autocomplete's, and a feed of one id's versions, which
	// starts at that id. No name holds a NUL byte, so a Prefix with one
	// keeps none.
	Prefix string
}

// SearchPrefixEnd is the least string that sorts, in byte order, after
// every string that starts with prefix, a valid UTF-8 string: prefix with
// its last rune that can grow by one grown, and what follows it dropped.
// UTF-8's byte order is its code points' order, so the names that start
// with prefix are those from prefix up to it. ok is false when no rune can
// grow, and nothing sorts after them.
func SearchPrefixEnd(prefix string) (end string, ok bool) {
	for i := len(prefix); i > 0; {
		r, size := utf8.DecodeLastRuneInString(prefix[:i])
		i -= size
		next := r + 1
		if next == 0xd800 {
			next = 0xe000
		}
		if next <= unicode.MaxRune {
			return prefix[:i] + string(next), true
		}
	}
	return "", false
}
