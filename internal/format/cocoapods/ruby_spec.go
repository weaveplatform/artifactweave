package cocoapods

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	rubyAssignment = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.([a-z_]+(?:\.[a-z_]+)?)\s*=\s*(.+)$`)
	rubyDependency = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.dependency\s+(.+)$`)
	rubyNested     = regexp.MustCompile(
		`^([a-z][a-z0-9_]*)\.(subspec|test_spec|app_spec)\s+(.+?)\s+do\s+\|([a-z][a-z0-9_]*)\|$`,
	)
	rubyKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type rubyFrame struct {
	variable     string
	fields       map[string]any
	dependencies map[string][]string
	platforms    map[string]string
}

func (f *rubyFrame) finish() {
	if len(f.dependencies) > 0 {
		f.fields["dependencies"] = f.dependencies
	}
	if len(f.platforms) > 0 {
		f.fields["platforms"] = f.platforms
	}
}

func platformName(v string) bool {
	switch v {
	case "ios", "osx", "tvos", "watchos", "visionos":
		return true
	default:
		return false
	}
}

// staticRubySpec recognizes data-only assignments and nested static spec
// blocks. It never evaluates Ruby, resolves variables, or runs hooks. Unknown
// statements fail the upload so CDN metadata cannot misrepresent the archive.
//
//nolint:gocyclo // Static assignments, nested blocks and refusal paths share one parser state.
func staticRubySpec(data []byte) (json.RawMessage, error) {
	var root *rubyFrame
	var stack []*rubyFrame
	statements, err := rubyStatements(data)
	if err != nil {
		return nil, err
	}
	for _, line := range statements {
		if root == nil && line == "Pod::Spec.new do |s|" {
			root = &rubyFrame{
				variable: "s", fields: map[string]any{},
				dependencies: map[string][]string{}, platforms: map[string]string{},
			}
			stack = append(stack, root)
			continue
		}
		if line == "end" && len(stack) > 0 {
			stack[len(stack)-1].finish()
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) == 0 {
			return nil, invalid("unsupported Ruby podspec statement")
		}
		current := stack[len(stack)-1]
		if m := rubyNested.FindStringSubmatch(line); m != nil {
			if m[1] != current.variable || len(stack) >= 16 {
				return nil, invalid("invalid static nested podspec")
			}
			name, e := rubyLiteral(m[3])
			n, ok := name.(string)
			if e != nil || !ok || !ValidName(n) {
				return nil, invalid("invalid static subspec name")
			}
			field := map[string]string{"subspec": "subspecs", "test_spec": "test_specs", "app_spec": "app_specs"}[m[2]]
			child := &rubyFrame{
				variable: m[4], fields: map[string]any{"name": n},
				dependencies: map[string][]string{}, platforms: map[string]string{},
			}
			children, _ := current.fields[field].([]any)
			current.fields[field] = append(children, child.fields)
			stack = append(stack, child)
			continue
		}
		if m := rubyAssignment.FindStringSubmatch(line); m != nil {
			if m[1] != current.variable {
				return nil, invalid("podspec assignment uses another block variable")
			}
			key := m[2]
			if key == "platform" {
				parts, e := rubyParts(m[3])
				if e != nil || len(parts) != 2 {
					return nil, invalid("invalid static platform")
				}
				name, e := rubyLiteral(parts[0])
				version, ve := rubyLiteral(parts[1])
				n, ok := name.(string)
				if e != nil || ve != nil || !ok || !platformName(n) {
					return nil, invalid("invalid static platform")
				}
				v, ok := version.(string)
				if !ok {
					return nil, invalid("invalid static platform version")
				}
				current.platforms[n] = v
				continue
			}
			value, e := rubyLiteral(m[3])
			if e != nil {
				return nil, fmt.Errorf("static podspec %s: %w", key, e)
			}
			if strings.Contains(key, ".") {
				platform, field, _ := strings.Cut(key, ".")
				if !platformName(platform) {
					return nil, invalid("invalid static platform field")
				}
				if field == "deployment_target" {
					version, ok := value.(string)
					if !ok {
						return nil, invalid("invalid static deployment version")
					}
					current.platforms[platform] = version
				} else {
					platformFields, _ := current.fields[platform].(map[string]any)
					if platformFields == nil {
						platformFields = map[string]any{}
						current.fields[platform] = platformFields
					}
					platformFields[field] = value
				}
				continue
			}
			if key == "author" {
				key = "authors"
			}
			current.fields[key] = value
			continue
		}
		if m := rubyDependency.FindStringSubmatch(line); m != nil {
			if m[1] != current.variable {
				return nil, invalid("podspec dependency uses another block variable")
			}
			parts, e := rubyParts(m[2])
			if e != nil || len(parts) == 0 {
				return nil, invalid("invalid static dependency")
			}
			name, e := rubyLiteral(parts[0])
			if e != nil {
				return nil, e
			}
			n, ok := name.(string)
			if !ok || n == "" || len(parts) > 8 {
				return nil, invalid("invalid static dependency name")
			}
			for _, part := range parts[1:] {
				v, e := rubyLiteral(part)
				s, ok := v.(string)
				if e != nil || !ok {
					return nil, invalid("invalid static dependency requirement")
				}
				current.dependencies[n] = append(current.dependencies[n], s)
			}
			if len(parts) == 1 {
				current.dependencies[n] = []string{}
			}
			continue
		}
		return nil, invalid("unsupported Ruby podspec statement")
	}
	if root == nil || len(stack) != 0 {
		return nil, invalid("incomplete Ruby podspec")
	}
	encoded, err := json.Marshal(root.fields)
	if err != nil {
		return nil, fmt.Errorf("encode static podspec: %w", err)
	}
	return encoded, nil
}

