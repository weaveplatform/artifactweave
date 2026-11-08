package remote

import (
	"errors"
	"net/url"
)

var (
	ErrURL           = errors.New("remote: invalid url or path")
	ErrRequest       = errors.New("remote: request failed")
	ErrRedirect      = errors.New("remote: redirect refused")
	ErrConfiguration = errors.New("remote: invalid transport configuration")
	ErrClosed        = errors.New("remote: transport pool closed")
	ErrConnection    = errors.New("remote: connection unavailable")
	ErrSecurity      = errors.New("remote: transport security validation failed")
)

// BaseURL parses a remote repository's configured URL: absolute http or
// https with a host, and no user information, fragment or opaque part.
// Anything else answers ErrURL. Unlike CheckedURL it does not judge the
// host, port or path; the transport does that when it connects.
func BaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrURL
	}
	return u, nil
}

func enabled(v *bool) bool { return v != nil && *v }
