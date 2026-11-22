package terraformbackendhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func TestUnitErrorAbortsMatchingRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var cause error
	r := httptest.NewRequestWithContext(httpkit.WithCause(ctx, &cause), http.MethodGet, Prefix+"/workspaces", nil)
	faults := 0
	h := handler{o: Options{Fault: func(*http.Request, error) { faults++ }}}
	rec := httptest.NewRecorder()
	rec.Code = 0
	aborted := func() (caught any) {
		defer func() { caught = recover() }()
		h.error(rec, r, fmt.Errorf("state lookup: %w", ctx.Err()))
		return nil
	}()
	if aborted != http.ErrAbortHandler { //nolint:errorlint // net/http requires this exact sentinel
		t.Fatal("cancelled Terraform backend handler did not abort", aborted)
	}
	if rec.Code != 0 || rec.Body.Len() != 0 || len(rec.Header()) != 0 ||
		!errors.Is(cause, context.Canceled) || faults != 1 {
		t.Fatal("Terraform backend cancellation published a response or lost its cause", rec.Code, cause, faults)
	}
}

func TestUnitErrorKeepsLiveBackendCancellation(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, Prefix+"/workspaces", nil)
	rec := httptest.NewRecorder()
	handler{}.error(rec, r, fmt.Errorf("state lookup: %w", context.Canceled))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != mediaType {
		t.Fatal("Terraform backend live failure lost its profile", rec.Code, rec.Header())
	}
}
