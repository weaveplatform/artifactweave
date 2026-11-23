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