// rubyStatements joins only literal hash/array continuations. A complete
// statement must finish before the next one; quoted multiline Ruby strings
// and method/chained expressions are deliberately outside this grammar.
func rubyStatements(data []byte) ([]string, error) {
	var statements []string
	var pending string
	depth := 0
	for _, raw := range strings.Split(string(data), "\n") {
		line, err := stripRubyComment(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pending != "" {
			pending += " "
		}
		pending += line
		var quote byte
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '\\':
				if quote != 0 {
					i++
				}
			case '\'', '"':
				switch quote {
				case 0:
					quote = line[i]
				case line[i]:
					quote = 0
				}
			case '{', '[':
				if quote == 0 {
					depth++
				}
			case '}', ']':
				if quote == 0 {
					depth--
				}
			}
			if depth < 0 {
				return nil, invalid("invalid Ruby literal continuation")
			}
		}
		if depth == 0 {
			statements = append(statements, pending)
			pending = ""
		}
	}
	if pending != "" || depth != 0 {
		return nil, invalid("unterminated Ruby literal continuation")
	}
	return statements, nil
}

func stripRubyComment(line string) (string, error) {
	var quote byte
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if quote != 0 {
				i++
			}
		case '\'', '"':
			switch quote {
			case 0:
				quote = line[i]
			case line[i]:
				quote = 0
			}
		case '#':
			if quote == 0 {
				return line[:i], nil
			}
		}
	}
	if quote != 0 {
		return "", invalid("unterminated Ruby string")
	}
	return line, nil
}

func rubyParts(s string) ([]string, error) {
	var parts []string
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if quote != 0 {
				i++
			}
		case '\'', '"':
			switch quote {
			case 0:
				quote = s[i]
			case s[i]:
				quote = 0
			}
		case '{', '[':
			if quote == 0 {
				depth++
			}
		case '}', ']':
			if quote == 0 {
				depth--
			}
		case ',':
			if quote == 0 && depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
		if depth < 0 {
			return nil, invalid("invalid Ruby literal nesting")
		}
	}
	if quote != 0 || depth != 0 {
		return nil, invalid("invalid Ruby literal")
	}
	last := strings.TrimSpace(s[start:])
	if last != "" {
		parts = append(parts, last)
	} else if start == 0 || s[len(s)-1] != ',' {
		return nil, invalid("empty Ruby literal part")
	}
	return parts, nil
}

//nolint:gocyclo // Recursive literal grammar checks each supported data form and rejects expressions.
func rubyLiteral(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, invalid("empty Ruby literal")
	}
	if strings.Contains(s, "#{") || strings.ContainsAny(s, "`;$") {
		return nil, invalid("dynamic Ruby expression")
	}
	if s[0] == '\'' || s[0] == '"' {
		if len(s) < 2 || s[len(s)-1] != s[0] {
			return nil, invalid("invalid quoted literal")
		}
		if s[0] == '"' {
			value, err := strconv.Unquote(s)
			if err != nil {
				return nil, fmt.Errorf("decode quoted podspec literal: %w", err)
			}
			return value, nil
		}
		var out strings.Builder
		for i := 1; i < len(s)-1; i++ {
			if s[i] == '\'' {
				return nil, invalid("unescaped Ruby quote")
			}
			if s[i] == '\\' {
				if i+1 >= len(s)-1 || s[i+1] != '\'' && s[i+1] != '\\' {
					return nil, invalid("unsupported Ruby string escape")
				}
				i++
			}
			out.WriteByte(s[i])
		}
		return out.String(), nil
	}
	if s[0] == ':' {
		if !rubyKey.MatchString(s[1:]) {
			return nil, invalid("invalid Ruby symbol")
		}
		return s[1:], nil
	}
	if s == "true" || s == "false" {
		return s == "true", nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}, nil
		}
		parts, err := rubyParts(inner)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			v, e := rubyLiteral(p)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		return out, nil
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		out := map[string]any{}
		if inner == "" {
			return out, nil
		}
		parts, err := rubyParts(inner)
		if err != nil {
			return nil, err
		}
		for _, p := range parts {
			keyPart, valuePart, ok := strings.Cut(p, "=>")
			if !ok {
				keyPart, valuePart, ok = strings.Cut(p, ":")
				if !ok || !rubyKey.MatchString(strings.TrimSpace(keyPart)) {
					return nil, invalid("invalid Ruby hash entry")
				}
			} else {
				keyPart = strings.TrimSpace(keyPart)
			}
			key, e := rubyLiteral(keyPart)
			if !ok || e != nil {
				if rubyKey.MatchString(keyPart) {
					key = keyPart
				} else {
					return nil, invalid("invalid Ruby hash key")
				}
			}
			k, ok := key.(string)
			if !ok || k == "" {
				return nil, invalid("invalid Ruby hash key")
			}
			v, e := rubyLiteral(valuePart)
			if e != nil {
				return nil, e
			}
			out[k] = v
		}
		return out, nil
	}
	return nil, invalid("nonliteral Ruby expression")
}
