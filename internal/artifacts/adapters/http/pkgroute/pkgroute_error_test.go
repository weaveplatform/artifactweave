package pkgroute_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitErrorAbortsMatchingRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var cause error
	r := httptest.NewRequestWithContext(httpkit.WithCause(ctx, &cause), http.MethodGet, "/package", nil)
	faults := 0
	k := pkgroute.Mount(chi.NewRouter(), pkgroute.Options{
		Fault: func(*http.Request, error) { faults++ },
	}, testFormat(nil))
	rec := httptest.NewRecorder()
	rec.Code = 0
	aborted := func() (caught any) {
		defer func() { caught = recover() }()
		k.Error(rec, r, fmt.Errorf("package lookup: %w", ctx.Err()))
		return nil
	}()
	if aborted != http.ErrAbortHandler { //nolint:errorlint // net/http requires this exact sentinel
		t.Fatal("cancelled package handler did not abort", aborted)
	}
	if rec.Code != 0 || rec.Body.Len() != 0 || len(rec.Header()) != 0 ||
		!errors.Is(cause, context.Canceled) || faults != 1 {
		t.Fatal("package handler cancellation published a response or lost its cause", rec.Code, cause, faults)
	}
}

func TestUnitErrorKeepsExplicitProfileAndLiveBackendFailure(t *testing.T) {
	k := pkgroute.Mount(chi.NewRouter(), pkgroute.Options{}, testFormat(nil))
	r := httptest.NewRequest(http.MethodGet, "/package", nil)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"backend cancellation", context.Canceled, ""},
		{"typed refusal", apierror.New(http.StatusForbidden, "refused"), "{\"error\":\"refused\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			k.Error(rec, r, tc.err)
			status := http.StatusInternalServerError
			if tc.want != "" {
				status = http.StatusForbidden
			}
			if rec.Code != status || tc.want != "" && rec.Body.String() != tc.want {
				t.Fatal("package handler changed its explicit profile or live failure", rec.Code, rec.Body)
			}
		})
	}
}
