package aql

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The query text is AQL's method-chain syntax around JSON values. The JSON
// is parsed here rather than by encoding/json because AQL objects may
// repeat a key ({"path":...,"path":...} is an implicit $and) and because
// errors must point at the offending offset.

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokString
	tokNumber
	tokPunct
)

type token struct {
	kind tokKind
	text string // identifier, punctuation, number literal, or decoded string
	pos  int
}

type lexer struct {
	src string
	pos int
}

func syntaxErr(pos int, format string, args ...any) error {
	return fmt.Errorf("%w at offset %d: %s", ErrSyntax, pos, fmt.Sprintf(format, args...))
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.src) && strings.IndexByte(" \t\r\n", l.src[l.pos]) >= 0 {
		l.pos++
	}
	start := l.pos
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, pos: start}, nil
	}
	c := l.src[l.pos]
	switch {
	case strings.IndexByte(".(){}[]:,", c) >= 0:
		l.pos++
		return token{kind: tokPunct, text: string(c), pos: start}, nil
	case c == '"':
		return l.str()
	case c == '-' || isDigit(c):
		return l.number()
	case isIdentStart(c):
		for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
			l.pos++
		}
		return token{kind: tokIdent, text: l.src[start:l.pos], pos: start}, nil
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
	return token{}, syntaxErr(start, "unexpected %q", r)
}

// str scans a JSON string and decodes it with encoding/json, which checks
// escapes and rejects control characters.
func (l *lexer) str() (token, error) {
	start := l.pos
	l.pos++
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
			continue
		case '"':
			l.pos++
			var s string
			if err := json.Unmarshal([]byte(l.src[start:l.pos]), &s); err != nil {
				return token{}, syntaxErr(start, "invalid string: %v", err)
			}
			return token{kind: tokString, text: s, pos: start}, nil
		}
		l.pos++
	}
	return token{}, syntaxErr(start, "unterminated string")
}

// number scans a JSON number literal.
func (l *lexer) number() (token, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
	}
	digits := func() int {
		n := 0
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
			n++
		}
		return n
	}
	if digits() == 0 {
		return token{}, syntaxErr(start, "invalid number")
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		if digits() == 0 {
			return token{}, syntaxErr(start, "invalid number")
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if digits() == 0 {
			return token{}, syntaxErr(start, "invalid number")
		}
	}
	return token{kind: tokNumber, text: l.src[start:l.pos], pos: start}, nil
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }

// valueKind is the kind of a JSON value.
type valueKind int

const (
	vObject valueKind = iota
	vArray
	vString
	vNumber
	vBool
	vNull
)

// value is a JSON value that keeps object members in order, duplicates
// included.
type value struct {
	kind    valueKind
	pos     int
	members []member
	items   []value
	text    string // string contents or number literal
	b       bool
}

type member struct {
	key string
	pos int
	val value
}

type parser struct {
	lx  lexer
	tok token
}

func (p *parser) advance() error {
	t, err := p.lx.next()
	p.tok = t
	return err
}

func (p *parser) punct(s string) bool { return p.tok.kind == tokPunct && p.tok.text == s }

func (p *parser) expect(s string) error {
	if !p.punct(s) {
		return syntaxErr(p.tok.pos, "expected %q, found %s", s, p.describe())
	}
	return p.advance()
}

func (p *parser) describe() string {
	if p.tok.kind == tokEOF {
		return "end of query"
	}
	return strconv.Quote(p.tok.text)
}

// maxValueDepth bounds raw JSON nesting; criteria nesting is checked
// against maxDepth when the tree is built, and an $and or $or level is an
// object and an array.
const maxValueDepth = 2*maxDepth + 4

func (p *parser) value(depth int) (value, error) {
	if depth > maxValueDepth {
		return value{}, syntaxErr(p.tok.pos, "nesting deeper than %d levels", maxDepth)
	}
	t := p.tok
	switch {
	case p.punct("{"):
		return p.object(depth)
	case p.punct("["):
		if err := p.advance(); err != nil {
			return value{}, err
		}
		v := value{kind: vArray, pos: t.pos}
		for !p.punct("]") {
			if len(v.items) > 0 {
				if err := p.expect(","); err != nil {
					return value{}, err
				}
			}
			item, err := p.value(depth + 1)
			if err != nil {
				return value{}, err
			}
			v.items = append(v.items, item)
		}
		return v, p.advance()
	case t.kind == tokString:
		return value{kind: vString, pos: t.pos, text: t.text}, p.advance()
	case t.kind == tokNumber:
		return value{kind: vNumber, pos: t.pos, text: t.text}, p.advance()
	case t.kind == tokIdent && (t.text == "true" || t.text == "false"):
		return value{kind: vBool, pos: t.pos, b: t.text == "true"}, p.advance()
	case t.kind == tokIdent && t.text == "null":
		return value{kind: vNull, pos: t.pos}, p.advance()
	}
	return value{}, syntaxErr(t.pos, "expected a value, found %s", p.describe())
}

