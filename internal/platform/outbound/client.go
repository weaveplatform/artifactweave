package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// ClientOptions is immutable trusted connection policy, never request metadata.
// Zero SocketTimeout permits no inactivity deadline; the owner supplies its
// configured default. Explicit proxy credentials are used only for CONNECT.
// NTLM, which requires Proxy, replaces the proxy URL's Basic userinfo: every
// new tunnel negotiates NTLMv2 on its own socket and never falls back to Basic.
type ClientOptions struct {
	Base          *url.URL
	Proxy         *url.URL
	NTLM          *NTLMProxy
	Policy        *Policy
	RootCAs       *x509.CertPool
	SocketTimeout time.Duration
}

// Client sends only to its configured origin and owns its idle connections.
// Active exchanges remain owned by their request contexts during Close.
type Client struct {
	base      url.URL
	proxy     *url.URL
	ntlm      *NTLMProxy
	policy    *Policy
	roots     *x509.CertPool
	timeout   time.Duration
	dialer    net.Dialer
	http      *http.Client
	transport *http.Transport
	closed    atomic.Bool
}

// NewClient validates and snapshots options without DNS or network I/O.
func NewClient(options ClientOptions) (*Client, error) {
	if checkAuthority(options.Base) != nil || options.Policy == nil || options.SocketTimeout < 0 ||
		options.NTLM != nil && options.Proxy == nil {
		return nil, ErrConfiguration
	}
	c := &Client{
		base: *options.Base, ntlm: options.NTLM, policy: options.Policy, timeout: options.SocketTimeout,
		dialer: net.Dialer{Timeout: options.SocketTimeout, KeepAlive: 30 * time.Second},
	}
	if options.RootCAs != nil {
		c.roots = options.RootCAs.Clone()
	}
	if options.Proxy != nil {
		proxy := *options.Proxy
		proxy.User = nil
		if checkAuthority(&proxy) != nil || proxy.RawQuery != "" || proxy.Path != "" && proxy.Path != "/" {
			return nil, ErrConfiguration
		}
		if options.NTLM == nil {
			proxy.User = options.Proxy.User
		}
		c.proxy = &proxy
	}
	c.transport = &http.Transport{
		DialContext: c.dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.roots},
		MaxIdleConns: 32, MaxIdleConnsPerHost: 10, MaxConnsPerHost: 32, IdleConnTimeout: IdleConnTimeout(c.timeout),
		TLSHandshakeTimeout: c.timeout, ResponseHeaderTimeout: c.timeout, ExpectContinueTimeout: time.Second,
		DisableCompression: true, MaxResponseHeaderBytes: 1 << 20,
		// Per-socket inactivity deadlines cannot safely multiplex HTTP/2 streams.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	c.http = &http.Client{Transport: checkedTransport{c}, CheckRedirect: c.redirect}
	return c, nil
}

// Do returns a caller-owned response body on success, regardless of status.
// It preserves only safe error classifications, never a url.Error's target.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, ErrRequest
	}
	if req.URL == nil {
		return nil, c.refuse(req, ErrRequest)
	}
	if c.closed.Load() {
		return nil, c.refuse(req, ErrClosed)
	}
	if checkAuthority(req.URL) != nil || Origin(req.URL) != Origin(&c.base) {
		return nil, c.refuse(req, ErrSecurity)
	}
	request := req.Clone(req.Context())
	request.Host = request.URL.Host
	request.Header.Del("Proxy-Authorization")
	mutating := request.Method != http.MethodGet && request.Method != http.MethodHead
	if mutating {
		// net/http treats an idempotency header plus GetBody as permission to
		// replay a write. Recovery belongs to the durable business driver.
		request.GetBody = nil
		request.Header.Del("Idempotency-Key")
		request.Header.Del("X-Idempotency-Key")
	}
	response, err := c.http.Do(request) //nolint:gosec // G704: fixed origin above; checkedTransport pins destination and proxy IPs.
	if err != nil {
		// net/http closes the response on redirect errors; every other error
		// returns no response. Returning that closed object would mislead owners.
		return nil, SafeError(err)
	}
	if mutating && redirectStatus(response.StatusCode) {
		if err := response.Body.Close(); err != nil {
			return nil, errors.Join(ErrRedirect, SafeError(err))
		}
		return nil, ErrRedirect
	}
	response.Body = safeBody{response.Body}
	return response, nil
}

