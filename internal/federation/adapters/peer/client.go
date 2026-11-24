package peer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// Client defaults. A call is bounded in time per attempt and in size both
// ways; retries are few and short, because every caller of the peer client
// is itself a retried background job (decision 0033).
const (
	DefaultSocketTimeout   = 30 * time.Second
	DefaultAttemptTimeout  = 2 * time.Minute
	DefaultMaxResponseBody = 64 << 20
	DefaultAttempts        = 3
	DefaultBackoff         = 500 * time.Millisecond
)

// Errors a call wraps besides the transport's own.
var (
	// ErrTooLarge: a request or answer body exceeds its bound.
	ErrTooLarge = errors.New("peer message body exceeds its bound")
	// ErrNoPeerKey: the peer has no recorded key and the call supplied
	// none for its answer.
	ErrNoPeerKey = errors.New("no key to verify the peer's answer with")
)

// UnverifiedAnswerError is an answer whose signature did not verify: a
// refusal the peer cannot sign (401 to a request it could not verify), an
// intermediary's error, or a tampered answer. Status says what arrived;
// nothing else about it can be trusted, and its body is not returned.
type UnverifiedAnswerError struct {
	Status int
	Err    error
}

func (e *UnverifiedAnswerError) Error() string {
	return fmt.Sprintf("peer answer %d is not signed by the peer: %v", e.Status, e.Err)
}

func (e *UnverifiedAnswerError) Unwrap() error { return e.Err }

// ClientOptions is the peer client's trusted configuration.
type ClientOptions struct {
	// Signer returns this instance's signer.
	Signer func(context.Context) (corepeer.Signer, error)
	// Proxy returns the outbound proxy and its NTLM identity, nil for a
	// direct connection; pushhttp.PlatformProxy over the configuration
	// store is the application's. Nil connects directly.
	Proxy func(context.Context) (*url.URL, *outbound.NTLMProxy, error)
	// RootCAs extends the system roots peers' TLS certificates are
	// verified against; nil uses the system roots alone. TLS is always
	// verified: signatures are an additional layer, not a replacement.
	RootCAs *x509.CertPool
	// AllowedPrivateOrigins grants peers on private addresses, as the
	// remote and push clients' setting does.
	AllowedPrivateOrigins []string
	// LookupIP replaces DNS for tests.
	LookupIP func(context.Context, string) ([]net.IPAddr, error)
	// SocketTimeout bounds connecting and waiting for an answer's headers;
	// 0 is DefaultSocketTimeout.
	SocketTimeout time.Duration
	// AttemptTimeout bounds one attempt, the answer's body included; 0 is
	// DefaultAttemptTimeout.
	AttemptTimeout time.Duration
	// MaxRequestBody and MaxResponseBody bound bodies; 0 is
	// DefaultMaxRequestBody and DefaultMaxResponseBody.
	MaxRequestBody  int64
	MaxResponseBody int64
	// Attempts is how many times a retryable call is tried; 0 is
	// DefaultAttempts. Backoff is the wait before the second attempt,
	// doubled for each later one; 0 is DefaultBackoff.
	Attempts int
	Backoff  time.Duration
	// Now is the clock answers are checked against; nil is time.Now.
	Now func() time.Time
}

// Factory builds clients for peers. It holds no connection itself.
type Factory struct {
	o      ClientOptions
	policy *outbound.Policy
	index  *operations.Index
}

// NewFactory validates o without network I/O.
func NewFactory(o ClientOptions) (*Factory, error) {
	if o.Signer == nil {
		return nil, fmt.Errorf("peer client: %w: no signer", outbound.ErrConfiguration)
	}
	policy, err := outbound.NewPolicy(o.AllowedPrivateOrigins, o.LookupIP)
	if err != nil {
		return nil, fmt.Errorf("peer client destination policy: %w", err)
	}
	index, err := operations.NewIndex(operations.Table)
	if err != nil {
		return nil, fmt.Errorf("peer client operations: %w", err)
	}
	if o.SocketTimeout <= 0 {
		o.SocketTimeout = DefaultSocketTimeout
	}
	if o.AttemptTimeout <= 0 {
		o.AttemptTimeout = DefaultAttemptTimeout
	}
	if o.MaxRequestBody <= 0 {
		o.MaxRequestBody = DefaultMaxRequestBody
	}
	if o.MaxResponseBody <= 0 {
		o.MaxResponseBody = DefaultMaxResponseBody
	}
	if o.Attempts <= 0 {
		o.Attempts = DefaultAttempts
	}
	if o.Backoff <= 0 {
		o.Backoff = DefaultBackoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Factory{o: o, policy: policy, index: index}, nil
}

// Client calls one peer at the base URL its record holds. It signs every
// request and verifies every answer's signature and digest before it
// returns the answer, and it is the HttpRequestDoer of the generated
// client (API).
type Client struct {
	f         *Factory
	peer      model.Peer
	base      *url.URL
	http      *outbound.Client
	answerKey func([]byte) (string, ed25519.PublicKey, error)
}

// Client returns a client for p. Its answers are verified with p's recorded
// key unless WithAnswerKey supplies another way.
func (f *Factory) Client(ctx context.Context, p model.Peer) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(p.BaseURL, "/"))
	if err != nil || base.Host == "" || base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("peer %s base URL: %w", p.InstanceID, outbound.ErrConfiguration)
	}
	var (
		proxy *url.URL
		ntlm  *outbound.NTLMProxy
	)
	if f.o.Proxy != nil {
		if proxy, ntlm, err = f.o.Proxy(ctx); err != nil {
			return nil, fmt.Errorf("peer client proxy: %w", err)
		}
	}
	hc, err := outbound.NewClient(outbound.ClientOptions{
		Base: base, Proxy: proxy, NTLM: ntlm, Policy: f.policy, RootCAs: f.o.RootCAs,
		SocketTimeout: f.o.SocketTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("peer client: %w", err)
	}
	return &Client{f: f, peer: p, base: base, http: hc}, nil
}

