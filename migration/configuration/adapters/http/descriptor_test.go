package configurationhttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

const configPath = "/artifactory/api/system/configuration"

// TestAccPatchConfigurationBody covers what the router never sends in tests: a
// body over the limit while being read, an unreadable body, and a router
// built without a store.
func TestAccPatchConfigurationBody(t *testing.T) {
	w := newRepoWorld(t)
	svc := SystemAPI{deps: Deps{Store: w.db}}
	admin := kernel.WithPrincipal(
		context.Background(),
		kernel.Principal{Name: "admin", Admin: true},
	)
	for name, c := range map[string]struct {
		svc      SystemAPI
		body     func(http.ResponseWriter) io.ReadCloser
		expected int
	}{
		"too large": {svc, func(rw http.ResponseWriter) io.ReadCloser {
			return http.MaxBytesReader(rw, io.NopCloser(strings.NewReader("abcdef")), 3)
		}, http.StatusRequestEntityTooLarge},
		"unreadable": {svc, func(http.ResponseWriter) io.ReadCloser {
			return io.NopCloser(failingReader{})
		}, http.StatusBadRequest},
		"no store": {SystemAPI{deps: Deps{}}, func(http.ResponseWriter) io.ReadCloser {
			return io.NopCloser(strings.NewReader("a: b"))
		}, http.StatusNotImplemented},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(admin, http.MethodPatch, configPath, nil)
		req.Body = c.body(rec)
		c.svc.PatchConfiguration(rec, req)
		if rec.Code != c.expected {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
