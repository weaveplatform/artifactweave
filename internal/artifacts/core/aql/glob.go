package aql

import "strings"

// AQL wildcards are * (any run of characters, slashes included) and ? (one
// character), matched case-sensitively; no other character is special and
// there is no escape. The three translations below must agree, which the
// store contract suite checks on every backend.

// globMatch reports whether s matches pattern, character by character.
func globMatch(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti := 0, 0
	starP, starT := -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			starP, starT = pi, ti
			pi++
		case starP >= 0:
			starT++
			pi, ti = starP+1, starT
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// sqliteGlob translates a pattern for SQLite's GLOB, where * and ? mean the
// same but [ opens a character class; [[] is a literal [.
func sqliteGlob(pattern string) string {
	return strings.ReplaceAll(pattern, "[", "[[]")
}

// likeEscape is the escape character of likePattern, declared in the
// ESCAPE clause.
const likeEscape = `\`

// likePattern translates a pattern for PostgreSQL's LIKE: the escape
// character, % and _ are escaped, then * becomes % and ? becomes _.
func likePattern(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '\\', '%', '_':
			b.WriteString(likeEscape)
			b.WriteRune(r)
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
