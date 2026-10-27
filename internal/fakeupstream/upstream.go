// Package fakeupstream provides deterministic, loopback-only HTTP fixtures for remote
// repository tests. It models upstream mechanics, not observed Artifactory behavior.
// Keep ecosystem documents in the adapter tests that understand their schemas.
package fakeupstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// Options are fixed before starting the server. TLS enables HTTPS; ClientCAs and
// ClientAuth can require a client certificate. Now defaults to time.Now.
type Options struct {
	TLS  *tls.Config
	Now  func() time.Time
	Auth Auth
	// Secrets also redacts fixture-specific secrets embedded in paths or safe headers.
	// Query values, credentials, cookies and unrecognized headers are always redacted.
	Secrets []string
}

// Auth requires either a bearer Token or a basic Username/Password. An empty policy
// permits anonymous requests. A token takes precedence over basic credentials.
type Auth struct{ Username, Password, Token string }

func (a Auth) accepts(r *http.Request, proxy bool) bool {
	name := "Authorization"
	if proxy {
		name = "Proxy-Authorization"
	}
	if a.Token != "" {
		return equal(r.Header.Get(name), "Bearer "+a.Token)
	}
	if a.Username == "" && a.Password == "" {
		return true
	}
	copyRequest := r.Clone(r.Context())
	copyRequest.Header.Set("Authorization", r.Header.Get(name))
	u, p, ok := copyRequest.BasicAuth()
	return ok && equal(u, a.Username) && equal(p, a.Password)
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (a Auth) challenge(w http.ResponseWriter, proxy bool) {
	name, status := "WWW-Authenticate", http.StatusUnauthorized
	if proxy {
		name, status = "Proxy-Authenticate", http.StatusProxyAuthRequired
	}
	challenge := `Basic realm="fakeupstream"`
	if a.Token != "" {
		challenge = `Bearer realm="fakeupstream"`
	}
	w.Header().Set(name, challenge)
	w.WriteHeader(status)
}

// Resource is an atomic response snapshot. Set copies Body and Header. A zero
// Status serves normal content with HEAD, Range and HTTP validators. Other statuses
// serve Body verbatim. Redirect uses Status or defaults to 302.
type Resource struct {
	Body     []byte
	Header   http.Header
	Modified time.Time
	ETag     string
	Status   int
	Redirect string
	Fault    Fault
}

// Fault interrupts a normal resource without sleeps. TruncateAfter > 0 aborts the
// connection after that many body bytes. LengthDelta falsifies Content-Length.
// BeforeBody flushes headers first, allowing clients to observe a stalled body.
type Fault struct {
	BeforeHeaders  *Barrier
	BeforeBody     *Barrier
	DropConnection bool
	TruncateAfter  int64
	LengthDelta    int64
}

// Server owns its listener and connections; New registers cleanup with the test.
// Set/Delete/Requests/Count may run concurrently with requests.
type Server struct {
	URL         string
	srv         *httptest.Server
	cancel      context.CancelFunc
	closeOnce   sync.Once
	beforeClose func()
	options     Options
	key         [32]byte
	mu          sync.Mutex
	resources   map[string]Resource
	requests    []Request
}

func New(t testing.TB, options Options) *Server {
	t.Helper()
	return start(t, options, func(s *Server) http.Handler { return http.HandlerFunc(s.serve) })
}

func start(t testing.TB, options Options, handler func(*Server) http.Handler) *Server {
	t.Helper()
	if options.Now == nil {
		options.Now = time.Now
	}
	options.Secrets = append([]string(nil), options.Secrets...)
	s := &Server{options: options, resources: make(map[string]Resource)}
	if _, err := rand.Read(s.key[:]); err != nil {
		t.Fatalf("fakeupstream recording key: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	r := chi.NewRouter()
	r.Handle("/*", handler(s))
	s.srv = httptest.NewUnstartedServer(r)
	s.srv.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	// HTTP parser/handshake errors may contain caller-controlled strings. Assertions
	// use the sanitized recorder rather than writing those errors to test artifacts.
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if options.TLS != nil {
		s.srv.TLS = options.TLS.Clone()
		s.srv.StartTLS()
	} else {
		s.srv.Start()
	}
	s.URL = s.srv.URL
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Client() *http.Client           { return s.srv.Client() }
func (s *Server) Certificate() *x509.Certificate { return s.srv.Certificate() }

func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		if s.beforeClose != nil {
			s.beforeClose()
		}
		s.srv.CloseClientConnections()
		s.srv.Close()
	})
}

func (s *Server) Set(path string, resource Resource) {
	resource.Body = bytes.Clone(resource.Body)
	resource.Header = resource.Header.Clone()
	if resource.Modified.IsZero() {
		resource.Modified = s.options.Now().UTC()
	}
	if resource.ETag == "" {
		resource.ETag = fmt.Sprintf(`"%x"`, sha256.Sum256(resource.Body))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[path] = resource
}

func (s *Server) Delete(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resources, path)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	allowed := s.options.Auth.accepts(r, false)
	s.record(r, allowed)
	if !allowed {
		s.options.Auth.challenge(w, false)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	resource, ok := s.resources[r.URL.Path]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if resource.Fault.BeforeHeaders != nil &&
		resource.Fault.BeforeHeaders.Wait(r.Context()) != nil {
		return
	}
	if resource.Fault.DropConnection {
		panic(http.ErrAbortHandler)
	}
	for name, values := range resource.Header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.Header().Set("Date", s.options.Now().UTC().Format(http.TimeFormat))
	w.Header().Set("ETag", resource.ETag)
	fw := &faultWriter{ResponseWriter: w, ctx: r.Context(), fault: resource.Fault}
	if resource.Redirect != "" {
		status := resource.Status
		if status == 0 {
			status = http.StatusFound
		}
		w.Header().Set("Location", resource.Redirect)
		fw.WriteHeader(status)
		return
	}
	if resource.Status != 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(resource.Body)))
		fw.WriteHeader(resource.Status)
		if r.Method != http.MethodHead {
			_, _ = fw.Write(resource.Body)
		}
		return
	}
	http.ServeContent(fw, r, r.URL.Path, resource.Modified, bytes.NewReader(resource.Body))
}

type faultWriter struct {
	http.ResponseWriter
	ctx        context.Context
	fault      Fault
	written    int64
	hasStarted bool
}

func (w *faultWriter) WriteHeader(status int) {
	if w.hasStarted {
		return
	}
	w.hasStarted = true
	if w.fault.LengthDelta != 0 {
		n, err := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
		if err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(max(0, n+w.fault.LengthDelta), 10))
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *faultWriter) Write(p []byte) (int, error) {
	if !w.hasStarted {
		w.WriteHeader(http.StatusOK)
	}
	if w.fault.BeforeBody != nil {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
		if err := w.fault.BeforeBody.Wait(w.ctx); err != nil {
			return 0, fmt.Errorf("fakeupstream body wait: %w", err)
		}
	}
	if limit := w.fault.TruncateAfter; limit > 0 && w.written+int64(len(p)) >= limit {
		n, _ := w.ResponseWriter.Write(p[:limit-w.written])
		w.written += int64(n)
		_ = http.NewResponseController(w.ResponseWriter).Flush()
		panic(http.ErrAbortHandler)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	if err != nil {
		return n, fmt.Errorf("fakeupstream body: %w", err)
	}
	return n, nil
}
