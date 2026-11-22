package httpkit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitWriteErrAbortsCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var cause error
	r := httptest.NewRequestWithContext(httpkit.WithCause(ctx, &cause), http.MethodGet, "/artifact", nil)
	rec := httptest.NewRecorder()
	rec.Code = 0
	aborted := func() (caught any) {
		defer func() { caught = recover() }()
		httpkit.WriteErr(rec, r, fmt.Errorf("store: %w", ctx.Err()))
		return nil
	}()
	if aborted != http.ErrAbortHandler { //nolint:errorlint // the exact sentinel net/http handles
		t.Errorf("cancelled request did not abort through net/http")
	}
	if rec.Code != 0 || rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Errorf("cancelled request published a response: status %d", rec.Code)
	}
	if !errors.Is(cause, context.Canceled) {
		t.Error("request cancellation cause was lost")
	}
}

func TestUnitWriteErrKeepsIndependentFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancel   bool
		deadline bool
		err      error
		status   int
	}{
		{"live request backend cancellation", false, false, fmt.Errorf("backend: %w", context.Canceled), 500},
		{"cancelled request disk failure", true, false, errors.New("sqlite: disk I/O error"), 500},
		{"server deadline", false, true, context.DeadlineExceeded, 500},
		{"typed request timeout", false, true, apierror.New(http.StatusRequestTimeout, "request timed out"), 408},
		{"cancelled request typed refusal", true, false, apierror.New(http.StatusForbidden, "forbidden"), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if tc.deadline {
				expired, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				ctx = expired
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/artifact", nil)
			rec := httptest.NewRecorder()
			httpkit.WriteErr(rec, r, tc.err)
			if rec.Code != tc.status {
				t.Fatal("an independent failure changed its response status", rec.Code)
			}
		})
	}
}
