package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

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

type noCrossOriginAuthKey struct{}

// NewClient builds the client of repository c under its resolved options,
// as a Pool does for each configuration and credential revision. It copies
// c's pointer-valued settings, so a later change to c never reaches the
// client. An invalid repository URL is refused with remote.ErrURL; a
// negative timeout, a local address that is no IP address or a proxy that
// is neither HTTP nor HTTPS with remote.ErrConfiguration.
func NewClient(c repo.Config, opts remote.Options) (*Client, error) {
	// Snapshot pointer-valued settings: a caller applying a new configuration
	// must not mutate an active client's path or timeout policy.
	c.DisableURLNormalization = copySetting(c.DisableURLNormalization)
	c.PropagateQueryParams = copySetting(c.PropagateQueryParams)
	c.BypassHeadRequests = copySetting(c.BypassHeadRequests)
	c.MetadataRetrievalTimeoutSecs = copySetting(c.MetadataRetrievalTimeoutSecs)
	base, err := remote.BaseURL(c.URL)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	idle := 15 * time.Second
	if c.SocketTimeoutMillis != nil {
		idle = time.Duration(*c.SocketTimeoutMillis) * time.Millisecond
	}
	if idle < 0 || (c.MetadataRetrievalTimeoutSecs != nil && *c.MetadataRetrievalTimeoutSecs < 0) {
		return nil, remote.ErrConfiguration
	}
	dialer := &net.Dialer{Timeout: idle, KeepAlive: 30 * time.Second}
	if c.LocalAddress != "" {
		ip := net.ParseIP(c.LocalAddress)
		if ip == nil {
			return nil, fmt.Errorf(
				"%w: local address must be an ip address",
				remote.ErrConfiguration,
			)
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
			return nil, remote.ErrConfiguration
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

func redirectPolicy(opts remote.Options) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.User != nil ||
			(req.URL.Scheme != "http" && req.URL.Scheme != "https") ||
			(via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
			return remote.ErrRedirect
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
				//nolint:wrapcheck // preserves upstream error identity
				return err
			}
		}
		if opts.Prepare != nil {
			return opts.Prepare(req)
		}
		return nil
	}
}

