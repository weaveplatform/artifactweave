// Package cdntest is a fake CDN edge for tests (decision 0029). It checks a
// provider's URL signature the way the real edge does, then serves the blob
// the URL names from a blob store, honouring Range and the response
// overrides, so a download redirect is tested end to end without a cloud.
//
// The edge listens on "localhost" by default. A registry under test listens
// on 127.0.0.1, so Go clients, the JFrog CLI among them, treat the edge as
// another host and drop their Authorization header on the redirect, as they
// do for a real CDN; the edge records whether one arrived.
package cdntest

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // CloudFront's default hash, Cloud CDN's only one
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// ErrSignature reports a request the edge refuses: no signature, a bad one,
// an unknown key or an expired URL.
var ErrSignature = errors.New("cdntest: signature refused")

// Validator checks a request's signature as one CDN does.
type Validator interface {
	Validate(r *http.Request, now time.Time) error
}

// Options configures Start.
type Options struct {
	// Validator checks every request; required.
	Validator Validator
	// Origin serves the blobs; required. The last segment of a request path
	// is the blob's SHA-256, as in every store's object keys.
	Origin blob.Store
	// Host is the address the edge listens on; empty means "localhost".
	Host string
	// Now is the clock signatures are checked against; nil means time.Now.
	Now func() time.Time
}

// Request is one request the edge answered.
type Request struct {
	Method string
	Path   string
	Range  string
	// Authorization reports whether the request carried that header, which
	// a client must not send to a CDN on another host.
	Authorization bool
	Status        int
	// Cached reports a Front Door edge answering from its cache.
	Cached bool
}

// Edge is a running fake edge.
type Edge struct {
	// URL is the edge's base URL, for example "http://localhost:51234"; use
	// it as the CDN domain.
	URL string

	opts Options
	// frontDoor, when set, makes the edge a Front Door that passes requests
	// to a storage origin instead of checking a signature (StartFrontDoor).
	frontDoor *frontDoorEdge
	srv       *http.Server
	done      chan struct{}
	mu        sync.Mutex
	reqs      []Request
}

// Start listens and serves until Close.
func Start(o Options) (*Edge, error) {
	if o.Validator == nil || o.Origin == nil {
		return nil, errors.New("cdntest: a validator and an origin are required")
	}
	if o.Host == "" {
		o.Host = "localhost"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	ln, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp",
		net.JoinHostPort(o.Host, "0"),
	)
	if err != nil {
		return nil, fmt.Errorf("cdntest: listen: %w", err)
	}
	e := &Edge{opts: o, done: make(chan struct{})}
	e.URL = "http://" + net.JoinHostPort(o.Host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	r := chi.NewRouter()
	r.Get("/*", e.serve)
	r.Head("/*", e.serve)
	e.srv = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(e.done)
		_ = e.srv.Serve(ln)
	}()
	return e, nil
}

// Close stops the edge.
func (e *Edge) Close() error {
	err := e.srv.Close()
	<-e.done
	return err //nolint:wrapcheck // the server's own close error
}

// Requests returns the requests answered so far, oldest first.
func (e *Edge) Requests() []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Request(nil), e.reqs...)
}

