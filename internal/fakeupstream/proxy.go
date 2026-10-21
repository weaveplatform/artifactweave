package fakeupstream

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
)

// NewProxy starts an HTTP forward proxy with optional proxy authentication. Only
// the supplied running fixtures are reachable: an absolute URL or CONNECT cannot
// accidentally turn a fault test into a public-registry fetch. HTTPS uses CONNECT;
// the client remains responsible for trusting the destination's certificate.
func NewProxy(t testing.TB, auth Auth, targets ...*Server) *Server {
	t.Helper()
	p := &proxy{allowed: make(map[string]string), connections: make(map[net.Conn]struct{})}
	for _, target := range targets {
		u, err := url.Parse(target.URL)
		if err != nil {
			t.Fatalf("fakeupstream proxy target: %v", err)
		}
		p.allowed[u.Host] = u.Scheme
	}
	p.transport = http.DefaultTransport.(*http.Transport).Clone()
	p.transport.Proxy = nil
	s := start(
		t,
		Options{Auth: auth},
		func(s *Server) http.Handler { p.server = s; return http.HandlerFunc(p.serve) },
	)
	s.beforeClose = p.close
	return s
}

type proxy struct {
	server      *Server
	allowed     map[string]string
	transport   *http.Transport
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

func (p *proxy) close() {
	p.mu.Lock()
	p.closed = true
	for conn := range p.connections {
		_ = conn.Close()
	}
	p.mu.Unlock()
	p.transport.CloseIdleConnections()
	p.wg.Wait()
}

func (p *proxy) serve(w http.ResponseWriter, r *http.Request) {
	allowed := p.server.options.Auth.accepts(r, true)
	p.server.record(r, allowed)
	if !allowed {
		p.server.options.Auth.challenge(w, true)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" || p.allowed[r.URL.Host] != "http" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	response, err := p.transport.RoundTrip(out)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	if p.allowed[r.Host] != "https" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.connections[client], p.connections[upstream] = struct{}{}, struct{}{}
	p.wg.Add(1)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.connections, client)
		delete(p.connections, upstream)
		p.mu.Unlock()
		p.wg.Done()
	}()
	if _, err := fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, buffered)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	<-done
}
