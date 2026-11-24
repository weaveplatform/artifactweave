package httpkit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestUnitHidden: WriteErr gives a Hideable's Hidden answer only while the
// request's setting is on, asks the setting only for such an answer, and
// writes a failed read as a 500; a request without the middleware keeps
// the refusal, and errors.Is still sees the refusal through the wrapper.
func TestUnitHidden(t *testing.T) {
	hidden := apierror.New(http.StatusNotFound, "Resource not found; Path: 'a:b'")
	refusal := fmt.Errorf("wrapped: %w", apierror.Hide(kernel.ErrForbidden, hidden))
	if !errors.Is(refusal, kernel.ErrForbidden) {
		t.Fatal("the wrapper hides the refusal from errors.Is")
	}
	boom := errors.New("settings down")
	asked := 0
	for _, tc := range []struct {
		name   string
		hides  httpkit.Hiding
		err    error
		status int
		asks   int
	}{
		{"no middleware", nil, refusal, http.StatusForbidden, 0},
		{"off", func(context.Context) (bool, error) { asked++; return false, nil }, refusal, http.StatusForbidden, 1},
		{"on", func(context.Context) (bool, error) { asked++; return true, nil }, refusal, http.StatusNotFound, 1},
		{"failing", func(context.Context) (bool, error) { asked++; return false, boom }, refusal, http.StatusInternalServerError, 1},
		{"not hideable", func(context.Context) (bool, error) { asked++; return true, nil }, kernel.ErrForbidden, http.StatusForbidden, 0},
	} {
		asked = 0
		handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpkit.WriteErr(w, r, tc.err)
		}))
		if tc.hides != nil {
			handler = httpkit.HideUnauthorized(tc.hides)(handler)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/artifactory/a/b", nil))
		if rec.Code != tc.status || asked != tc.asks {
			t.Errorf("%s: %d after %d reads, want %d after %d", tc.name, rec.Code, asked, tc.status, tc.asks)
		}
		if tc.status == http.StatusNotFound && !strings.Contains(rec.Body.String(), "Resource not found") {
			t.Errorf("%s: %s", tc.name, rec.Body)
		}
		if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), boom.Error()) {
			t.Errorf("%s: the failure reached the client: %s", tc.name, rec.Body)
		}
	}
	if got := refusal.Error(); got != "wrapped: Forbidden" {
		t.Fatalf("Error() = %q", got)
	}
	if on, err := httpkit.Hides(t.Context()); on || err != nil {
		t.Fatal("a bare context hides", on, err)
	}
}

// TestUnitMapperHidden: on a route MapsForbidden marks, an anonymous
// caller's kernel.ErrUnauthenticated, however wrapped, answers 404 "Not
// Found" while the setting is on, as 7.x's REST exception mappers answer it;
// a named caller's, a request without a caller, an unmarked route and
// another refusal keep the 401 or their own answer, and the setting is
// read only for an answer it can hide.
func TestUnitMapperHidden(t *testing.T) {
	anonymous := kernel.Principal{Name: kernel.AnonymousUser, Anonymous: true}
	wrapped := fmt.Errorf("repositories: %w", kernel.ErrUnauthenticated)
	for _, tc := range []struct {
		name   string
		path   string
		caller *kernel.Principal
		err    error
		on     bool
		status int
		asks   int
	}{
		{"hidden", "/api/x", &anonymous, wrapped, true, http.StatusNotFound, 1},
		{"off", "/api/x", &anonymous, wrapped, false, http.StatusUnauthorized, 1},
		{"unmarked", "/native/x", &anonymous, wrapped, true, http.StatusUnauthorized, 0},
		{"named", "/api/x", &kernel.Principal{Name: "user"}, wrapped, true, http.StatusUnauthorized, 0},
		{"no caller", "/api/x", nil, wrapped, true, http.StatusUnauthorized, 0},
		{"other refusal", "/api/x", &anonymous, kernel.ErrForbidden, true, http.StatusForbidden, 0},
	} {
		asked := 0
		hides := func(context.Context) (bool, error) { asked++; return tc.on, nil }
		handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpkit.WriteErr(w, r, tc.err)
		}))
		handler = httpkit.MapsForbidden(func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/") })(handler)
		handler = httpkit.HideUnauthorized(hides)(handler)
		ctx := t.Context()
		if tc.caller != nil {
			ctx = kernel.WithPrincipal(ctx, *tc.caller)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, tc.path, nil))
		if rec.Code != tc.status || asked != tc.asks {
			t.Errorf("%s: %d %q after %d reads, want %d after %d", tc.name, rec.Code, rec.Body, asked, tc.status, tc.asks)
		}
		if tc.status == http.StatusNotFound && (!strings.Contains(rec.Body.String(), `"message":"Not Found"`) ||
			rec.Header().Get("WWW-Authenticate") != "") {
			t.Errorf("%s: %v %s", tc.name, rec.Header(), rec.Body)
		}
	}
}
