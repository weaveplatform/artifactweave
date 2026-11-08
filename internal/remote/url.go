// Package remote owns outbound HTTP for remote repositories. Callers must
// authorize the repository and path before consulting either cache or upstream.
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

func BaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrURL
	}
	return u, nil
}

func enabled(v *bool) bool { return v != nil && *v }
