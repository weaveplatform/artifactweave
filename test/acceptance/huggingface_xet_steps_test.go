package acceptance

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
)

type huggingFaceXetProof struct {
	server             *httptest.Server
	uploads, downloads atomic.Int32
	fallback           atomic.Bool
}

// The native client must cross the CAS boundary. Blocking the large-file HTTP
// path proves that a successful download cannot silently use its fallback.
func (w *world) xetProofProxy() (*huggingFaceXetProof, error) {
	target, err := url.Parse(w.url)
	if err != nil {
		return nil, err
	}
	proof := &huggingFaceXetProof{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	router := chi.NewRouter()
	router.Handle("/*", http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/resolve/") &&
			strings.HasSuffix(r.URL.Path, "/weights.bin") {
			proof.fallback.Store(true)
			out.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/v1/xorbs/") {
			proof.uploads.Add(1)
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/xet/") &&
			strings.Contains(r.URL.Path, "/data/") {
			proof.downloads.Add(1)
		}
		r.Header.Set("X-JFrog-Override-Base-Url", proof.server.URL)
		proxy.ServeHTTP(out, r)
	}))
	proof.server = httptest.NewServer(router)
	return proof, nil
}