func redirectStatus(status int) bool {
	return status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect
}

type safeBody struct{ io.ReadCloser }

func (b safeBody) Read(body []byte) (int, error) {
	n, err := b.ReadCloser.Read(body)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, SafeError(err)
}

func (b safeBody) Close() error {
	if err := b.ReadCloser.Close(); err != nil {
		return SafeError(err)
	}
	return nil
}

func (c *Client) refuse(req *http.Request, refusal error) error {
	if req.Body != nil {
		if err := req.Body.Close(); err != nil {
			return errors.Join(refusal, SafeError(err))
		}
	}
	return refusal
}

func (c *Client) redirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || len(via) >= 10 || checkAuthority(req.URL) != nil || Origin(req.URL) != Origin(&c.base) {
		return ErrRedirect
	}
	if method := via[0].Method; method != http.MethodGet && method != http.MethodHead {
		return ErrRedirect
	}
	return nil
}

// Close prevents new requests and releases idle connections. The application
// cancels the contexts of active operations before waiting for their workers.
func (c *Client) Close() {
	c.closed.Store(true)
	c.transport.CloseIdleConnections()
}

type dialPlan struct {
	targets []string
	proxies []string
}

type (
	planKey          struct{}
	checkedTransport struct{ client *Client }
)

func (t checkedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := t.client
	if c.closed.Load() {
		return nil, c.refuse(req, ErrClosed)
	}
	lookupCtx := req.Context()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		lookupCtx, cancel = context.WithTimeout(lookupCtx, c.timeout)
		defer cancel()
	}
	targets, err := c.policy.Addresses(lookupCtx, req.URL)
	if err != nil {
		return nil, c.refuse(req, SafeError(err))
	}
	plan := dialPlan{targets: targets}
	if c.proxy != nil {
		proxy := *c.proxy
		proxy.User = nil
		plan.proxies, err = c.policy.Addresses(lookupCtx, &proxy)
		if err != nil {
			return nil, c.refuse(req, SafeError(err))
		}
	}
	request := req.Clone(context.WithValue(req.Context(), planKey{}, plan))
	response, err := c.transport.RoundTrip(request)
	if err != nil {
		return nil, SafeError(err)
	}
	return response, nil
}

func (c *Client) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	plan, ok := ctx.Value(planKey{}).(dialPlan)
	if !ok || len(plan.targets) == 0 {
		return nil, ErrSecurity
	}
	last := ErrConnection
	for _, target := range plan.targets {
		if err := ctx.Err(); err != nil {
			return nil, SafeError(err)
		}
		var conn net.Conn
		var err error
		if c.proxy != nil {
			conn, err = c.tunnel(ctx, network, target, plan.proxies)
		} else {
			conn, err = c.direct(ctx, network, target)
		}
		if err == nil {
			return conn, nil
		}
		last = SafeError(err)
		if !errors.Is(last, ErrConnection) {
			return nil, last
		}
	}
	return nil, last
}

func (c *Client) direct(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := c.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, SafeError(err)
	}
	return SocketTimeoutConn(conn, c.timeout), nil
}

// SafeError discards network endpoint text while preserving actionable causes.
func SafeError(err error) error {
	var request *url.Error
	if errors.As(err, &request) {
		return SafeError(request.Err)
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrSecurity, ErrRedirect, ErrClosed, ErrConfiguration, ErrConnection} {
		if errors.Is(err, known) {
			return known
		}
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return ErrSecurity
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return context.DeadlineExceeded
	}
	var connection *net.OpError
	if errors.As(err, &connection) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrConnection
	}
	return ErrRequest
}
