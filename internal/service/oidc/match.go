package oidc

import (
	"reflect"
	"regexp"
	"strings"
)

// matches reports whether claims satisfy every claim a mapping names
// (research 21): a string matches with the * and ** wildcards, a list
// matches when any element does, an object matches its nested claims, and a
// claim the token carries as a list matches when any element does. A dotted
// name the token does not carry as such reaches into nested objects.
func matches(want, claims map[string]any) bool {
	for name, w := range want {
		got, ok := lookup(claims, name)
		if !ok || !matchValue(w, got) {
			return false
		}
	}
	return true
}

// lookup finds a claim by name, or by a dotted path into nested objects.
func lookup(claims map[string]any, name string) (any, bool) {
	if v, ok := claims[name]; ok {
		return v, true
	}
	head, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		return nil, false
	}
	inner, ok := claims[head].(map[string]any)
	if !ok {
		return nil, false
	}
	return lookup(inner, rest)
}

func matchValue(want, got any) bool {
	if list, ok := got.([]any); ok {
		for _, g := range list {
			if matchValue(want, g) {
				return true
			}
		}
		return false
	}
	switch w := want.(type) {
	case []any:
		for _, e := range w {
			if matchValue(e, got) {
				return true
			}
		}
		return false
	case string:
		g, ok := got.(string)
		return ok && glob(w).MatchString(g)
	case map[string]any:
		g, ok := got.(map[string]any)
		return ok && matches(w, g)
	default:
		return reflect.DeepEqual(want, got)
	}
}

// glob compiles a claim pattern: ** matches anything, * anything but a
// slash, so "octo-org/*" names the repositories of one organisation and
// "octo-org/**" everything below it.
func glob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
