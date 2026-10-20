package pypi

import "strings"

// Fields are the header fields of a core metadata file, by name in lower
// case, as the names are not case-sensitive; each holds its values in the
// order the file gives them, since Classifier, Requires-Dist and others
// are used more than once.
type Fields map[string][]string

// Get is the first value of the field name, or "".
func (f Fields) Get(name string) string {
	if v := f[strings.ToLower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ParseFields reads the header fields of a whole core metadata file, as
// the JSON API needs them (decision 0037).
func ParseFields(text string) Fields {
	f, _, _ := parseHeader(strings.TrimPrefix(text, string(utf8BOM)), true)
	return f
}

// Description reads the body after the metadata headers, falling back to the
// legacy Description header. It shares the format's tolerant header parsing.
func Description(text string) string {
	text = strings.TrimPrefix(text, string(utf8BOM))
	f, block, _ := parseHeader(text, true)
	body := strings.TrimPrefix(strings.TrimPrefix(text[len(block):], "\r"), "\n")
	if body != "" {
		return body
	}
	return f.Get("Description")
}

// parseHeader reads the header block of a core metadata file the way
// Python's email parser, which the packaging library uses, reads one
// (compat32): "Name: value" lines, each continued by the lines after it
// that start with a space or a tab, up to the first empty line or the
// first line that is no field, where the body, the description, starts.
// Only the block holds fields: a description line that looked like one
// once became pypi.name in Artifactory (RTFACT-18571). It returns the
// fields and the block. When text is not the whole file, ok is false
// unless the block ends within it.
func parseHeader(text string, whole bool) (Fields, string, bool) {
	f := Fields{}
	var key string
	for pos := 0; pos < len(text); {
		end := strings.IndexByte(text[pos:], '\n')
		if end < 0 && !whole {
			break
		}
		line, next := text[pos:], len(text)
		if end >= 0 {
			line, next = text[pos:pos+end], pos+end+1
		}
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return f, text[:pos], true
		}
		if line[0] == ' ' || line[0] == '\t' {
			if v := f[key]; len(v) > 0 {
				v[len(v)-1] += "\n" + line
			}
			pos = next
			continue
		}
		name, value, ok := cutField(line)
		if !ok {
			return f, text[:pos], true
		}
		key = strings.ToLower(name)
		f[key] = append(f[key], strings.TrimLeft(value, " \t"))
		pos = next
	}
	if !whole {
		return nil, "", false
	}
	return f, text, true
}

// cutField splits a field line at its first colon; ok is false for a line
// that is no field, whose name before the colon would hold a character
// outside printable ASCII or a space, as Python's header pattern
// ([\041-\071\073-\176]*:) refuses.
func cutField(line string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	for i := range len(name) {
		if name[i] < '!' || name[i] > '~' {
			return "", "", false
		}
	}
	return name, value, true
}