func (p *parser) object(depth int) (value, error) {
	v := value{kind: vObject, pos: p.tok.pos}
	if err := p.advance(); err != nil {
		return value{}, err
	}
	for !p.punct("}") {
		if len(v.members) > 0 {
			if err := p.expect(","); err != nil {
				return value{}, err
			}
		}
		if p.tok.kind != tokString {
			return value{}, syntaxErr(p.tok.pos, "expected a quoted key, found %s", p.describe())
		}
		m := member{key: p.tok.text, pos: p.tok.pos}
		if err := p.advance(); err != nil {
			return value{}, err
		}
		if err := p.expect(":"); err != nil {
			return value{}, err
		}
		val, err := p.value(depth + 1)
		if err != nil {
			return value{}, err
		}
		m.val = val
		v.members = append(v.members, m)
	}
	return v, p.advance()
}

// otherDomains are AQL's other primary domains, refused as unsupported
// rather than as a syntax error.
var otherDomains = []string{
	"builds", "build", "entries", "archive", "properties", "property", "releases",
	"release_artifacts", "promotions", "modules", "dependencies", "artifacts",
}

// Parse parses an items query. Errors wrap ErrEmpty, ErrSyntax,
// ErrUnsupported, ErrInvalid or ErrTooLong.
func Parse(src string) (*Query, error) {
	if strings.TrimSpace(src) == "" {
		return nil, ErrEmpty
	}
	if utf8.RuneCountInString(src) > MaxLength {
		return nil, ErrTooLong
	}
	p := &parser{lx: lexer{src: src}}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.tok.kind != tokIdent {
		return nil, syntaxErr(p.tok.pos, "expected a domain, found %s", p.describe())
	}
	domain := p.tok.text
	if domain != "items" {
		if slices.Contains(otherDomains, domain) {
			return nil, fmt.Errorf(
				"%w: the %s domain (only items is served)",
				ErrUnsupported,
				domain,
			)
		}
		return nil, syntaxErr(p.tok.pos, "unknown domain %q", domain)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if err := p.expect("."); err != nil {
		return nil, err
	}
	if p.tok.kind != tokIdent || p.tok.text != "find" {
		if p.tok.kind == tokIdent && (p.tok.text == "delete" || p.tok.text == "update") {
			return nil, fmt.Errorf("%w: items.%s", ErrUnsupported, p.tok.text)
		}
		return nil, syntaxErr(p.tok.pos, "expected find, found %s", p.describe())
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	q := &Query{Distinct: true}
	var root Node
	if !p.punct(")") {
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		if v.kind != vObject {
			return nil, syntaxErr(v.pos, "find takes an object")
		}
		if len(v.members) > 0 {
			if root, err = criteria(v, 0); err != nil {
				return nil, err
			}
		}
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	// The root is always an And, so the implicit criteria below can join it.
	switch r := root.(type) {
	case nil:
		root = And{}
	case And:
	default:
		root = And{r}
	}
	if !mentionsType(root) {
		root = append(root.(And), &FieldCriterion{Field: FieldType, Op: OpEq, Text: "file"})
	}
	q.Criteria = root
	seen := map[string]bool{}
	for p.punct(".") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.tok.kind != tokIdent {
			return nil, syntaxErr(p.tok.pos, "expected a modifier, found %s", p.describe())
		}
		name, pos := p.tok.text, p.tok.pos
		if seen[name] {
			return nil, syntaxErr(pos, "%s given twice", name)
		}
		seen[name] = true
		if err := p.advance(); err != nil {
			return nil, err
		}
		if err := p.expect("("); err != nil {
			return nil, err
		}
		if err := p.modifier(q, name, pos); err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
	}
	if p.tok.kind != tokEOF {
		return nil, syntaxErr(p.tok.pos, "unexpected %s after the query", p.describe())
	}
	if err := q.validate(); err != nil {
		return nil, err
	}
	// An "@<key>" include also filters out items without that key
	// (research 22 §1.2), as an any-value criterion would.
	for _, k := range q.keyIncludes() {
		q.Criteria = append(q.Criteria.(And), &PropertyCriterion{Key: k, Op: OpEq, Value: "*"})
	}
	return q, nil
}

// validate applies Artifactory's output rules (research 22 §1.2): sort,
// offset and limit only with an include of item fields alone, sorting only
// on fields in the output, and virtual_repos only beside repo, path and
// name.
func (q *Query) validate() error {
	if (q.Sort != nil || q.HasOffset || q.HasLimit) && !q.primaryOnly() {
		return fmt.Errorf(
			"%w: sort, offset and limit need an include of item fields only",
			ErrInvalid,
		)
	}
	fields := q.Fields()
	if q.Sort != nil {
		for _, f := range q.Sort.Fields {
			if !slices.Contains(fields, f) {
				return fmt.Errorf("%w: sort on %q, which the output leaves out", ErrInvalid, f)
			}
		}
	}
	if q.VirtualRepos() {
		for _, f := range []Field{FieldRepo, FieldPath, FieldName} {
			if !slices.Contains(fields, f) {
				return fmt.Errorf(
					"%w: virtual_repos needs repo, path and name in the output",
					ErrInvalid,
				)
			}
		}
	}
	return nil
}

// modifier parses the arguments of one suffix.
func (p *parser) modifier(q *Query, name string, pos int) error {
	var args []value
	for !p.punct(")") {
		if len(args) > 0 {
			if err := p.expect(","); err != nil {
				return err
			}
		}
		v, err := p.value(0)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	switch name {
	case "include":
		for _, a := range args {
			if a.kind != vString {
				return syntaxErr(a.pos, "include takes field names")
			}
			if !includable(a.text) {
				return fmt.Errorf("%w: include of %q", ErrUnsupported, a.text)
			}
			q.Include = append(q.Include, a.text)
		}
		return nil
	case "sort":
		s, err := sortOf(args, pos)
		q.Sort = s
		return err
	case "offset", "limit":
		if len(args) != 1 || args[0].kind != vNumber {
			return syntaxErr(pos, "%s takes one number", name)
		}
		n, err := strconv.Atoi(args[0].text)
		if err != nil || n < 0 {
			return syntaxErr(args[0].pos, "%s must be a non-negative integer", name)
		}
		if name == "offset" {
			q.Offset, q.HasOffset = n, true
		} else {
			q.Limit, q.HasLimit = n, true
		}
		return nil
	case "transitive":
		if len(args) != 0 {
			return syntaxErr(pos, "transitive takes no arguments")
		}
		q.Transitive = true
		return nil
	case "distinct":
		if len(args) != 1 || args[0].kind != vBool {
			return syntaxErr(pos, "distinct takes true or false")
		}
		q.Distinct = args[0].b
		return nil
	}
	return syntaxErr(pos, "unknown modifier %q", name)
}

// includable reports whether name may appear in an include: an item field,
// "*", virtual_repos, the property secondary domain ("property",
// "property.*", "property.key", "property.value" or "@<key>"), or the stat
// domain ("stat", "stat.*" or "stat.<field>").
func includable(name string) bool {
	if _, ok := fieldKinds[Field(name)]; ok {
		return true
	}
	switch name {
	case "*", FieldVirtualRepos, "property", "property.*", "property.key", "property.value",
		"stat", "stat.*":
		return true
	}
	if f, ok := strings.CutPrefix(name, "stat."); ok {
		_, known := statKinds[StatField(f)]
		return known
	}
	k, ok := strings.CutPrefix(name, "@")
	return ok && k != "" && k != "*"
}

func sortOf(args []value, pos int) (*Sort, error) {
	if len(args) != 1 || args[0].kind != vObject || len(args[0].members) != 1 {
		return nil, syntaxErr(pos, `sort takes {"$asc":[...]} or {"$desc":[...]}`)
	}
	m := args[0].members[0]
	if (m.key != "$asc" && m.key != "$desc") || m.val.kind != vArray || len(m.val.items) == 0 {
		return nil, syntaxErr(m.pos, `sort takes {"$asc":[...]} or {"$desc":[...]}`)
	}
	s := &Sort{Desc: m.key == "$desc"}
	for _, f := range m.val.items {
		if f.kind != vString {
			return nil, syntaxErr(f.pos, "sort takes field names")
		}
		if _, ok := fieldKinds[Field(f.text)]; !ok {
			return nil, fmt.Errorf("%w: sort on %q (only item fields sort)", ErrUnsupported, f.text)
		}
		s.Fields = append(s.Fields, Field(f.text))
	}
	return s, nil
}

// mentionsType reports whether the criteria name the type field anywhere;
// without it AQL searches files only.
func mentionsType(n Node) bool {
	switch n := n.(type) {
	case And:
		return slices.ContainsFunc(n, mentionsType)
	case Or:
		return slices.ContainsFunc(n, mentionsType)
	case *FieldCriterion:
		return n.Field == FieldType
	}
	return false
}
