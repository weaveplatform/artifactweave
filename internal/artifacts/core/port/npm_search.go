package port

import (
	"bytes"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// NpmSearch is what npm search (GET /-/v1/search) asks the package index
// for (PackageStore.SearchNpm): the packages of RepoKey that Terms may
// match.
type NpmSearch struct {
	RepoKey string
	// Terms are the search's terms, lower case as strings.ToLower makes
	// them. With none, every package with a version is a candidate.
	Terms []string
}

// SearchCandidate is a package a text search may list (SearchNpm and
// SearchNuGet), with every one of its versions, ordered as
// PackageStore.Versions orders them.
type SearchCandidate struct {
	Package  model.Package
	Versions []model.PackageVersion
}

// The candidates of a text search are the packages with a version for
// which, for every term with SearchRuns, either each run appears in
// SearchFold of the package's name, or each appears in SearchFold of the
// version's stored metadata, or the metadata is empty or holds
// SearchEscape. Every package whose name or decoded metadata text holds
// each term after strings.ToLower is one, so a store narrows a search
// without changing what it lists; it may return packages that match
// nothing, and the handler, which matches each package by the newest
// version its caller may read, decides.
//
// The store cannot match exactly. Only the versions the caller may read
// count, and the access module decides that per path, in Go, from
// permission patterns that SQL cannot evaluate; and the text a search
// matches is decoded from JSON and lower-cased by Go, Unicode and all,
// where SQLite's lower() folds ASCII alone. What it can test the same way
// on SQLite, PostgreSQL and in memory is the printable ASCII the term
// holds, against the stored JSON as written.
const (
	// SearchEscape is the JSON escape that can spell any character,
	// including the ASCII a run looks for, so a version whose metadata
	// holds one is a candidate whatever its text.
	SearchEscape = `\u`
	// searchRunMax bounds a run in bytes and searchRunsMax the runs of one
	// search, so that a long or many-termed query asks the store a
	// bounded question. Leaving a run out, or its tail, only lets more
	// packages through.
	searchRunMax  = 64
	searchRunsMax = 16
)

// SearchRuns returns, for each term, the runs of it a store looks for: its
// longest stretches of printable ASCII other than `"`, `\` and `/`, which
// JSON may write as escapes (`\"`, `\\`, `\/`). A term without one, all
// non-ASCII say, narrows nothing and is left out.
func SearchRuns(terms []string) [][]string {
	var out [][]string
	total := 0
	for _, t := range terms {
		var runs []string
		for run := range strings.FieldsFuncSeq(t, notRunByte) {
			if total == searchRunsMax {
				break
			}
			runs = append(runs, run[:min(len(run), searchRunMax)])
			total++
		}
		if len(runs) > 0 {
			out = append(out, runs)
		}
	}
	return out
}

// notRunByte reports a rune that ends a run. Non-ASCII runes are tested
// whole, so a multi-byte rune never splits into bytes that look like
// ASCII.
func notRunByte(r rune) bool {
	return r <= ' ' || r > '~' || r == '"' || r == '\\' || r == '/'
}

// SearchFold is s as a store compares it with a run: ASCII letters lower
// case, and U+0130 and U+212A, the two runes strings.ToLower turns into
// ASCII ("i" and "k"), replaced by what it makes of them. Every other rune
// is left as it is, as SQLite's lower() leaves it.
func SearchFold(s string) string {
	b := []byte(searchFolder.Replace(s))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// SearchFoldPairs are the runes SearchFold replaces before it lower-cases
// ASCII, each with its replacement, which the SQL stores replace the same
// way.
var SearchFoldPairs = [][2]string{{"İ", "i"}, {"K", "k"}}

var searchFolder = strings.NewReplacer(
	SearchFoldPairs[0][0], SearchFoldPairs[0][1],
	SearchFoldPairs[1][0], SearchFoldPairs[1][1],
)

// SearchMayMatch reports whether a version of the package called name,
// whose stored metadata is metadata, makes the package a candidate for a
// search whose runs (SearchRuns) are runs.
func SearchMayMatch(name string, metadata []byte, runs [][]string) bool {
	if len(metadata) == 0 || bytes.Contains(metadata, []byte(SearchEscape)) {
		return true
	}
	foldedName, foldedMeta := SearchFold(name), SearchFold(string(metadata))
	for _, term := range runs {
		if !holdsAll(foldedName, term) && !holdsAll(foldedMeta, term) {
			return false
		}
	}
	return true
}

func holdsAll(s string, runs []string) bool {
	for _, r := range runs {
		if !strings.Contains(s, r) {
			return false
		}
	}
	return true
}
