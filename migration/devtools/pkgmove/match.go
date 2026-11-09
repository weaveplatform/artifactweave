package pkgmove

import (
	"slices"
	"strings"
)

// mapping is an old path and the path that replaces it.
type mapping struct {
	// from is the old directory, slash-separated and relative to the
	// repository root.
	from string
	// to is its replacement, or empty when the old path has no single
	// replacement (a split or deleted package, or a check of a re-homed
	// directory whose destination is not given).
	to string
	// rename maps a file name directly in from to its name in to.
	rename map[string]string
	// whole reports that everything below from lands at the same position
	// below to, so a path that continues past from without naming a single
	// file or asset (a directory prefix such as "P/", a "P/..." pattern, a
	// glob or a variable) has a replacement too. Otherwise such a path also
	// covers packages that stay, and it is left alone.
	whole bool
	// moduleOnly makes from match only after the module path, as promotion
	// needs for "<module>/migration/": a bare "migration" is a word.
	moduleOnly bool
	// file marks the old path of a file entry that names a file: nothing
	// lies below it, so a "/" after it ends no reference.
	file bool
	// asset marks the old path of a file entry that names an asset
	// directory, which moves whole by its own entry.
	asset bool
	// ticket is the owning ticket, for messages; empty outside the ledger.
	ticket string
}

// matcher finds the references to the old paths of its mappings in text.
// move, check, promote and status share it, so a move rewrites exactly what
// the stale check would report.
type matcher struct {
	// forms are the spellings of the module that may precede an old path
	// anywhere: the module path for import paths, and the $(MODULE) and
	// ${MODULE} variables of the Makefile and Dockerfile -X flags and
	// CONAN_COVERPKG.
	forms    []string
	mappings []*mapping
	// region reports whether a directory below an old path keeps its own
	// ledger entry or holds a package, so that it stays when its parent
	// moves.
	region func(dir string) bool
	// isFile reports whether a path names a file of the tree, so that
	// "migration/access/core/authn/token.go" is not read as the package migration/access/core/token
	// followed by ".go".
	isFile func(path string) bool
	// fileEntry reports whether a path is, or lies in, the old path of a
	// file entry, which its own entry judges: such a file stays when its
	// package moves, or goes somewhere else. Nil means no file entry.
	fileEntry func(path string) bool
}

func newMatcher(module string, mappings []*mapping, region, isFile func(string) bool) *matcher {
	return &matcher{
		forms:    []string{module + "/", "$(MODULE)/", "${MODULE}/"},
		mappings: mappings,
		region:   region,
		isFile:   isFile,
	}
}

// ref is one reference to an old path: the bytes text[start:end] and their
// replacement, which is empty when the mapping has no single replacement.
type ref struct {
	start, end int
	m          *mapping
	repl       string
}

// followers are the bytes that may follow an old path, besides "/" and the
// end of the text: punctuation that ends a path in prose, code, YAML, JSON,
// Makefiles and shell, and white space. A letter, digit, "_" or "-" means a
// longer name, so internal/alpha never matches internal/alphabet.
const followers = `.:"'\` + "`" + " \t\n\r\v\f" + `)]},;$*?#=>`

// find returns the references in text, ordered and without overlaps. Where
// two old paths match at one place, the longer one wins.
func (mt *matcher) find(text string) []ref {
	var refs []ref
	for _, m := range mt.mappings {
		for i := 0; ; {
			j := strings.Index(text[i:], m.from)
			if j < 0 {
				break
			}
			start := i + j
			if r, ok := mt.at(text, start, m); ok {
				refs = append(refs, r)
			}
			i = start + len(m.from)
		}
	}
	slices.SortFunc(refs, func(a, b ref) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return len(b.m.from) - len(a.m.from)
	})
	var out []ref
	end := 0
	for _, r := range refs {
		if r.start >= end {
			out = append(out, r)
			end = r.end
		}
	}
	return out
}

// at decides whether the old path of m found at start in text is a
// reference, and to what.
func (mt *matcher) at(text string, start int, m *mapping) (ref, bool) {
	if !mt.afterModule(text, start) && (m.moduleOnly || !bareStart(text, start)) {
		return ref{}, false
	}
	end := start + len(m.from)
	whole := ref{start: start, end: end, m: m, repl: m.to}
	if end == len(text) {
		return whole, true
	}
	switch c := text[end]; {
	case c == '/' && m.file:
		return ref{}, false
	case c == '/':
		return mt.below(text, end+1, whole)
	case c == '.' && mt.namesFile(text, end, m.from):
		return ref{}, false
	case strings.IndexByte(followers, c) >= 0:
		return whole, true
	}
	return ref{}, false
}

// below decides a reference that continues past the old path with "/", at
// text[k:].
func (mt *matcher) below(text string, k int, whole ref) (ref, bool) {
	m := whole.m
	seg := segment(text[k:])
	if trimmed := strings.TrimRight(seg, "."); trimmed != "" {
		// Dots that end the element end a sentence, as in "see P/sub."
		seg = trimmed
	}
	switch {
	case seg == "" || seg == "...":
		return whole, m.whole
	case strings.Trim(seg, ".") == "":
		// "P/." and "P/.." name P or its parent, not something P holds.
		return ref{}, false
	case mt.region(m.from + "/" + seg):
		// A package below P keeps its own entry and is matched by it.
		return ref{}, false
	case !m.asset && mt.fileEntry != nil && mt.fileEntry(m.from+"/"+pathAt(text[k:])):
		return ref{}, false
	}
	name := seg
	if renamed, ok := m.rename[seg]; ok {
		name = renamed
	}
	r := ref{start: whole.start, end: k + len(seg), m: m}
	if m.to != "" {
		r.repl = m.to + "/" + name
	}
	return r, true
}

// afterModule reports whether text[:start] ends with one of the module forms.
func (mt *matcher) afterModule(text string, start int) bool {
	return slices.ContainsFunc(mt.forms, func(f string) bool {
		return strings.HasSuffix(text[:start], f)
	})
}

// namesFile reports whether the "." at text[dot] starts the extension of a
// file named from.ext, rather than ending a reference to from.
func (mt *matcher) namesFile(text string, dot int, from string) bool {
	ext := text[dot+1:]
	n := 0
	for n < len(ext) && isAlnum(ext[n]) {
		n++
	}
	return n > 0 && mt.isFile(from+"."+ext[:n])
}

// bareStart reports whether an old path written without the module may
// start at text[i]: at the start of the text, after "^" (a regular
// expression anchor), after a chain of "./" and "../" that itself starts
// there, or after a byte that cannot be part of a path. So "pkg/x/internal/y"
// never matches internal/y.
func bareStart(text string, i int) bool {
	for {
		switch {
		case strings.HasSuffix(text[:i], "../"):
			i -= len("../")
		case strings.HasSuffix(text[:i], "./"):
			i -= len("./")
		default:
			return i == 0 || text[i-1] == '^' || !isPathByte(text[i-1])
		}
	}
}

func isPathByte(c byte) bool {
	return isSegmentByte(c) || c == '/'
}

func isSegmentByte(c byte) bool {
	return isAlnum(c) || c == '_' || c == '.' || c == '-'
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// pathAt returns the slash-separated path at the start of s, without the
// dots and slashes that end a sentence or a directory prefix.
func pathAt(s string) string {
	n := 0
	for n < len(s) && isPathByte(s[n]) {
		n++
	}
	return strings.TrimRight(s[:n], "./")
}

// segment returns the path element at the start of s.
func segment(s string) string {
	n := 0
	for n < len(s) && isSegmentByte(s[n]) {
		n++
	}
	return s[:n]
}
