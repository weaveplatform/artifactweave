package oidc

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Bounds on dynamic patterns. Administrators write the patterns, but the
// claims they read arrive in external tokens. Go's regular expressions run in
// time linear in their input, so bounding each value, the number of steps and
// the values a list claim expands to bounds the work one exchange can cause.
// Every intermediate value is bounded too, so an operator cannot grow a claim
// without limit.
const (
	maxPatternParts = 16
	maxValueBytes   = 1024
	maxClaimValues  = 100
)

// placeholder is a claim reference in a template, {{name}}, once the
// template's whitespace is removed.
var placeholder = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// opCall is an operator step, op:<name>("<from>","<to>").
var opCall = regexp.MustCompile(`^op:([A-Za-z]+)\((.*)\)$`)

type stepKind int

const (
	// stepMatch: the value must match re, whose groups later replacements
	// expand.
	stepMatch stepKind = iota
	// stepReplace: the value becomes the replacement, expanded with the
	// groups of the latest match, applied again to the current value.
	stepReplace
	// stepOp: a literal string replacement.
	stepOp
)

type step struct {
	kind     stepKind
	re       *regexp.Regexp
	repl     []replPiece
	op       string
	from, to string
}

// replPiece is literal text, or the text of a group when group is 0 or more.
type replPiece struct {
	lit   string
	group int
}

// pattern is a parsed username, groups or resource pattern (research 21,
// "Dynamic Mapping" and "Advanced Dynamic Mappings"). Its first part is a
// template naming one claim as {{claim}}; the template is a regular
// expression matched against the claim's value, the claim's place being a
// capture group. Each later part is an operator, op:replaceAll("a","b"),
// op:replaceFirst or op:replaceLast; a regular expression the value must
// match; or a replacement that rebuilds the value from the latest match's
// groups, $0 being the whole match.
type pattern struct {
	claim string
	steps []step
}

func parsePattern(s string) (pattern, error) {
	parts := strings.Split(s, "|")
	if len(parts) > maxPatternParts {
		return pattern{}, invalidf("pattern %q has more than %d parts", s, maxPatternParts)
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return pattern{}, invalidf("pattern %q has an empty part", s)
		}
	}
	claim, re, err := parseTemplate(s, parts[0])
	if err != nil {
		return pattern{}, err
	}
	p := pattern{claim: claim, steps: []step{{kind: stepMatch, re: re}}}
	rest := parts[1:]
	isOp := func(part string) bool { return strings.HasPrefix(part, "op:") }
	// Which non-operator parts are expressions and which replacements is
	// not written down: "template | replacement" expands the template's
	// groups and "template | regex | replacement" the regex's. A part is
	// therefore an expression when another non-operator part follows to
	// expand it and the part before it was not an expression waiting for
	// that, and a replacement otherwise. Every example on the page parses
	// this way.
	expectReplace := false
	for i, part := range rest {
		switch {
		case isOp(part):
			st, err := parseOp(s, part)
			if err != nil {
				return pattern{}, err
			}
			p.steps = append(p.steps, st)
		case !expectReplace && slices.ContainsFunc(rest[i+1:], func(q string) bool { return !isOp(q) }):
			if re, err = regexp.Compile(part); err != nil {
				return pattern{}, invalidf("pattern %q: %v", s, err)
			}
			p.steps = append(p.steps, step{kind: stepMatch, re: re})
			expectReplace = true
		default:
			pieces, err := parseReplacement(part, re)
			if err != nil {
				return pattern{}, invalidf("pattern %q: %v", s, err)
			}
			p.steps = append(p.steps, step{kind: stepReplace, repl: pieces})
			expectReplace = false
		}
	}
	return p, nil
}

// parseTemplate reads the first part of a pattern. Its whitespace is
// dropped, as the page's "({{actor}}) @jfrog.com|$1" turns
// "username@jfrog.com" into "username" only without it. The text around the
// placeholder is a regular expression, which that example's parentheses also
// need; a part never holds "|", so it cannot add an alternative that escapes
// the anchors.
func parseTemplate(s, part string) (string, *regexp.Regexp, error) {
	t := strings.Join(strings.Fields(part), "")
	refs := placeholder.FindAllStringSubmatchIndex(t, -1)
	switch len(refs) {
	case 0:
		return "", nil, invalidf("pattern %q references no claim ({{name}})", s)
	case 1:
	default:
		return "", nil, invalidf("pattern %q references more than one claim", s)
	}
	ref := refs[0]
	re, err := regexp.Compile(`^(?:` + t[:ref[0]] + `(.*)` + t[ref[1]:] + `)$`)
	if err != nil {
		return "", nil, invalidf("pattern %q: %v", s, err)
	}
	return t[ref[2]:ref[3]], re, nil
}

// parseOp reads an operator step. The operators replace literal text, not
// expressions: op:replaceAll(".","-") turns every period into a hyphen.
func parseOp(s, part string) (step, error) {
	m := opCall.FindStringSubmatch(part)
	if m == nil {
		return step{}, invalidf(`pattern %q: %q is not op:<name>("<from>","<to>")`, s, part)
	}
	switch m[1] {
	case "replaceAll", "replaceFirst", "replaceLast":
	default:
		return step{}, invalidf("pattern %q: unknown operator %q", s, m[1])
	}
	from, to, err := opArgs(m[2])
	if err != nil {
		return step{}, invalidf("pattern %q: %s: %v", s, part, err)
	}
	return step{kind: stepOp, op: m[1], from: from, to: to}, nil
}

