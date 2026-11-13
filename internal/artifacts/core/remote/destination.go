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
// addresses before a request is sent. The only path it does not judge this
// way is the matrix string a request appends, which CheckedRequestURL
// checks by its own rule.
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

// CheckedRequestURL is CheckedURL for a request whose escaped path ends with
// matrix, the matrix string TransportRequest appended after the path
// (Request.Matrix). The URL without matrix, which holds the repository URL
// and the item path, must pass CheckedURL as any URL must, and matrix must
// pass CheckedMatrix. With an empty matrix, or a URL whose escaped path
// does not end with it, such as a redirect elsewhere, the whole URL is
// checked by CheckedURL.
func CheckedRequestURL(raw, matrix string) (*url.URL, error) {
	if matrix == "" {
		return CheckedURL(raw)
	}
	u, err := BaseURL(raw)
	if err != nil {
		return nil, ErrSecurity
	}
	bare, ok := strings.CutSuffix(u.EscapedPath(), matrix)
	if !ok {
		return CheckedURL(raw)
	}
	path, pathErr := url.PathUnescape(bare)
	if CheckedMatrix(matrix) != nil || pathErr != nil {
		return nil, ErrSecurity
	}
	without := *u
	without.Path, without.RawPath = path, bare
	if _, err := CheckedURL(without.String()); err != nil {
		return nil, err
	}
	return u, nil
}

// CheckedMatrix refuses with ErrSecurity a matrix string, as it follows an
// upstream path, that is not ";"-led and escaped throughout (RFC 3986
// segment bytes and %XX escapes, nothing else), or whose decoded form holds
// a "." or ".." piece between "/", "\" or NUL.
//
// 7.x sends a remote request's matrix parameters as HttpRepo.encodeIfNeeded
// leaves them (7.161.26): ArtifactoryRequestBase.processMatrixParams
// URL-decodes each key and value, RemoteRepoBase.buildRequestMatrixParams
// appends them as they are, and HttpUtils.encodeUrl escapes the path with
// URIUtil.encodeQuery, whose allowed_query set is RFC 2396's uric without
// "%". A backslash, NUL, CR and LF fall outside it and travel as %5C, %00,
// %0D and %0A, which this check accepts where CheckedURL refuses them in
// the rest of the path. As escapes they are printable octets of the request
// line, and HTTP/1.1 frames a message on raw CR LF before any decoding, so
// they split neither our request nor its headers. An upstream that decodes
// the path and copies it into a request or header of its own carries the
// line break on; that is the upstream's fault, and 7.x sends it the same
// bytes.
//
// encodeQuery keeps "/" and ".", so 7.x sends a value "a/../b" as it is and
// the upstream resolves another file than the one asked for: a 7.x fault,
// not followed. Escaped as %2F the value would stay in one segment only for
// an upstream that resolves dot segments before it decodes; one that
// decodes first, as nginx does, would step out of the path. So a string
// whose decoded form holds a dot piece is refused, and TransportRequest
// leaves it off. Pieces are cut at "\" too, which some upstreams read as a
// separator, and at NUL, which a C upstream reads as the end of the path or
// drops, so that neither escape can be decoded into a step.
func CheckedMatrix(matrix string) error {
	if !strings.HasPrefix(matrix, ";") {
		return ErrSecurity
	}
	decoded := make([]byte, 0, len(matrix))
	for i := 0; i < len(matrix); i++ {
		switch c := matrix[i]; {
		case c == '%' && i+2 < len(matrix) && isHex(matrix[i+1]) && isHex(matrix[i+2]):
			decoded = append(decoded, unhex(matrix[i+1])<<4|unhex(matrix[i+2]))
			i += 2
		case c == '%' || !segmentByte(c):
			return ErrSecurity
		default:
			decoded = append(decoded, c)
		}
	}
	start := 0
	for i := 0; i <= len(decoded); i++ {
		if i < len(decoded) && decoded[i] != '/' && decoded[i] != '\\' && decoded[i] != 0 {
			continue
		}
		if piece := string(decoded[start:i]); piece == "." || piece == ".." {
			return ErrSecurity
		}
		start = i + 1
	}
	return nil
}

// unhex is the value of c, a hexadecimal digit.
func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}
