package antpath

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Pattern is a compiled Ant-style path pattern: '/' separates segments, "**"
// matches zero or more segments, '*' any run of characters within a
// segment, '?' exactly one character. Matching is case-sensitive and paths
// are relative to the repository root.
type Pattern struct {
	segs []string
}

// CompilePattern compiles p. A leading '/' is ignored and a trailing '/'
// means everything below, as in Ant ("dir/" is "dir/**").
func CompilePattern(p string) Pattern {
	p = strings.TrimPrefix(p, "/")
	if strings.HasSuffix(p, "/") || p == "" {
		p += "**"
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		// Consecutive "**" match the same as one, and collapsing them keeps
		// the match table small.
		if s == "**" && len(segs) > 0 && segs[len(segs)-1] == "**" {
			continue
		}
		segs = append(segs, s)
	}
	return Pattern{segs: segs}
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// Match reports whether the pattern matches path ("" is the repository
// root, which only patterns that can match zero segments match).
func (p Pattern) Match(path string) bool {
	segs := splitPath(path)
	// m[j] reports whether p.segs[i:] matches segs[j:], filled from the end
	// of the pattern backwards: dynamic programming instead of backtracking,
	// so a pattern of many "**" cannot take exponential time.
	next := make([]bool, len(segs)+1)
	next[len(segs)] = true
	cur := make([]bool, len(segs)+1)
	for i := len(p.segs) - 1; i >= 0; i-- {
		ps := p.segs[i]
		cur[len(segs)] = ps == "**" && next[len(segs)]
		for j := len(segs) - 1; j >= 0; j-- {
			if ps == "**" {
				cur[j] = next[j] || cur[j+1]
			} else {
				cur[j] = matchSegment(ps, segs[j]) && next[j+1]
			}
		}
		next, cur = cur, next
	}
	return next[0]
}

// MatchBelow reports whether the pattern could match some path below
// folder, so a listing can show the folders that lead to permitted content
// (Ant's matchPatternStart).
func (p Pattern) MatchBelow(folder string) bool {
	segs := splitPath(folder)
	for i, s := range segs {
		if i >= len(p.segs) {
			return false
		}
		if p.segs[i] == "**" {
			return true
		}
		if !matchSegment(p.segs[i], s) {
			return false
		}
	}
	return len(p.segs) > len(segs)
}

// MatchAllBelow reports whether the pattern matches every path below
// folder, a path of at least one more segment ("" for every path but the
// root), whatever those segments hold. A list relies on it to count what
// lies below a folder without matching each path, so it must never be true
// when some path below the folder fails Match; it may be false when every
// path matches, which only costs that list its shortcut. It is true when
// the pattern has a "**", each segment before it matches the folder's
// segment at its place or, past the folder, is made of '*' alone, each
// segment after it is made of '*' alone, and the shortest path below the
// folder has a segment for each of those: "**", "a/**" below "a", "*/**",
// "**/*" and "a/" (which is "a/**").
func (p Pattern) MatchAllBelow(folder string) bool {
	segs := splitPath(folder)
	first := slices.Index(p.segs, "**")
	// Without "**" a pattern matches paths of one length only; and the
	// shortest path below the folder, len(segs)+1 segments, needs one for
	// each segment the "**" does not stand for.
	if first < 0 || len(p.segs)-1 > len(segs)+1 {
		return false
	}
	for i, s := range p.segs {
		switch {
		case i == first:
		case i < first && i < len(segs):
			if !matchSegment(s, segs[i]) {
				return false
			}
		case s == "" || strings.Trim(s, "*") != "":
			// A segment that may not match whatever a path holds there.
			return false
		}
	}
	return true
}

// matchSegment matches one segment against '*' and '?' by runes, with the
// usual single-backtrack wildcard algorithm (linear for one '*', quadratic
// at worst).
func matchSegment(pat, s string) bool {
	px, sx := 0, 0
	starP, starS := -1, 0
	for sx < len(s) {
		if px < len(pat) {
			pc, pn := utf8.DecodeRuneInString(pat[px:])
			switch pc {
			case '*':
				starP, starS = px, sx
				px += pn
				continue
			case '?':
				_, sn := utf8.DecodeRuneInString(s[sx:])
				px, sx = px+pn, sx+sn
				continue
			default:
				sc, sn := utf8.DecodeRuneInString(s[sx:])
				if pc == sc {
					px, sx = px+pn, sx+sn
					continue
				}
			}
		}
		if starP < 0 {
			return false
		}
		_, sn := utf8.DecodeRuneInString(s[starS:])
		starS += sn
		px, sx = starP+1, starS
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
}