// Do sends r, a GET or HEAD relative to the repository URL, or to
// r.ExternalURL when the client has a CheckURL, and returns the response
// whatever its status; the caller owns and closes its body. The only POSTs
// it admits are a closed set of protocol exchanges (the VS Code gallery
// query, the Git LFS batch call and a CocoaPods Git upload-pack), each with
// its exact path, size and Accept header. It applies the request's and the
// options' checks, the credential preparation, the metadata deadline to a
// metadata request, and the WithOutbound hooks. A refused request answers
// remote.ErrRequest or remote.ErrSecurity; a transport failure comes back
// classified, without the URL or headers it carried.
//
//nolint:gocyclo // Preserves transport guards while admitting closed protocol-specific POSTs.
func (c *Client) Do(ctx context.Context, r remote.Request) (*http.Response, error) {
	gitPOST := r.GitUploadPack && r.Method == http.MethodPost &&
		c.config.PackageType == repo.CocoaPods && r.Metadata && r.NoCrossOriginAuth &&
		r.ExternalURL != "" && r.Path == "" && r.Query == "" &&
		len(r.Body) > 0 && len(r.Body) <= 4096 &&
		r.Header.Get("Accept") == "application/x-git-upload-pack-result" && r.CheckURL != nil
	galleryPOST := r.Method == http.MethodPost &&
		c.config.PackageType == repo.AIEditorExtensions && r.Metadata &&
		r.Path == "extensionquery" && r.Query == "" &&
		r.ExternalURL == "" && len(r.Body) > 0 && len(r.Body) <= 64<<10 &&
		r.Header.Get("Accept") == "application/json;api-version=3.0-preview.1"
	lfsPOST := r.Method == http.MethodPost &&
		c.config.PackageType == repo.GitLFS && r.Metadata &&
		r.Path == "objects/batch" && r.Query == "" && r.ExternalURL == "" &&
		len(r.Body) > 0 && len(r.Body) <= 4096 &&
		r.Header.Get("Accept") == "application/vnd.git-lfs+json"
	lfsLocksGET := r.Method == http.MethodGet &&
		c.config.PackageType == repo.GitLFS && r.Metadata && r.Path == "locks" &&
		r.ExternalURL == "" && len(r.Body) == 0 &&
		r.Header.Get("Accept") == "application/vnd.git-lfs+json"
	if !galleryPOST && !lfsPOST && !gitPOST &&
		(r.Method != http.MethodGet && r.Method != http.MethodHead || len(r.Body) != 0) {
		return nil, remote.ErrRequest
	}
	if galleryPOST || lfsPOST || gitPOST {
		for name := range r.Header {
			if name != "Accept" {
				return nil, remote.ErrRequest
			}
		}
	}
	var u *url.URL
	var err error
	if r.ExternalURL != "" {
		if c.checkURL == nil || r.Path != "" || r.Query != "" {
			return nil, remote.ErrSecurity
		}
		u, err = remote.CheckedURL(r.ExternalURL)
	} else {
		u, err = requestURL(c.config, c.base, r.Path, r.Query)
	}
	if err != nil {
		return nil, err
	}
	if gitPOST &&
		(u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/git-upload-pack")) {
		return nil, remote.ErrRequest
	}
	if galleryPOST && (u.RawQuery != "" || u.Fragment != "" ||
		u.EscapedPath() != strings.TrimRight(c.base.EscapedPath(), "/")+"/extensionquery") ||
		lfsPOST && (u.RawQuery != "" || u.Fragment != "" ||
			u.EscapedPath() != strings.TrimRight(c.base.EscapedPath(), "/")+"/objects/batch") {
		return nil, remote.ErrRequest
	}
	if r.CheckURL != nil {
		if err = r.CheckURL(ctx, u); err != nil {
			return nil, safeError(err)
		}
	}
	if !c.validGitLFSActionHeaders(u, r) {
		return nil, remote.ErrRequest
	}
	if r.NoCrossOriginAuth {
		ctx = context.WithValue(ctx, noCrossOriginAuthKey{}, true)
	}
	deadline := 60 * time.Second
	if c.config.MetadataRetrievalTimeoutSecs != nil {
		deadline = time.Duration(*c.config.MetadataRetrievalTimeoutSecs) * time.Second
	}
	cancel := func() {}
	if r.Metadata && deadline > 0 {
		ctx, cancel = context.WithTimeout(ctx, deadline)
	}
	var body io.Reader
	if galleryPOST || lfsPOST || gitPOST {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), body)
	if err != nil {
		cancel()
		return nil, remote.ErrRequest
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
	if galleryPOST {
		// The credential layer removes caller headers before adding configured
		// credentials. Restore only the fixed protocol headers afterward.
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json;api-version=3.0-preview.1")
	}
	if lfsPOST {
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
	}
	if lfsLocksGET {
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
	}
	if gitPOST {
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		req.Header.Set("Accept", "application/x-git-upload-pack-result")
	}
	for name, values := range r.GitLFSActionHeaders {
		req.Header[name] = append([]string(nil), values...)
	}
	out := remote.OutboundFrom(ctx)
	if out.Header != nil {
		out.Header(req)
	}
	if out.Allow != nil && !out.Allow(req.URL.String()) {
		cancel()
		return nil, remote.ErrSecurity
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
func (c *Client) Fetch(ctx context.Context, r remote.Request) (*http.Response, error) {
	if r.Method == http.MethodPost {
		return c.Do(ctx, r)
	}
	// A Git LFS lock list is volatile control state. Fetch it once, exactly as
	// the locking API defines, rather than probing an unspecified HEAD route.
	if r.Method == http.MethodGet && c.config.PackageType == repo.GitLFS &&
		r.Metadata && r.Path == "locks" && r.ExternalURL == "" && len(r.Body) == 0 &&
		r.Header.Get("Accept") == "application/vnd.git-lfs+json" {
		return c.Do(ctx, r)
	}
	// An LFS action can be a one-shot signed download. Do not spend or expose
	// its action headers on a separate HEAD preflight.
	if len(r.GitLFSActionHeaders) > 0 {
		if r.Method != http.MethodGet {
			return nil, remote.ErrRequest
		}
		return c.Do(ctx, r)
	}
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

func (c *Client) fetch(ctx context.Context, r remote.Request) (*http.Response, error) {
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

// Close releases the client's idle connections; requests in flight finish
// on their own connections.
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
		return fmt.Errorf("%w: %w", remote.ErrRequest, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", remote.ErrRequest, context.DeadlineExceeded)
	}
	if errors.Is(err, remote.ErrRedirect) {
		return fmt.Errorf("%w: %w", remote.ErrRequest, remote.ErrRedirect)
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) || errors.Is(err, remote.ErrSecurity) {
		return fmt.Errorf("%w: %w", remote.ErrRequest, remote.ErrSecurity)
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return fmt.Errorf("%w: %w", remote.ErrRequest, context.DeadlineExceeded)
	}
	var connection *net.OpError
	if errors.As(err, &connection) || errors.Is(err, remote.ErrConnection) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %w", remote.ErrRequest, remote.ErrConnection)
	}
	return remote.ErrRequest
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
// authorization callback or outbound hooks. Every redirect intersects both
// guards with the existing transport destination and credential policy.
func (c *Client) requestClient(ctx context.Context, r remote.Request) *http.Client {
	client := *c.http
	if r.NoCrossOriginAuth {
		client.Jar = nil
	}
	out := remote.OutboundFrom(ctx)
	redirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if r.Method == http.MethodPost {
			return remote.ErrRedirect
		}
		for name := range r.GitLFSActionHeaders {
			next.Header.Del(name)
		}
		if r.CheckURL != nil {
			if err := r.CheckURL(ctx, next.URL); err != nil {
				//nolint:wrapcheck // preserves upstream error identity
				return err
			}
		}
		if out.Allow != nil && !out.Allow(next.URL.String()) {
			return remote.ErrSecurity
		}
		if err := redirect(next, via); err != nil {
			return err
		}
		if out.Header != nil && remote.Origin(next.URL) == remote.Origin(via[0].URL) {
			out.Header(next)
		}
		return nil
	}
	return &client
}

// Guarded reports whether c was built with remote.Options.CheckURL, the
// check that every absolute destination taken from metadata must pass.
func (c *Client) Guarded() bool { return c.checkURL != nil }

// CheckURL applies remote.Options.CheckURL to u, and returns
// remote.ErrSecurity when c has none, so that an unguarded client never
// vouches for a destination.
func (c *Client) CheckURL(ctx context.Context, u *url.URL) error {
	if c.checkURL == nil {
		return remote.ErrSecurity
	}
	return c.checkURL(ctx, u)
}

// Prepare applies remote.Options.Prepare to req, a request its caller built
// rather than Do; without one it does nothing and returns nil.
func (c *Client) Prepare(req *http.Request) error {
	if c.prepare == nil {
		return nil
	}
	return c.prepare(req)
}

// Send sends req over c's transport, proxy, TLS settings and cookie jar
// included, and refuses every redirect with remote.ErrRedirect, because a
// request built outside Do has none of the redirect checks Do installs. It
// applies neither Do's checks nor the WithOutbound hooks; its caller checks
// and prepares req first. The error is the HTTP client's own, unwrapped and
// not classified, and a successful call transfers response-body ownership.
func (c *Client) Send(req *http.Request) (*http.Response, error) {
	transport := *c.http
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return remote.ErrRedirect }
	// gosec's taint analysis cannot see that the caller checks req's target
	// with CheckURL before Send, as the contract above asks.
	//nolint:wrapcheck,gosec // preserves upstream error identity; target checked by the caller
	return transport.Do(req)
}

func (c *Client) validGitLFSActionHeaders(u *url.URL, r remote.Request) bool {
	if len(r.GitLFSActionHeaders) == 0 {
		return true
	}
	if c.config.PackageType != repo.GitLFS || r.Method != http.MethodGet ||
		r.ExternalURL == "" || !r.NoCrossOriginAuth || remote.Origin(u) != remote.Origin(c.base) ||
		!source.ValidGitLFSActionHeaders(r.GitLFSActionHeaders) {
		return false
	}
	return true
}
