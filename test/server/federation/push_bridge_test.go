package federation_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

// pushBridge forwards to the separate real application. Hooks interrupt only
// the HTTP seam; successful effects always run through its native handlers.
type pushBridge struct {
	URL    string
	proxy  *httputil.ReverseProxy
	mu     sync.Mutex
	hook   func(http.ResponseWriter, *http.Request) bool
	counts map[string]int
}

func newPushBridge(t *testing.T, target string) *pushBridge {
	t.Helper()
	origin, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(origin)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.ErrorLog = slog.NewLogLogger(slog.DiscardHandler, slog.LevelError)
	b := &pushBridge{proxy: proxy, counts: make(map[string]int)}
	router := chi.NewRouter()
	router.Handle("/*", b)
	server := httptest.NewServer(router)
	b.URL = server.URL
	t.Cleanup(func() { server.Close(); transport.CloseIdleConnections() })
	return b
}

func (b *pushBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.counts[r.Method+" "+r.URL.Path]++
	hook := b.hook
	b.mu.Unlock()
	if hook != nil && hook(w, r) {
		return
	}
	b.proxy.ServeHTTP(w, r)
}

func (b *pushBridge) intercept(hook func(http.ResponseWriter, *http.Request) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hook = hook
}

func (b *pushBridge) count(method, path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[method+" "+path]
}

func forwardRecorded(w http.ResponseWriter, response *httptest.ResponseRecorder) {
	for name, values := range response.Header() {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}
