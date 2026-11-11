package fakeupstream

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Redacted is recorded in place of any value that could carry a secret: every
// query value, every header but Accept, Accept-Encoding, Content-Length,
// Content-Type, Range, User-Agent and the If- conditionals, and every
// occurrence of Options.Secrets or the Auth password or token in a value the
// recorder keeps. Tests compare recorded values with it.
const Redacted = "[REDACTED]"

// Request is safe to include in assertion failures. It never contains a request
// body, URL userinfo, raw query values or credential/cookie header values.
type Request struct {
	Method                    string
	Path                      string
	Host                      string
	Header                    http.Header
	Query                     url.Values
	At                        time.Time
	Authorized                bool
	VerifiedClientCertificate bool
	matches                   func(string, string) bool
	pathMatches               func(string) bool
}

// HeaderMatches reports whether the request's first value of header name equals
// value. It compares keyed hashes, so a test can check a credential it knows
// without the record holding the plaintext; a header the request lacked
// matches nothing.
func (r Request) HeaderMatches(name, value string) bool {
	return r.matches != nil && r.matches(name, value)
}

var visibleHeaders = map[string]bool{
	"Accept": true, "Accept-Encoding": true, "Content-Length": true,
	"Content-Type": true, "If-Match": true, "If-None-Match": true,
	"If-Modified-Since": true, "If-Unmodified-Since": true, "If-Range": true,
	"Range": true, "User-Agent": true,
}

func (s *Server) redact(value string) string {
	secrets := append([]string(nil), s.options.Secrets...)
	secrets = append(secrets, s.options.Auth.Password, s.options.Auth.Token)
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, Redacted)
		}
	}
	return value
}

func (s *Server) record(r *http.Request, allowed bool) {
	header := make(http.Header, len(r.Header))
	hashes := make(map[string][]byte, len(r.Header))
	for name, values := range r.Header {
		name = http.CanonicalHeaderKey(name)
		mac := hmac.New(sha256.New, s.key[:])
		_, _ = mac.Write([]byte(r.Header.Get(name)))
		hashes[name] = mac.Sum(nil)
		if visibleHeaders[name] {
			for _, value := range values {
				header.Add(name, s.redact(value))
			}
		} else {
			header.Set(name, Redacted)
		}
	}
	query := r.URL.Query()
	for name := range query {
		query[name] = []string{Redacted}
	}
	pathMAC := hmac.New(sha256.New, s.key[:])
	_, _ = pathMAC.Write([]byte(r.URL.Path))
	pathHash := pathMAC.Sum(nil)
	record := Request{
		Method: r.Method, Path: s.redact(r.URL.Path), Host: s.redact(r.Host),
		Header: header, Query: query, At: s.options.Now(), Authorized: allowed,
		VerifiedClientCertificate: r.TLS != nil && len(r.TLS.VerifiedChains) != 0,
		pathMatches: func(path string) bool {
			mac := hmac.New(sha256.New, s.key[:])
			_, _ = mac.Write([]byte(path))
			return hmac.Equal(pathHash, mac.Sum(nil))
		},
		matches: func(name, value string) bool {
			mac := hmac.New(sha256.New, s.key[:])
			_, _ = mac.Write([]byte(value))
			return hmac.Equal(hashes[http.CanonicalHeaderKey(name)], mac.Sum(nil))
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, record)
}

// Requests returns independent snapshots in arrival order, including denied,
// missing and subsequently interrupted requests. Counts never reset implicitly.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	for i, request := range s.requests {
		out[i] = request
		out[i].Header = request.Header.Clone()
		out[i].Query = make(url.Values, len(request.Query))
		for name, values := range request.Query {
			out[i].Query[name] = append([]string(nil), values...)
		}
	}
	return out
}

// Count matches the decoded path before redaction; empty method/path match all.
func (s *Server) Count(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, request := range s.requests {
		if (method == "" || request.Method == method) && (path == "" || request.pathMatches(path)) {
			n++
		}
	}
	return n
}
