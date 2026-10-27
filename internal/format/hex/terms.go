package hex

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errMetadata = errors.New("hex: invalid hex metadata.config")

type tuple []any

type termParser struct {
	data  []byte
	pos   int
	nodes int
}

// parseMetadata reads the bounded subset of Erlang terms prescribed by the
// Hex package-metadata specification. It does not evaluate Erlang code.
func parseMetadata(data []byte) (map[string]any, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return nil, errMetadata
	}
	p := termParser{data: data}
	fields := make(map[string]any)
	for {
		p.space()
		if p.pos == len(data) {
			break
		}
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		p.space()
		if !p.consume('.') {
			return nil, errMetadata
		}
		kv, ok := v.(tuple)
		if !ok || len(kv) != 2 {
			return nil, errMetadata
		}
		key, ok := kv[0].(string)
		if !ok || key == "" {
			return nil, errMetadata
		}
		if _, exists := fields[key]; exists {
			return nil, errMetadata
		}
		fields[key] = kv[1]
	}
	return fields, nil
}

func (p *termParser) value(depth int) (any, error) {
	p.space()
	p.nodes++
	if depth > 24 || p.nodes > 20000 || p.pos >= len(p.data) {
		return nil, errMetadata
	}
	switch p.data[p.pos] {
	case '<':
		return p.binary()
	case '[':
		return p.sequence('[', ']', depth)
	case '{':
		items, err := p.sequence('{', '}', depth)
		return tuple(items), err
	case 't':
		if p.word("true") {
			return true, nil
		}
	case 'f':
		if p.word("false") {
			return false, nil
		}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		start := p.pos
		if p.data[p.pos] == '-' {
			p.pos++
		}
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		n, err := strconv.ParseInt(string(p.data[start:p.pos]), 10, 64)
		if err != nil {
			return nil, errMetadata
		}
		return n, nil
	}
	return nil, errMetadata
}

func (p *termParser) sequence(open, close byte, depth int) ([]any, error) {
	if !p.consume(open) {
		return nil, errMetadata
	}
	p.space()
	if p.consume(close) {
		return []any{}, nil
	}
	var out []any
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.space()
		if p.consume(close) {
			return out, nil
		}
		if !p.consume(',') {
			return nil, errMetadata
		}
	}
}

func (p *termParser) binary() (string, error) {
	if !p.prefix("<<\"") {
		return "", errMetadata
	}
	start := p.pos
	var b strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '"' {
			if !p.prefix(">>") {
				return "", errMetadata
			}
			if !utf8.ValidString(b.String()) {
				return "", errMetadata
			}
			return b.String(), nil
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if p.pos >= len(p.data) {
			return "", errMetadata
		}
		esc := p.data[p.pos]
		p.pos++
		switch esc {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\', '"':
			b.WriteByte(esc)
		default:
			return "", fmt.Errorf("%w: unsupported escape at byte %d", errMetadata, start)
		}
	}
	return "", errMetadata
}

func (p *termParser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		case '%':
			for p.pos < len(p.data) && p.data[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *termParser) consume(c byte) bool {
	if p.pos >= len(p.data) || p.data[p.pos] != c {
		return false
	}
	p.pos++
	return true
}

func (p *termParser) prefix(s string) bool {
	if !strings.HasPrefix(string(p.data[p.pos:]), s) {
		return false
	}
	p.pos += len(s)
	return true
}

func (p *termParser) word(s string) bool {
	if !strings.HasPrefix(string(p.data[p.pos:]), s) {
		return false
	}
	end := p.pos + len(s)
	if end < len(p.data) {
		c := p.data[end]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			return false
		}
	}
	p.pos = end
	return true
}
