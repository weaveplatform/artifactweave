// Package remote owns outbound HTTP for remote repositories. Callers must
// authorize the repository and path before consulting either cache or upstream.
package remote

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var (
	ErrURL           = errors.New("invalid remote URL or path")
	ErrRequest       = errors.New("remote request failed")
	ErrRedirect      = errors.New("remote redirect refused")
	ErrConfiguration = errors.New("invalid remote transport configuration")
	ErrClosed        = errors.New("remote transport pool closed")
	ErrConnection    = errors.New("remote connection unavailable")
	ErrSecurity      = errors.New("remote transport security validation failed")
)

func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrURL
	}
	return u, nil
}

// requestURL treats Path as an escaped relative path. It never parses a path as
// an authority, and preserves raw query order and escaping for signed URLs.
func requestURL(c repo.Config, base *url.URL, escapedPath, query string) (*url.URL, error) {
	if strings.HasPrefix(escapedPath, "/") || strings.ContainsAny(escapedPath, "?#\\\r\n") {
		return nil, ErrURL
	}
	decoded, err := url.PathUnescape(escapedPath)
	if err != nil || strings.ContainsAny(decoded, "\\\x00\r\n") {
		return nil, ErrURL
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return nil, ErrURL
		}
	}
	path := strings.TrimRight(base.EscapedPath(), "/") + "/" + escapedPath
	if !enabled(c.DisableURLNormalization) {
		for strings.Contains(path, "//") {
			path = strings.ReplaceAll(path, "//", "/")
		}
	}
	u := *base
	u.Path, err = url.PathUnescape(path)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base path", ErrURL)
	}
	u.RawPath = path
	u.RawQuery = appendQuery(u.RawQuery, c.QueryParams)
	if enabled(c.PropagateQueryParams) {
		u.RawQuery = appendQuery(u.RawQuery, query)
	}
	if strings.ContainsAny(u.RawQuery, "#\r\n") {
		return nil, ErrURL
	}
	return &u, nil
}

func appendQuery(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "&" + b
}

func enabled(v *bool) bool { return v != nil && *v }
