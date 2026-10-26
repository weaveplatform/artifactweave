package remote

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

type sourceSecurity struct {
	base        *url.URL
	config      repo.Config
	policy      *destinationPolicy
	proxy       *url.URL
	roots       *x509.CertPool
	certificate tls.Certificate
}

func (s *sourceSecurity) checkURL(ctx context.Context, u *url.URL) error {
	_, err := s.policy.addresses(ctx, u)
	return err
}

func transportHeaders(h http.Header) {
	for key := range h {
		switch http.CanonicalHeaderKey(key) {
		case "Accept", "Accept-Encoding", "Range", "If-Range", "If-None-Match", "If-Modified-Since":
		default:
			delete(h, key)
		}
	}
}

func (s *sourceSecurity) authorized(u *url.URL) bool {
	return origin(u) == origin(s.base) ||
		(enabled(s.config.AllowAnyHostAuth) && u.Scheme == "https")
}

func (s *sourceSecurity) prepare(req *http.Request) error {
	// Caller and previous-hop headers are untrusted. The jar adds only cookies
	// learned by this source instance after this hook, using domain/path/secure rules.
	for _, h := range s.config.CustomHTTPHeaders {
		req.Header.Del(h.Name)
	}
	transportHeaders(req.Header)
	if _, err := checkedURL(req.URL.String()); err != nil {
		return err
	}
	if s.base.Scheme == "https" && req.URL.Scheme != "https" {
		return ErrSecurity
	}
	if req.Context().Value(noCrossOriginAuthKey{}) == true && origin(req.URL) != origin(s.base) {
		return nil
	}
	if s.authorized(req.URL) {
		for _, h := range s.config.CustomHTTPHeaders {
			req.Header.Set(h.Name, h.Value)
		}
		if enabled(s.config.EnableTokenAuthentication) {
			if s.config.Password != "" {
				req.Header.Set("Authorization", "Bearer "+s.config.Password)
			}
		} else if s.config.Username != "" || s.config.Password != "" {
			req.SetBasicAuth(s.config.Username, s.config.Password)
		}
	}
	return nil
}

type secureTransport struct {
	source  *sourceSecurity
	base    *http.Transport
	mu      sync.Mutex
	clients map[string]*http.Transport
}

func (s *sourceSecurity) transport(base *http.Transport) (http.RoundTripper, func()) {
	t := &secureTransport{source: s, base: base, clients: make(map[string]*http.Transport)}
	return t, t.close
}

func (t *secureTransport) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.clients {
		tr.CloseIdleConnections()
	}
	t.clients = make(map[string]*http.Transport)
	t.base.CloseIdleConnections()
}

func (t *secureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	addresses, err := t.source.policy.addresses(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	var proxy *url.URL
	if t.source.proxy != nil {
		p := *t.source.proxy
		p.User = nil
		proxies, proxyErr := t.source.policy.addresses(req.Context(), &p)
		if proxyErr != nil {
			return nil, proxyErr
		}
		p.Host = proxies[0]
		p.User = t.source.proxy.User
		proxy = &p
	}
	// Use checked IPs as the physical URL authority, including HTTP proxy and
	// CONNECT requests. Otherwise the proxy could resolve an approved hostname a
	// second time to a forbidden address. Host and TLS SNI remain the logical host.
	for _, addr := range addresses {
		physical := req.Clone(req.Context())
		u := *req.URL
		u.Host = addr
		physical.URL = &u
		physical.Host = req.URL.Host
		noAuth := req.Context().Value(noCrossOriginAuthKey{}) == true &&
			origin(req.URL) != origin(t.source.base)
		tr := t.forAuthority(req.URL, proxy, noAuth)
		res, roundErr := tr.RoundTrip(physical)
		if roundErr == nil {
			res.Request = req
			return res, nil
		}
		err = safeError(roundErr)
		// GET/HEAD have no request bodies. Only connection failures can try another
		// approved DNS answer; security errors and responses are never retried.
		if !errors.Is(err, ErrConnection) {
			break
		}
	}
	return nil, err
}

func (t *secureTransport) forAuthority(logical, proxy *url.URL, noAuth bool) *http.Transport {
	key := origin(logical)
	if noAuth {
		key += "|no-auth"
	}
	if proxy != nil {
		key += "|" + proxy.Host
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.clients[key]; tr != nil {
		return tr
	}
	tr := t.base.Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    t.source.roots,
		ServerName: logical.Hostname(),
	}
	if !noAuth && t.source.authorized(logical) && len(t.source.certificate.Certificate) > 0 {
		tr.TLSClientConfig.Certificates = []tls.Certificate{t.source.certificate}
	}
	tr.Proxy = nil
	if proxy != nil {
		if logical.Scheme == "http" && net.ParseIP(logical.Hostname()) == nil {
			// A forward proxy must replace Host with the absolute URI's authority.
			// CONNECT preserves virtual-host routing while pinning the checked IP.
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return t.tunnel(ctx, network, addr, proxy)
			}
		} else {
			tr.Proxy = http.ProxyURL(proxy)
			if proxy.Scheme == "https" {
				tr.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
					return t.dialProxy(ctx, network, proxy)
				}
			}
		}
	}
	// Metadata can name many public authorities. Bound retained connection pools;
	// an evicted pool's in-flight requests keep their own transport alive.
	if len(t.clients) >= 32 {
		for old, client := range t.clients {
			client.CloseIdleConnections()
			delete(t.clients, old)
			break
		}
	}
	t.clients[key] = tr
	return tr
}

// The proxy and destination use separate TLS identities. Repository client
// certificates never authenticate to the proxy.
func (t *secureTransport) dialProxy(
	ctx context.Context,
	network string,
	proxy *url.URL,
) (net.Conn, error) {
	conn, err := t.base.DialContext(ctx, network, proxy.Host)
	if err != nil {
		return nil, safeError(err)
	}
	if proxy.Scheme != "https" {
		return conn, nil
	}
	secure := tls.Client(
		conn,
		&tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    t.source.roots,
			ServerName: t.source.proxy.Hostname(),
		},
	)
	if err = secure.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, safeError(err)
	}
	return secure, nil
}

func (t *secureTransport) tunnel(
	ctx context.Context,
	network, addr string,
	proxy *url.URL,
) (net.Conn, error) {
	conn, err := t.dialProxy(ctx, network, proxy)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+addr, nil)
	if err != nil {
		return nil, ErrSecurity
	}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		req.SetBasicAuth(proxy.User.Username(), password)
		req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
		req.Header.Del("Authorization")
	}
	if err = req.Write(conn); err != nil {
		return nil, safeError(err)
	}
	// Bound the CONNECT response independently of artifact bytes. The reader's
	// remaining allowance is followed by the raw connection after headers.
	reader := bufio.NewReader(io.LimitReader(conn, t.base.MaxResponseHeaderBytes))
	// The CONNECT body is the caller-owned tunnel, not a response to drain.
	//nolint:bodyclose // the caller owns the tunnel connection, including its body
	res, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, safeError(err)
	}
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusProxyAuthRequired ||
			res.StatusCode == http.StatusForbidden {
			return nil, ErrSecurity
		}
		return nil, ErrConnection
	}
	success = true
	return &bufferedConn{Conn: conn, reader: io.MultiReader(reader, conn)}, nil
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, safeError(err)
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, nil
}
