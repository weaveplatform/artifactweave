package remote

import (
	"errors"
	"net/url"
)

// The transport's error vocabulary is declared in core so that the policy,
// Smart Remote and the format rules can match an upstream failure with
// errors.Is without importing the transport (decision 0044).
var (
	// ErrURL is returned when a configured repository URL fails BaseURL, or
	// when a request path or query cannot be placed below it: a leading
	// slash, a dot segment, or an escaped or control character that would
	// change what the URL addresses.
	ErrURL = errors.New("remote: invalid url or path")
	// ErrRequest is returned when the transport or a format rule refuses a
	// request it will not send, such as a method, body, header or target
	// the exchange does not allow. When a sent exchange fails, the transport
	// returns it wrapping the cause: ErrRedirect, ErrSecurity, ErrConnection
	// or the context's error.
	ErrRequest = errors.New("remote: request failed")
	// ErrRedirect is returned when a redirect is refused: one the redirect
	// rules forbid (more than ten hops, user information, a scheme other
	// than http or https, or https to http), a redirect of a POST, or any
	// redirect on a connection that follows none, such as Send's.
	ErrRedirect = errors.New("remote: redirect refused")
	// ErrConfiguration is returned when a remote's transport settings cannot
	// be applied: an unusable proxy, local address, certificate or stored
	// credential, or a registry client for an identity that is not a
	// complete external HTTP source.
	ErrConfiguration = errors.New("remote: invalid transport configuration")
	// ErrClosed is returned by the transport's and the registry clients'
	// pools after Close, at application shutdown.
	ErrClosed = errors.New("remote: transport pool closed")
	// ErrConnection is returned when the upstream cannot be reached: its
	// name resolves to no address, the proxy tunnel fails, or the connection
	// fails or ends early.
	ErrConnection = errors.New("remote: connection unavailable")
	// ErrSecurity is returned when a target fails the destination or TLS
	// checks: CheckedURL refuses it, it resolves to a cloud metadata address
	// or to a private one the operator did not allow, an Outbound hook
	// refuses it, an unguarded connection is asked to vouch for it, its
	// certificate does not verify, or the proxy refuses its credentials.
	ErrSecurity = errors.New("remote: transport security validation failed")
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
