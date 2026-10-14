package oidc

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
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

// placeholder is a claim reference in a dynamic mapping, {{name}}.
var placeholder = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

// pattern is a parsed username or groups pattern (Artifactory 7.95+): a
// template of {{claim}} references, optionally followed by "| replacement"
// or "| regex | replacement", where $0 is the whole match and $1 the first
// group.
type pattern struct {
	template    string
	re          *regexp.Regexp
	replacement string
}

func parsePattern(s string) (pattern, error) {
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	p := pattern{template: parts[0]}
	if !placeholder.MatchString(p.template) {
		return pattern{}, invalid("pattern %q references no claim ({{name}})", s)
	}
	switch len(parts) {
	case 1:
	case 2:
		p.re, p.replacement = regexp.MustCompile(`^.*$`), parts[1]
	case 3:
		re, err := regexp.Compile(parts[1])
		if err != nil {
			return pattern{}, invalid("pattern %q: %v", s, err)
		}
		p.re, p.replacement = re, parts[2]
	default:
		return pattern{}, invalid("pattern %q has more than three parts", s)
	}
	return p, nil
}

// apply expands p against claims. A claim the token carries as a list
// yields one value per element, which is how a groups pattern maps every
// group the token names; a username pattern must yield exactly one.
func (p pattern) apply(claims map[string]any) ([]string, error) {
	values := []string{p.template}
	for _, m := range placeholder.FindAllStringSubmatch(p.template, -1) {
		v, ok := lookup(claims, m[1])
		if !ok {
			return nil, fmt.Errorf("%w: the token has no claim %q", ErrNoMatch, m[1])
		}
		var subs []string
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				subs = append(subs, scalar(e))
			}
		default:
			subs = []string{scalar(x)}
		}
		var next []string
		for _, cur := range values {
			for _, sub := range subs {
				next = append(next, strings.ReplaceAll(cur, m[0], sub))
			}
		}
		values = next
	}
	if p.re == nil {
		return values, nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		idx := p.re.FindStringSubmatchIndex(v)
		if idx == nil {
			return nil, fmt.Errorf("%w: %q does not match %q", ErrNoMatch, v, p.re.String())
		}
		out = append(out, string(p.re.ExpandString(nil, p.replacement, v, idx)))
	}
	return out, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
