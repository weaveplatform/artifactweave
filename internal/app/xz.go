package app

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// xzRequests makes each request one holder of the xz dictionary budget
// (xzfile.WithRequest), so a request that reads a second xz file while its
// first is open, such as a Debian package's control and data archives,
// never waits behind a decode that waits for its own first dictionary.
func xzRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(xzfile.WithRequest(r.Context())))
	})
}
