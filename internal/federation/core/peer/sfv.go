package peer

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file parses and serializes the Structured Field Values (RFC 8941)
// that Signature-Input, Signature and Content-Digest carry. It covers
// dictionaries, inner lists, parameters and the bare items integer,
// string, token, byte sequence and boolean. Decimals are refused: no field
// the peer protocol reads carries one, and a refusal is safer than a
// lenient parse of a signed value.

// errStructured reports a field value that is not a valid structured field.
var errStructured = errors.New("malformed structured field")

// token is a structured field token, kept apart from a string so that a
// round trip serializes each as it came.
type token string

// param is one parameter of an item or inner list.
type param struct {
	key   string
	value any
}

// params keeps parameters in the order they were given, which the
// signature base repeats.
type params []param

// item is a bare item with its parameters.
type item struct {
	value  any
	params params
}

// innerList is a parenthesised list of items with its parameters.
type innerList struct {
	items  []item
	params params
}

// member is one dictionary entry; value is an item or an innerList.
type member struct {
	key   string
	value any
}

// dictionary keeps its members in order.
type dictionary []member

func (d dictionary) get(key string) (any, bool) {
	for _, m := range d {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

// parseDictionary parses a Dictionary (RFC 8941, section 4.2.2). Several
// field lines are joined with ", " by the caller first.
func parseDictionary(input string) (dictionary, error) {
	p := &sfParser{s: input}
	p.skipSP()
	var out dictionary
	for !p.done() {
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		var value any
		if p.peek() == '=' {
			p.i++
			value, err = p.itemOrInnerList()
		} else {
			var ps params
			ps, err = p.parameters()
			value = item{value: true, params: ps}
		}
		if err != nil {
			return nil, err
		}
		out = setMember(out, key, value)
		p.skipOWS()
		if p.done() {
			return out, nil
		}
		if p.peek() != ',' {
			return nil, fmt.Errorf("%w: expected a comma at %d", errStructured, p.i)
		}
		p.i++
		p.skipOWS()
		if p.done() {
			return nil, fmt.Errorf("%w: trailing comma", errStructured)
		}
	}
	return out, nil
}

// setMember replaces an existing key's value in place, as RFC 8941 says a
// repeated key does, or appends a new one.
func setMember(d dictionary, key string, value any) dictionary {
	for i := range d {
		if d[i].key == key {
			d[i].value = value
			return d
		}
	}
	return append(d, member{key: key, value: value})
}

// parseInnerList parses one inner list on its own, as a signature's
// "@signature-params" line serializes it.
func parseInnerList(input string) (innerList, error) {
	p := &sfParser{s: input}
	l, err := p.innerList()
	if err != nil {
		return innerList{}, err
	}
	if !p.done() {
		return innerList{}, fmt.Errorf("%w: trailing characters", errStructured)
	}
	return l, nil
}

type sfParser struct {
	s string
	i int
}

func (p *sfParser) done() bool { return p.i >= len(p.s) }

func (p *sfParser) peek() byte {
	if p.done() {
		return 0
	}
	return p.s[p.i]
}

func (p *sfParser) skipSP() {
	for !p.done() && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *sfParser) skipOWS() {
	for !p.done() && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *sfParser) itemOrInnerList() (any, error) {
	if p.peek() == '(' {
		return p.innerList()
	}
	return p.item()
}

func (p *sfParser) innerList() (innerList, error) {
	if p.peek() != '(' {
		return innerList{}, fmt.Errorf("%w: expected an inner list", errStructured)
	}
	p.i++
	var out innerList
	for !p.done() {
		p.skipSP()
		if p.peek() == ')' {
			p.i++
			ps, err := p.parameters()
			out.params = ps
			return out, err
		}
		it, err := p.item()
		if err != nil {
			return innerList{}, err
		}
		out.items = append(out.items, it)
		if c := p.peek(); c != ' ' && c != ')' {
			return innerList{}, fmt.Errorf("%w: expected a space or ')' at %d", errStructured, p.i)
		}
	}
	return innerList{}, fmt.Errorf("%w: unterminated inner list", errStructured)
}

func (p *sfParser) item() (item, error) {
	v, err := p.bareItem()
	if err != nil {
		return item{}, err
	}
	ps, err := p.parameters()
	return item{value: v, params: ps}, err
}

func (p *sfParser) parameters() (params, error) {
	var out params
	for p.peek() == ';' {
		p.i++
		p.skipSP()
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		var value any = true
		if p.peek() == '=' {
			p.i++
			if value, err = p.bareItem(); err != nil {
				return nil, err
			}
		}
		replaced := false
		for i := range out {
			if out[i].key == key {
				out[i].value, replaced = value, true
			}
		}
		if !replaced {
			out = append(out, param{key: key, value: value})
		}
	}
	return out, nil
}

func (p *sfParser) key() (string, error) {
	start := p.i
	if c := p.peek(); !isLCAlpha(c) && c != '*' {
		return "", fmt.Errorf("%w: expected a key at %d", errStructured, p.i)
	}
	for !p.done() {
		c := p.s[p.i]
		if !isLCAlpha(c) && !isDigit(c) && c != '_' && c != '-' && c != '.' && c != '*' {
			break
		}
		p.i++
	}
	return p.s[start:p.i], nil
}

func (p *sfParser) bareItem() (any, error) {
	switch c := p.peek(); {
	case c == '-' || isDigit(c):
		return p.integer()
	case c == '"':
		return p.str()
	case c == ':':
		return p.bytes()
	case c == '?':
		return p.boolean()
	case isAlpha(c) || c == '*':
		return p.tok(), nil
	default:
		return nil, fmt.Errorf("%w: unexpected %q at %d", errStructured, c, p.i)
	}
}

func (p *sfParser) integer() (int64, error) {
	start := p.i
	if p.peek() == '-' {
		p.i++
	}
	digits := p.i
	for !p.done() && isDigit(p.s[p.i]) {
		p.i++
	}
	switch {
	case p.peek() == '.':
		return 0, fmt.Errorf("%w: decimals are not accepted", errStructured)
	case p.i == digits || p.i-digits > 15:
		return 0, fmt.Errorf("%w: integer of %d digits", errStructured, p.i-digits)
	}
	n, err := strconv.ParseInt(p.s[start:p.i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errStructured, err)
	}
	return n, nil
}

func (p *sfParser) str() (string, error) {
	p.i++ // the opening quote
	var b strings.Builder
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch {
		case c == '\\':
			if p.done() || p.s[p.i] != '"' && p.s[p.i] != '\\' {
				return "", fmt.Errorf("%w: bad escape in a string", errStructured)
			}
			b.WriteByte(p.s[p.i])
			p.i++
		case c == '"':
			return b.String(), nil
		case c < 0x20 || c > 0x7e:
			return "", fmt.Errorf("%w: a string holds a non-printable character", errStructured)
		default:
			b.WriteByte(c)
		}
	}
	return "", fmt.Errorf("%w: unterminated string", errStructured)
}

func (p *sfParser) tok() token {
	start := p.i
	p.i++
	for !p.done() && (isTChar(p.s[p.i]) || p.s[p.i] == ':' || p.s[p.i] == '/') {
		p.i++
	}
	return token(p.s[start:p.i])
}

func (p *sfParser) bytes() ([]byte, error) {
	p.i++ // the opening colon
	end := strings.IndexByte(p.s[p.i:], ':')
	if end < 0 {
		return nil, fmt.Errorf("%w: unterminated byte sequence", errStructured)
	}
	raw := p.s[p.i : p.i+end]
	p.i += end + 1
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: byte sequence: %w", errStructured, err)
	}
	return b, nil
}

