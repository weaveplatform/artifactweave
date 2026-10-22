package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Options contains resolved connection settings. Revision must change whenever
// externally resolved settings (proxy, trust, credentials) change. Prepare runs
// for initial requests and redirects, after the transport removes credentials
// from requests crossing origins. It must not forward downstream credentials.
type Options struct {
	ProxyURL  *url.URL
	TLSConfig *tls.Config
	Revision  string
	Jar       http.CookieJar
	// CheckURL authorizes metadata-provided absolute destinations.
	CheckURL      func(context.Context, *url.URL) error
	WrapTransport func(*http.Transport) (http.RoundTripper, func())
	Prepare       func(*http.Request) error
	CheckRedirect func(*http.Request, []*http.Request) error
	// MaxResponseHeaderBytes defaults to 1 MiB; tests can lower it.
	MaxResponseHeaderBytes int64
}

type Request struct {
	// ExternalURL is a metadata/token-realm URL, never combined with Path/Query.
	// It requires a resolved destination policy.
	ExternalURL string
	// CheckURL is a request-scoped restriction, never retained by the pooled client.
	// It applies before the initial request and each redirected request is sent.
	CheckURL func(context.Context, *url.URL) error
	Method   string
	Path     string
	Query    string
	Header   http.Header
	Metadata bool
}

// Client is immutable and safe for concurrent use. Every successful Do or Fetch
// transfers response-body ownership to the caller, including non-2xx responses.
type Client struct {
	config    repo.Config
	base      *url.URL
	http      *http.Client
	transport *http.Transport
	prepare   func(*http.Request) error
	checkURL  func(context.Context, *url.URL) error
	close     func()
}

func NewClient(c repo.Config, opts Options) (*Client, error) {
	// Snapshot pointer-valued settings: a caller applying a new configuration
	// must not mutate an active client's path or timeout policy.
	c.DisableURLNormalization = copySetting(c.DisableURLNormalization)
	c.PropagateQueryParams = copySetting(c.PropagateQueryParams)
	c.BypassHeadRequests = copySetting(c.BypassHeadRequests)
	c.MetadataRetrievalTimeoutSecs = copySetting(c.MetadataRetrievalTimeoutSecs)
	base, err := baseURL(c.URL)
	if err != nil {
		return nil, err
	}
	idle := 15 * time.Second
	if c.SocketTimeoutMillis != nil {
		idle = time.Duration(*c.SocketTimeoutMillis) * time.Millisecond
	}
	if idle < 0 || (c.MetadataRetrievalTimeoutSecs != nil && *c.MetadataRetrievalTimeoutSecs < 0) {
		return nil, ErrConfiguration
	}
	dialer := &net.Dialer{Timeout: idle, KeepAlive: 30 * time.Second}
	if c.LocalAddress != "" {
		ip := net.ParseIP(c.LocalAddress)
		if ip == nil {
			return nil, fmt.Errorf("%w: local address must be an IP address", ErrConfiguration)
		}
		dialer.LocalAddr = &net.TCPAddr{IP: ip}
	}
	tr := &http.Transport{
		MaxIdleConns: 100, MaxIdleConnsPerHost: 10, MaxConnsPerHost: 32,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: idle,
		ResponseHeaderTimeout: idle, ExpectContinueTimeout: time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 << 20,
		// Socket inactivity deadlines are per connection; HTTP/1 avoids one
		// multiplexed stream resetting another stream's deadline.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if opts.MaxResponseHeaderBytes > 0 {
		tr.MaxResponseHeaderBytes = opts.MaxResponseHeaderBytes
	}
	if opts.TLSConfig != nil {
		tr.TLSClientConfig = opts.TLSConfig.Clone()
	}
	if opts.ProxyURL != nil && !enabled(c.DisableProxy) {
		p := *opts.ProxyURL
		if p.Scheme != "http" && p.Scheme != "https" {
			return nil, ErrConfiguration
		}
		tr.Proxy = http.ProxyURL(&p)
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, dialErr := dialer.DialContext(ctx, network, addr)
		if dialErr != nil {
			return nil, fmt.Errorf("dial remote: %w", dialErr)
		}
		return &idleConn{Conn: conn, timeout: idle}, nil
	}
	client := &Client{
		config:    c,
		base:      base,
		transport: tr,
		prepare:   opts.Prepare,
		checkURL:  opts.CheckURL,
		close:     tr.CloseIdleConnections,
	}
	var roundTripper http.RoundTripper = tr
	if opts.WrapTransport != nil {
		roundTripper, client.close = opts.WrapTransport(tr)
	}
	client.http = &http.Client{
		Transport:     roundTripper,
		CheckRedirect: redirectPolicy(opts),
		Jar:           opts.Jar,
	}
	return client, nil
}

func copySetting[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func redirectPolicy(opts Options) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.User != nil ||
			(req.URL.Scheme != "http" && req.URL.Scheme != "https") ||
			(via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
			return ErrRedirect
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			// Custom headers can contain secrets too. Only transport headers
			// survive a change of origin unless the credential layer opts in.
			for key := range req.Header {
				switch key {
				case "Accept",
					"Accept-Encoding",
					"Range",
					"If-Range",
					"If-None-Match",
					"If-Modified-Since":
				default:
					req.Header.Del(key)
				}
			}
		}
		if opts.CheckRedirect != nil {
			if err := opts.CheckRedirect(req, via); err != nil {
				return err
			}
		}
		if opts.Prepare != nil {
			return opts.Prepare(req)
		}
		return nil
	}
}

