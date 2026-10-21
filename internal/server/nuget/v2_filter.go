package nuget

import (
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
)

type v2Version struct {
	meta             nugetfmt.Metadata
	latest, absolute bool
}

type v2Predicate func(v2Version) bool

// This parser accepts only the predicates V2FeedQueryBuilder emits:
// latest flags, the Id/Description/Tags null guards and substringof with
// tolower, joined by and/or and parentheses. A size/depth bound prevents
// hostile nested expressions from consuming an unbounded stack. There
// are no arbitrary property reads, comparisons or OData function calls.
type v2FilterParser struct {
	rest       string
	substrings bool
	valid      bool
}

func parseV2Filter(s string, substrings bool) (v2Predicate, bool) {
	if len(s) > 16*1024 {
		return nil, false
	}
	if strings.TrimSpace(s) == "" {
		return func(v2Version) bool { return true }, true
	}
	p := v2FilterParser{rest: s, substrings: substrings, valid: true}
	pred := p.or(0)
	return pred, p.valid && strings.TrimSpace(p.rest) == ""
}

func (p *v2FilterParser) take(s string) bool {
	p.rest = strings.TrimSpace(p.rest)
	if !strings.HasPrefix(p.rest, s) {
		return false
	}
	// Keywords cannot consume a prefix of a longer identifier.
	if s == "and" || s == "or" || s == "ne" || s == "null" {
		if len(p.rest) > len(s) {
			c := p.rest[len(s)]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
				return false
			}
		}
	}
	p.rest = p.rest[len(s):]
	return true
}

func (p *v2FilterParser) need(s string) {
	if !p.take(s) {
		p.valid = false
	}
}

func (p *v2FilterParser) or(depth int) v2Predicate {
	left := p.and(depth)
	for p.valid && p.take("or") {
		first, right := left, p.and(depth)
		left = func(v v2Version) bool { return first(v) || right(v) }
	}
	return left
}

func (p *v2FilterParser) and(depth int) v2Predicate {
	left := p.atom(depth)
	for p.valid && p.take("and") {
		first, right := left, p.atom(depth)
		left = func(v v2Version) bool { return first(v) && right(v) }
	}
	return left
}

func (p *v2FilterParser) atom(depth int) v2Predicate {
	no := func(v2Version) bool { return false }
	if depth >= 32 || !p.valid {
		p.valid = false
		return no
	}
	if p.take("(") {
		pred := p.or(depth + 1)
		p.need(")")
		return pred
	}
	if p.take("IsAbsoluteLatestVersion") {
		return func(v v2Version) bool { return v.absolute }
	}
	if p.take("IsLatestVersion") {
		return func(v v2Version) bool { return v.latest }
	}
	if !p.substrings {
		p.valid = false
		return no
	}
	if p.take("substringof(") {
		term := p.literal()
		p.need(",")
		p.need("tolower(")
		field := p.field()
		p.need(")")
		p.need(")")
		return func(v v2Version) bool { return strings.Contains(strings.ToLower(field(v.meta)), term) }
	}
	field := p.field()
	p.need("ne")
	p.need("null")
	return func(v v2Version) bool { return field(v.meta) != "" }
}

func (p *v2FilterParser) field() func(nugetfmt.Metadata) string {
	switch {
	case p.take("Id"):
		return func(m nugetfmt.Metadata) string { return m.ID }
	case p.take("Description"):
		return func(m nugetfmt.Metadata) string { return m.Description }
	case p.take("Tags"):
		return func(m nugetfmt.Metadata) string { return " " + m.Tags + " " }
	default:
		p.valid = false
		return func(nugetfmt.Metadata) string { return "" }
	}
}

func (p *v2FilterParser) literal() string {
	p.rest = strings.TrimSpace(p.rest)
	if !strings.HasPrefix(p.rest, "'") {
		p.valid = false
		return ""
	}
	for i := 1; i < len(p.rest); i++ {
		if p.rest[i] != '\'' {
			continue
		}
		if i+1 < len(p.rest) && p.rest[i+1] == '\'' {
			i++
			continue
		}
		literal := p.rest[:i+1]
		p.rest = p.rest[i+1:]
		value, _ := odataLiteral(literal)
		return value
	}
	p.valid = false
	return ""
}
