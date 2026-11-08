package remote

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Origin is u's scheme and host in lower case, without a trailing dot, with
// the port the scheme implies when u names none: two URLs with the same
// Origin reach the same server, which credential, redirect and pagination
// checks compare.
func Origin(u *url.URL) string {
	return strings.ToLower(
		u.Scheme,
	) + "://" + net.JoinHostPort(
		strings.TrimSuffix(strings.ToLower(u.Hostname()), "."),
		Port(u),
	)
}

// Port is u's port, or the one its scheme implies when it names none: 443
// for https and 80 otherwise, so that Origin, CheckedURL and the transport's
// dialer compare and connect to the same port.
func Port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// CheckedURL parses raw as an absolute http or https URL with a host and no
// user information, fragment or opaque part, and refuses with ErrSecurity one
// that could address something other than what it shows: an escaped or
// control character in the host, a port outside 1 to 65535, a backslash, NUL
// or line break in the decoded path, a dot segment, or a '#' or line break in
// the query. It looks nothing up; the destination policy checks the
// addresses before a request is sent.
func CheckedURL(raw string) (*url.URL, error) {
	u, err := BaseURL(raw)
	if err != nil || strings.ContainsAny(u.Hostname(), "%\\\x00\r\n") {
		return nil, ErrSecurity
	}
	p, err := strconv.Atoi(Port(u))
	if err != nil || p < 1 || p > 65535 {
		return nil, ErrSecurity
	}
	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.ContainsAny(path, "\\\x00\r\n") {
		return nil, ErrSecurity
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return nil, ErrSecurity
		}
	}
	if strings.ContainsAny(u.RawQuery, "#\r\n") {
		return nil, ErrSecurity
	}
	return u, nil
}