func (e *Edge) serve(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	hit := false
	if e.frontDoor != nil {
		hit = e.frontDoor.answer(sw, r)
	} else {
		e.answer(sw, r)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reqs = append(e.reqs, Request{
		Method: r.Method, Path: r.URL.Path, Range: r.Header.Get("Range"),
		Authorization: r.Header.Get("Authorization") != "", Status: sw.status, Cached: hit,
	})
}

func (e *Edge) answer(w http.ResponseWriter, r *http.Request) {
	if err := e.opts.Validator.Validate(r, e.opts.Now()); err != nil {
		http.Error(w, "AccessDenied: "+err.Error(), http.StatusForbidden)
		return
	}
	sha := path.Base(r.URL.Path)
	if blob.ValidateSHA256(sha) != nil {
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	}
	rc, _, err := e.opts.Origin.Open(r.Context(), sha)
	switch {
	case errors.Is(err, blob.ErrNotFound):
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "origin: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = rc.Close() }()
	q := r.URL.Query()
	if v := q.Get("response-content-disposition"); v != "" {
		w.Header().Set("Content-Disposition", v)
	}
	ct := q.Get("response-content-type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, "", time.Time{}, rc)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

// CloudFront validates canned-policy signed URLs as CloudFront does with a
// trusted key group: Keys maps each public key ID to its key. Custom
// policies are not modelled.
type CloudFront struct {
	Keys map[string]crypto.PublicKey
}

// cloudFrontParams are the query parameters CloudFront adds when it signs,
// and removes before it compares the URL with the policy's resource.
var cloudFrontParams = map[string]bool{
	"Expires": true, "Signature": true, "Key-Pair-Id": true, "Hash-Algorithm": true,
}

// Validate checks r's canned-policy signature at now.
func (v CloudFront) Validate(r *http.Request, now time.Time) error {
	q := r.URL.Query()
	if q.Has("Policy") {
		return fmt.Errorf("%w: custom policies are not modelled", ErrSignature)
	}
	exp, sig, id := q.Get("Expires"), q.Get("Signature"), q.Get("Key-Pair-Id")
	if exp == "" || sig == "" || id == "" {
		return fmt.Errorf("%w: missing Expires, Signature or Key-Pair-Id", ErrSignature)
	}
	expires, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: Expires %q", ErrSignature, exp)
	}
	if now.Unix() >= expires {
		return fmt.Errorf("%w: expired at %d", ErrSignature, expires)
	}
	key, ok := v.Keys[id]
	if !ok {
		return fmt.Errorf("%w: unknown key %q", ErrSignature, id)
	}
	hasher, h := sha1.New(), crypto.SHA1 //nolint:gosec // CloudFront's default
	switch alg := q.Get("Hash-Algorithm"); alg {
	case "":
	case "SHA256":
		hasher, h = sha256.New(), crypto.SHA256
	default:
		return fmt.Errorf("%w: Hash-Algorithm %q", ErrSignature, alg)
	}
	policy, err := cannedPolicy(resource(r), time.Unix(expires, 0))
	if err != nil {
		return err
	}
	_, _ = hasher.Write(policy)
	digest := hasher.Sum(nil)
	raw, err := base64.StdEncoding.DecodeString(
		strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(sig),
	)
	if err != nil {
		return fmt.Errorf("%w: Signature is not base64", ErrSignature)
	}
	switch k := key.(type) {
	case *rsa.PublicKey:
		if rsa.VerifyPKCS1v15(k, h, digest, raw) != nil {
			return fmt.Errorf("%w: bad RSA signature", ErrSignature)
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(k, digest, raw) {
			return fmt.Errorf("%w: bad ECDSA signature", ErrSignature)
		}
	default:
		return fmt.Errorf("%w: key %q is %T", ErrSignature, id, key)
	}
	return nil
}

// requestBase is r's scheme and host, as the client addressed the edge.
func requestBase(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// resource is the URL r was signed for: as the client sent it, without the
// signature parameters, which CloudFront appends after the rest.
func resource(r *http.Request) string {
	rawPath, rawQuery, _ := strings.Cut(r.RequestURI, "?")
	var kept []string
	for p := range strings.SplitSeq(rawQuery, "&") {
		name, _, _ := strings.Cut(p, "=")
		if p != "" && !cloudFrontParams[name] {
			kept = append(kept, p)
		}
	}
	u := requestBase(r) + rawPath
	if len(kept) > 0 {
		u += "?" + strings.Join(kept, "&")
	}
	return u
}

// cannedPolicy is the JSON CloudFront signs for a canned policy, encoded as
// the AWS SDK encodes it: no HTML escaping, no surrounding whitespace.
func cannedPolicy(res string, expires time.Time) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(sign.NewCannedPolicy(res, expires)); err != nil {
		return nil, fmt.Errorf("cdntest: encode policy: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// CloudCDN validates URL signatures as Cloud CDN does: Keys maps each key
// name on the backend bucket to its raw 16-byte key. Cloud CDN itself passes
// an unsigned request to the origin, which refuses it because the bucket is
// private (decision 0029); the fake refuses it at the edge with the same
// status. URLPrefix signatures and signed cookies are not modelled.
type CloudCDN struct {
	Keys map[string][]byte
}

// cloudCDNSignature separates what Cloud CDN signs from the signature, which
// is always the last parameter.
const cloudCDNSignature = "&Signature="

// Validate checks r's URL signature at now.
func (v CloudCDN) Validate(r *http.Request, now time.Time) error {
	q := r.URL.Query()
	if q.Has("URLPrefix") {
		return fmt.Errorf("%w: URLPrefix signatures are not modelled", ErrSignature)
	}
	exp, name := q.Get("Expires"), q.Get("KeyName")
	full := requestBase(r) + r.RequestURI
	i := strings.LastIndex(full, cloudCDNSignature)
	if exp == "" || name == "" || i < 0 {
		return fmt.Errorf("%w: missing Expires, KeyName or Signature", ErrSignature)
	}
	signed, sig := full[:i], full[i+len(cloudCDNSignature):]
	if strings.Contains(sig, "&") ||
		!strings.HasSuffix(signed, "Expires="+exp+"&KeyName="+name) {
		return fmt.Errorf(
			"%w: Expires, KeyName and Signature must end the URL, in that order",
			ErrSignature,
		)
	}
	expires, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: Expires %q", ErrSignature, exp)
	}
	if now.Unix() >= expires {
		return fmt.Errorf("%w: expired at %d", ErrSignature, expires)
	}
	key, ok := v.Keys[name]
	if !ok {
		return fmt.Errorf("%w: unknown key %q", ErrSignature, name)
	}
	got, err := base64.URLEncoding.DecodeString(sig)
	if err != nil {
		got, err = base64.RawURLEncoding.DecodeString(sig)
	}
	if err != nil {
		return fmt.Errorf("%w: Signature is not base64url", ErrSignature)
	}
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(signed))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("%w: bad HMAC signature", ErrSignature)
	}
	return nil
}