func (p *sfParser) boolean() (bool, error) {
	p.i++ // the question mark
	switch p.peek() {
	case '1':
		p.i++
		return true, nil
	case '0':
		p.i++
		return false, nil
	default:
		return false, fmt.Errorf("%w: bad boolean", errStructured)
	}
}

func isLCAlpha(c byte) bool { return c >= 'a' && c <= 'z' }
func isAlpha(c byte) bool   { return isLCAlpha(c) || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool   { return c >= '0' && c <= '9' }

// isTChar is RFC 9110's tchar.
func isTChar(c byte) bool {
	return isAlpha(c) || isDigit(c) || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// serializeBare renders a bare item (RFC 8941, section 4.1.3).
func serializeBare(b *strings.Builder, v any) {
	switch x := v.(type) {
	case bool:
		if x {
			b.WriteString("?1")
		} else {
			b.WriteString("?0")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case string:
		b.WriteByte('"')
		for i := 0; i < len(x); i++ {
			if x[i] == '"' || x[i] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(x[i])
		}
		b.WriteByte('"')
	case token:
		b.WriteString(string(x))
	case []byte:
		b.WriteByte(':')
		b.WriteString(base64.StdEncoding.EncodeToString(x))
		b.WriteByte(':')
	}
}

func serializeParams(b *strings.Builder, ps params) {
	for _, p := range ps {
		b.WriteByte(';')
		b.WriteString(p.key)
		if v, ok := p.value.(bool); ok && v {
			continue
		}
		b.WriteByte('=')
		serializeBare(b, p.value)
	}
}

func serializeItem(b *strings.Builder, it item) {
	serializeBare(b, it.value)
	serializeParams(b, it.params)
}

func serializeInnerList(b *strings.Builder, l innerList) {
	b.WriteByte('(')
	for i, it := range l.items {
		if i > 0 {
			b.WriteByte(' ')
		}
		serializeItem(b, it)
	}
	b.WriteByte(')')
	serializeParams(b, l.params)
}

// validString reports whether s can be a structured field string: printable
// ASCII only.
func validString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
