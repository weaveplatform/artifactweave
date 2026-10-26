package source

import (
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ValidGitLFSActionHeaders limits upstream-selected basic-transfer headers to
// bounded end-to-end fields. The transport applies them only to the exact
// validated action URL and removes them before following any redirect.
func ValidGitLFSActionHeaders(headers http.Header) bool {
	if len(headers) > 16 {
		return false
	}
	total := 0
	for name, values := range headers {
		if !httpguts.ValidHeaderFieldName(name) ||
			http.CanonicalHeaderKey(name) != name || len(values) != 1 ||
			unsafeGitLFSActionHeader(name) || !httpguts.ValidHeaderFieldValue(values[0]) {
			return false
		}
		total += len(name) + len(values[0])
		if len(values[0]) > 4096 || total > 8192 {
			return false
		}
	}
	return true
}

func unsafeGitLFSActionHeader(name string) bool {
	if strings.HasPrefix(name, "Content-") || strings.HasPrefix(name, "Proxy-") ||
		strings.HasPrefix(name, "X-Forwarded-") {
		return true
	}
	switch name {
	case "Host", "Cookie", "Set-Cookie", "Connection", "Keep-Alive", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Forwarded", "X-Forwarded", "Origin", "Referer", "X-Real-Ip":
		return true
	}
	return false
}
