package docker

import "net/http"

// registerProbe registers the version check, GET /v2/. A client sends it
// first and reads the answer as whether it may go on: 200 when it is
// authenticated or anonymous access lets it in. The authentication
// middleware has already answered 401 to any other caller, so reaching
// the endpoint is the check.
func (g *Registry) registerProbe() {
	g.handle(KindBase, http.MethodGet, probe)
	g.handle(KindBase, http.MethodHead, probe)
}

func probe(w http.ResponseWriter, r *http.Request, _ Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("{}"))
	}
}