// opArgs reads an operator's two double-quoted arguments, which take Go's
// string escapes. The first must not be empty, which would match between
// every character.
func opArgs(s string) (string, string, error) {
	var args []string
	s = strings.TrimSpace(s)
	for {
		if !strings.HasPrefix(s, `"`) {
			return "", "", invalidf("arguments must be double-quoted strings")
		}
		q, err := strconv.QuotedPrefix(s)
		if err != nil {
			return "", "", invalidf("unterminated argument")
		}
		v, _ := strconv.Unquote(q)
		args = append(args, v)
		s = strings.TrimSpace(s[len(q):])
		if len(args) == 2 {
			break
		}
		next, ok := strings.CutPrefix(s, ",")
		if !ok {
			return "", "", invalidf("two arguments are required")
		}
		s = strings.TrimSpace(next)
	}
	switch {
	case s != "":
		return "", "", invalidf("two arguments are required")
	case args[0] == "":
		return "", "", invalidf("the text to replace is empty")
	}
	return args[0], args[1], nil
}

// parseReplacement reads a replacement with the group references the
// page's examples use: $ and a group number, read as Java reads it so that
// "$1_$2" is group 1, "_", group 2; ${name} or ${number} for a group; and \
// before a character that stands for itself. Every reference must name a group of
// re. The replacement is expanded once, so a $ in a claim's value stays text.
func parseReplacement(r string, re *regexp.Regexp) ([]replPiece, error) {
	var out []replPiece
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, replPiece{lit: lit.String(), group: -1})
			lit.Reset()
		}
	}
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	groups := re.NumSubexp()
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '\\':
			if i+1 == len(r) {
				return nil, invalidf("the replacement ends in an escape")
			}
			i++
			lit.WriteByte(r[i])
		case '$':
			i++
			var g int
			switch {
			case i < len(r) && r[i] == '{':
				end := strings.IndexByte(r[i:], '}')
				if end < 0 {
					return nil, invalidf("the replacement has an unterminated ${")
				}
				name := r[i+1 : i+end]
				g = re.SubexpIndex(name)
				if n, err := strconv.Atoi(name); err == nil && n >= 0 && n <= groups {
					g = n
				}
				if g < 0 {
					return nil, invalidf("no group is named %q", name)
				}
				i += end
			case i < len(r) && isDigit(r[i]):
				// A further digit belongs to the number only while the
				// number still names a group.
				g = int(r[i] - '0')
				for i+1 < len(r) && isDigit(r[i+1]) && g*10+int(r[i+1]-'0') <= groups {
					g = g*10 + int(r[i+1]-'0')
					i++
				}
				if g > groups {
					return nil, invalidf("there is no group %d", g)
				}
			default:
				return nil, invalidf("$ must be followed by a group number or {name}")
			}
			flush()
			out = append(out, replPiece{group: g})
		default:
			lit.WriteByte(r[i])
		}
	}
	flush()
	return out, nil
}

// apply evaluates p against claims: one result for each value of its claim,
// so a list claim, such as a token's groups, yields a value per element.
func (p pattern) apply(claims map[string]any) ([]string, error) {
	v, ok := lookup(claims, p.claim)
	if !ok {
		return nil, fmt.Errorf("%w: the token has no claim %q", ErrNoMatch, p.claim)
	}
	list, isList := v.([]any)
	if !isList {
		list = []any{v}
	}
	if len(list) > maxClaimValues {
		return nil, fmt.Errorf(
			"%w: claim %q has more than %d values",
			ErrNoMatch, p.claim, maxClaimValues,
		)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := scalar(e)
		if !ok {
			return nil, fmt.Errorf(
				"%w: claim %q is not a string, number or boolean",
				ErrNoMatch,
				p.claim,
			)
		}
		r, err := p.eval(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// eval runs p's steps over one claim value. The value is only ever input:
// no step reads it as an expression or a replacement.
func (p pattern) eval(v string) (string, error) {
	tooLong := func() error {
		return fmt.Errorf("%w: claim %q gives a value longer than %d bytes",
			ErrNoMatch, p.claim, maxValueBytes)
	}
	if len(v) > maxValueBytes {
		return "", tooLong()
	}
	var re *regexp.Regexp
	for _, s := range p.steps {
		switch s.kind {
		case stepMatch:
			re = s.re
			if !re.MatchString(v) {
				return "", fmt.Errorf("%w: %q does not match %q", ErrNoMatch, v, re.String())
			}
		case stepReplace:
			m := re.FindStringSubmatchIndex(v)
			if m == nil {
				return "", fmt.Errorf("%w: %q does not match %q", ErrNoMatch, v, re.String())
			}
			v = expand(s.repl, v, m)
		case stepOp:
			if grown := len(
				v,
			) + strings.Count(
				v,
				s.from,
			)*(len(s.to)-len(s.from)); grown > maxValueBytes {
				return "", tooLong()
			}
			v = operate(s, v)
		}
		if len(v) > maxValueBytes {
			return "", tooLong()
		}
	}
	return v, nil
}

func expand(pieces []replPiece, src string, m []int) string {
	var b strings.Builder
	for _, p := range pieces {
		if p.group < 0 {
			b.WriteString(p.lit)
			continue
		}
		// A group that took no part in the match adds nothing.
		if lo := m[2*p.group]; lo >= 0 {
			b.WriteString(src[lo:m[2*p.group+1]])
		}
	}
	return b.String()
}

func operate(s step, v string) string {
	switch s.op {
	case "replaceAll":
		return strings.ReplaceAll(v, s.from, s.to)
	case "replaceFirst":
		return strings.Replace(v, s.from, s.to, 1)
	default: // replaceLast
		i := strings.LastIndex(v, s.from)
		if i < 0 {
			return v
		}
		return v[:i] + s.to + v[i+len(s.from):]
	}
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		return "", false
	}
}