func (c *Client) Do(ctx context.Context, r Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, ErrRequest
	}
	var u *url.URL
	var err error
	if r.ExternalURL != "" {
		if c.checkURL == nil || r.Path != "" || r.Query != "" {
			return nil, ErrSecurity
		}
		u, err = checkedURL(r.ExternalURL)
	} else {
		u, err = requestURL(c.config, c.base, r.Path, r.Query)
	}
	if err != nil {
		return nil, err
	}
	if r.CheckURL != nil {
		if err = r.CheckURL(ctx, u); err != nil {
			return nil, safeError(err)
		}
	}
	deadline := 60 * time.Second
	if c.config.MetadataRetrievalTimeoutSecs != nil {
		deadline = time.Duration(*c.config.MetadataRetrievalTimeoutSecs) * time.Second
	}
	cancel := func() {}
	if r.Metadata && deadline > 0 {
		ctx, cancel = context.WithTimeout(ctx, deadline)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), nil)
	if err != nil {
		cancel()
		return nil, ErrRequest
	}
	req.Header = r.Header.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if r.ExternalURL != "" {
		if err = c.checkURL(ctx, u); err != nil {
			cancel()
			return nil, safeError(err)
		}
	}
	if c.prepare != nil {
		if err = c.prepare(req); err != nil {
			cancel()
			return nil, safeError(err)
		}
	}
	smartHeaders(req)
	if session, ok := ctx.Value(smartSessionKey{}).(smartSession); ok && session.metadata &&
		!smartMetadataTarget(session.peer, req.URL.String()) {
		cancel()
		return nil, ErrSecurity
	}
	client := c.requestClient(ctx, r)

	res, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, safeError(err)
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

// Fetch probes with HEAD unless bypassed. An unsupported HEAD falls back to GET;
// a definitive error or conditional 304 is returned directly. GET is authoritative
// when the resource changes between requests: no HEAD length/checksum is reused.
func (c *Client) Fetch(ctx context.Context, r Request) (*http.Response, error) {
	// The metadata budget covers preflight and retrieval together. Do's own
	// deadline can only narrow this parent context, never reset its budget.
	cancel := func() {}
	deadline := 60 * time.Second
	if c.config.MetadataRetrievalTimeoutSecs != nil {
		deadline = time.Duration(*c.config.MetadataRetrievalTimeoutSecs) * time.Second
	}
	if r.Metadata && deadline > 0 {
		ctx, cancel = context.WithTimeout(ctx, deadline)
	}
	res, err := c.fetch(ctx, r)
	if err != nil {
		cancel()
		return nil, err
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

func (c *Client) fetch(ctx context.Context, r Request) (*http.Response, error) {
	if r.Method == http.MethodHead {
		return c.Do(ctx, r)
	}
	if !enabled(c.config.BypassHeadRequests) {
		r.Method = http.MethodHead
		res, err := c.Do(ctx, r)
		if err != nil {
			return nil, err
		}
		if res.StatusCode == http.StatusNotModified ||
			(res.StatusCode >= 400 && res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusNotImplemented) {
			return res, nil
		}
		if err = res.Body.Close(); err != nil {
			return nil, safeError(err)
		}
	}
	r.Method = http.MethodGet
	return c.Do(ctx, r)
}

func (c *Client) Close() { c.close() }

// http.Client errors embed URLs, including signed query strings. Preserve only
// actionable classifications; never place an upstream's URL or headers in logs.
func safeError(err error) error {
	// url.Error implements Timeout itself but may wrap another non-net.Error
	// wrapper. Inspect its cause so a socket timeout is not mistaken for a reset.
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return safeError(requestError.Err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrRequest, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrRequest, context.DeadlineExceeded)
	}
	if errors.Is(err, ErrRedirect) {
		return fmt.Errorf("%w: %w", ErrRequest, ErrRedirect)
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) || errors.Is(err, ErrSecurity) {
		return fmt.Errorf("%w: %w", ErrRequest, ErrSecurity)
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return fmt.Errorf("%w: %w", ErrRequest, context.DeadlineExceeded)
	}
	var connection *net.OpError
	if errors.As(err, &connection) || errors.Is(err, ErrConnection) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %w", ErrRequest, ErrConnection)
	}
	return ErrRequest
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	b.cancel()
	if err := b.ReadCloser.Close(); err != nil {
		return safeError(err)
	}
	return nil
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, fmt.Errorf("set read deadline: %w", err)
		}
	}
	n, err := c.Conn.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("read remote: %w", err)
	}
	return n, nil
}

func (c *idleConn) Write(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, fmt.Errorf("set write deadline: %w", err)
		}
	}
	n, err := c.Conn.Write(p)
	if err != nil {
		return n, fmt.Errorf("write remote: %w", err)
	}
	return n, nil
}

// Request restrictions share pooled connections but never another request's
// authorization callback or Smart peer. Every redirect intersects both guards
// with the existing transport destination and credential policy.
func (c *Client) requestClient(ctx context.Context, r Request) *http.Client {
	client := *c.http
	redirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if r.CheckURL != nil {
			if err := r.CheckURL(ctx, next.URL); err != nil {
				return err
			}
		}
		if session, ok := ctx.Value(smartSessionKey{}).(smartSession); ok && session.metadata &&
			!smartMetadataTarget(session.peer, next.URL.String()) {
			return ErrSecurity
		}
		if err := redirect(next, via); err != nil {
			return err
		}
		if origin(next.URL) == origin(via[0].URL) {
			smartHeaders(next)
		}
		return nil
	}
	return &client
}
