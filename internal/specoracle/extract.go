package specoracle

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

var fencedJSON = regexp.MustCompile("(?s)```json[ \t]*\n(\\s*\\{.*?)\n```")

// ExtractBlock finds the OpenAPI document embedded in an endpoint page. The
// pages carry it under an "OpenAPI definition" heading as a fenced json block;
// older renderings inline the JSON. Both are handled, and HTML entities are
// decoded because some pages escape angle brackets inside descriptions. The
// returned bytes are compacted JSON so cache hashes are stable across
// whitespace changes on the site.
func ExtractBlock(page string) ([]byte, bool) {
	candidates := make([]string, 0, 2)
	for _, m := range fencedJSON.FindAllStringSubmatch(page, -1) {
		candidates = append(candidates, m[1])
	}
	if i := strings.Index(page, `{"openapi"`); i >= 0 {
		candidates = append(candidates, page[i:])
	}
	if i := strings.Index(page, "{\n  \"openapi\""); i >= 0 {
		candidates = append(candidates, page[i:])
	}
	for _, c := range candidates {
		if strings.Contains(c, "&lt;") || strings.Contains(c, "&gt;") ||
			strings.Contains(c, "&amp;") ||
			strings.Contains(c, "&quot;") {
			c = html.UnescapeString(c)
		}
		var raw json.RawMessage
		dec := json.NewDecoder(strings.NewReader(c))
		if err := dec.Decode(&raw); err != nil {
			continue
		}
		if !bytes.Contains(raw, []byte(`"openapi"`)) {
			continue
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			continue
		}
		return buf.Bytes(), true
	}
	return nil, false
}
