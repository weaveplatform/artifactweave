package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const configPath = "/artifactory/api/system/configuration"

func TestPatchConfiguration(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "old-local", Class: repo.Local})
	patch := `
localRepositories:
  yaml-local:
    type: generic
    repoLayout: simple-default
    xray:
      enabled: true
  old-local: null
virtualRepositories:
  yaml-virtual:
    type: generic
    repositories: [yaml-local]
mailServer:
  host: smtp.example
`
	rec := w.do(http.MethodPatch, configPath, "admin", patch, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("PATCH = %d %s: %s", rec.Code, rec.Header(), rec.Body)
	}
	for _, want := range []string{
		"Created: localRepositories.yaml-local, virtualRepositories.yaml-virtual",
		"Deleted: localRepositories.old-local",
		"Ignored (accepted without effect): mailServer",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, rec.Body)
		}
	}
	if c := w.stored(
		"yaml-local",
	); c.RepoLayoutRef != "simple-default" || c.XrayIndex == nil ||
		!*c.XrayIndex {
		t.Fatalf("stored %+v", c)
	}
	// The same patch again changes nothing.
	rec = w.do(http.MethodPatch, configPath, "admin", patch, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Created") {
		t.Fatalf("second PATCH = %d: %s", rec.Code, rec.Body)
	}
}

func TestPatchConfigurationErrors(t *testing.T) {
	w := newRepoWorld(t)
	for name, c := range map[string]struct {
		as, body string
		fail     string
		want     int
		message  string
	}{
		"no credentials": {as: "", body: "localRepositories: {}", want: 401},
		"not an admin":   {as: "reader", body: "localRepositories: {}", want: 403},
		"empty":          {as: "admin", body: "", want: 400, message: "the document is empty"},
		"not yaml":       {as: "admin", body: "[unclosed", want: 400, message: "not a YAML mapping"},
		"protected": {
			as: "admin", body: "localRepositories:\n  artifactory-build-info: null\n",
			want: 400, message: "cannot be deleted",
		},
		"layout names another": {
			as: "admin", body: "repoLayouts:\n  mine:\n    name: theirs\n    artifactPathPattern: x\n",
			want: 400, message: "repoLayouts.mine: name theirs does not match the entry key",
		},
		"store fails": {
			as: "admin", body: "localRepositories:\n  x-local: {type: generic}\n",
			fail: "list", want: 500, message: "internal error",
		},
	} {
		clear(w.fail)
		if c.fail != "" {
			w.fail[c.fail] = true
		}
		rec := w.do(http.MethodPatch, configPath, c.as, c.body, "Content-Type", "application/yaml")
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.message) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// TestPatchConfigurationBody covers what the router never sends in tests: a
// body over the limit while being read, an unreadable body, and a router
// built without a store.
func TestPatchConfigurationBody(t *testing.T) {
	w := newRepoWorld(t)
	svc := artifactoryService{cfg: Config{Store: w.db}}
	admin := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	for name, c := range map[string]struct {
		svc  artifactoryService
		body func(http.ResponseWriter) io.ReadCloser
		want int
	}{
		"too large": {svc, func(rw http.ResponseWriter) io.ReadCloser {
			return http.MaxBytesReader(rw, io.NopCloser(strings.NewReader("abcdef")), 3)
		}, http.StatusRequestEntityTooLarge},
		"unreadable": {svc, func(http.ResponseWriter) io.ReadCloser {
			return io.NopCloser(failingReader{})
		}, http.StatusBadRequest},
		"no store": {artifactoryService{}, func(http.ResponseWriter) io.ReadCloser {
			return io.NopCloser(strings.NewReader("a: b"))
		}, http.StatusNotImplemented},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(admin, http.MethodPatch, configPath, nil)
		req.Body = c.body(rec)
		c.svc.patchConfiguration(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
