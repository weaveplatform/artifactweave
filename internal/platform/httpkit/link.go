package httpkit

import (
	"fmt"
	"net/url"
	"strings"
)

// NextLink is the value of a Link header naming the next page of a list,
// `<target>; rel="next"` (RFC 8288), built as decision 0043 settles it: the
// target is path with the query the client sent, every parameter kept as
// sent, in its order, and only position set to value. A query without
// position gets it appended; one that repeats it keeps the first, set to
// value, and drops the rest, so the next request reads one position. Bytes
// a URI may not hold, such as a space or the angle brackets around the
// target, are percent-encoded, so a raw query a server accepted cannot end
// the target early.
func NextLink(path, rawQuery, position, value string) string {
	next := position + "=" + url.QueryEscape(value)
	var parts []string
	set := false
	for part := range strings.SplitSeq(rawQuery, "&") {
		name, _, _ := strings.Cut(part, "=")
		if n, err := url.QueryUnescape(name); err == nil && n == position {
			if !set {
				parts, set = append(parts, next), true
			}
			continue
		}
		if part != "" {
			parts = append(parts, uriSafe(part))
		}
	}
	if !set {
		parts = append(parts, next)
	}
	return "<" + uriSafe(path) + "?" + strings.Join(parts, "&") + `>; rel="next"`
}

// uriSafe percent-encodes every byte of s that RFC 3986 allows in no part
// of a URI, leaving reserved and unreserved characters and existing escapes
// as they are.
func uriSafe(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c > ' ' && c < 0x7f && !strings.ContainsRune(`"<>\^`+"`{|}", rune(c)) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