// WithAnswerKey returns a copy of c that verifies answers with the key and
// keyid key reads from the answer body. Redeeming a pairing needs it: the
// issuer is not yet a peer, its answer presents its key, and the caller
// then checks that key against the token's fingerprint.
func (c *Client) WithAnswerKey(key func(body []byte) (keyID string, pub ed25519.PublicKey, err error)) *Client {
	out := *c
	out.answerKey = key
	return &out
}

// PairAnswerKey reads the instance_id and public_key a POST /pair answer
// presents, for WithAnswerKey.
func PairAnswerKey(body []byte) (string, ed25519.PublicKey, error) {
	var b struct {
		InstanceID string `json:"instance_id"`
		PublicKey  string `json:"public_key"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return "", nil, fmt.Errorf("%w: the pairing answer is not JSON", ErrNoPeerKey)
	}
	key, err := base64.StdEncoding.DecodeString(b.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || b.InstanceID == "" {
		return "", nil, fmt.Errorf("%w: the pairing answer presents no Ed25519 key", ErrNoPeerKey)
	}
	return b.InstanceID, key, nil
}

// API returns the generated peer client over c.
func (c *Client) API() (*peerclient.ClientWithResponses, error) {
	return peerclient.NewClientWithResponses( //nolint:wrapcheck // the generated client only parses the URL
		c.base.String()+Prefix, peerclient.WithHTTPClient(c))
}

// Close releases idle connections.
func (c *Client) Close() { c.http.Close() }

// Do sends req to the peer, signed, and returns the answer once its
// signature and digest verify; its body is then read whole and bounded.
// An operation whose decision 0043 class is safe or natural is retried,
// signed afresh, after a connection failure or a 429, 502, 503 or 504; any
// other operation is sent once. An answer that does not verify is an
// *UnverifiedAnswerError and is never retried.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	body, err := c.requestBody(req)
	if err != nil {
		return nil, err
	}
	signer, err := c.f.o.Signer(req.Context())
	if err != nil {
		return nil, fmt.Errorf("peer request signer: %w", err)
	}
	attempts := 1
	if op, ok := c.f.index.FindRequest(req); ok && (op.Idempotency == "safe" || op.Idempotency == "natural") {
		attempts = c.f.o.Attempts
	}
	wait := c.f.o.Backoff
	for attempt := 1; ; attempt++ {
		resp, retry, err := c.attempt(req, signer, body, attempt < attempts)
		if !retry {
			return resp, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err() //nolint:wrapcheck // the caller's own cancellation
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (c *Client) requestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	body, err := io.ReadAll(io.LimitReader(req.Body, c.f.o.MaxRequestBody+1))
	if err != nil {
		return nil, fmt.Errorf("read peer request body: %w", err)
	}
	if int64(len(body)) > c.f.o.MaxRequestBody {
		return nil, ErrTooLarge
	}
	return body, nil
}

// attempt sends one signed attempt. retry reports that the caller should
// try again: only when mayRetry and the failure is transient.
func (c *Client) attempt(
	req *http.Request,
	signer corepeer.Signer,
	body []byte,
	mayRetry bool,
) (_ *http.Response, retry bool, _ error) {
	ctx, cancel := context.WithTimeout(req.Context(), c.f.o.AttemptTimeout)
	defer cancel()
	r := req.Clone(ctx)
	r.Body, r.GetBody, r.ContentLength = http.NoBody, nil, int64(len(body))
	if len(body) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	// The URI the peer reconstructs is what goes on the wire: the origin
	// and the escaped request target, not url.URL.String's re-rendering.
	target := r.URL.Scheme + "://" + r.URL.Host + r.URL.RequestURI()
	if err := signer.SignRequest(r.Method, target, r.Header, body); err != nil {
		return nil, false, err //nolint:wrapcheck // SignRequest names the failure
	}
	resp, err := c.http.Do(r)
	if err != nil {
		transient := errors.Is(err, outbound.ErrConnection) ||
			errors.Is(err, context.DeadlineExceeded) && req.Context().Err() == nil
		return nil, mayRetry && transient, fmt.Errorf("peer %s: %w", c.peer.InstanceID, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if mayRetry {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			return nil, true, nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.f.o.MaxResponseBody+1))
	if err != nil {
		return nil, false, fmt.Errorf("read peer %s answer: %w", c.peer.InstanceID, err)
	}
	if int64(len(data)) > c.f.o.MaxResponseBody {
		return nil, false, ErrTooLarge
	}
	if err := c.verify(resp, r, target, data); err != nil {
		return nil, false, &UnverifiedAnswerError{Status: resp.StatusCode, Err: err}
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	return resp, false, nil
}

func (c *Client) verify(resp *http.Response, sent *http.Request, target string, body []byte) error {
	keyID, key := c.peer.InstanceID, c.peer.PublicKey
	if c.answerKey != nil {
		var err error
		if keyID, key, err = c.answerKey(body); err != nil {
			return err
		}
	}
	if len(key) == 0 {
		return ErrNoPeerKey
	}
	m := corepeer.Message{
		Status: resp.StatusCode, Header: resp.Header,
		Request: &corepeer.Message{Method: sent.Method, TargetURI: target, Header: sent.Header},
	}
	return corepeer.VerifyResponse(m, body, keyID, key, c.f.o.Now()) //nolint:wrapcheck // corepeer's refusals are the API
}
