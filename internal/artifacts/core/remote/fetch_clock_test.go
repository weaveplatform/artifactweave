package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type clockDriverTimeout struct{}

func (clockDriverTimeout) Error() string   { return "database connection timeout" }
func (clockDriverTimeout) Timeout() bool   { return true }
func (clockDriverTimeout) Temporary() bool { return true }

func TestUnitClockPublicationErrorPreservesStorageCauseAndVerdicts(t *testing.T) {
	driver := errors.New("database statement failed")
	statement := fmt.Errorf("publish clock: %w", errors.Join(kernel.ErrStore, driver))
	for _, tc := range []struct {
		name    string
		err     error
		ended   string
		storage bool
		want    error
		status  int
		kind    source.FailureKind
	}{
		{name: "statement", err: statement, storage: true},
		{name: "statement with driver deadline", err: errors.Join(statement, context.DeadlineExceeded), storage: true},
		{name: "statement with driver net timeout", err: errors.Join(statement, clockDriverTimeout{}), storage: true},
		{name: "missing record", err: kernel.ErrNotFound, want: kernel.ErrNotFound},
		{name: "conflicting token", err: kernel.ErrConflict, want: source.ErrSuperseded},
		{name: "superseded source", err: source.ErrSuperseded, want: source.ErrSuperseded},
		{name: "read refusal", err: source.ErrDenied, want: source.ErrDenied},
		{name: "invalid record", err: kernel.ErrInvalid, status: 502, kind: source.FailureKindProtocol},
		{name: "other failure", err: driver, status: 502, kind: source.FailureKindProtocol},
		{name: "upstream deadline", err: context.DeadlineExceeded, status: 504, kind: source.FailureKindTimeout},
		{name: "upstream timeout", err: clockDriverTimeout{}, status: 504, kind: source.FailureKindTimeout},
		{name: "upstream unavailable", err: &source.HTTPError{Status: http.StatusServiceUnavailable, Kind: source.FailureKindHTTP}, status: 503, kind: source.FailureKindHTTP},
		{name: "request canceled", err: errors.Join(statement, context.Canceled), ended: "canceled", want: context.Canceled},
		{name: "request deadline", err: errors.Join(statement, context.DeadlineExceeded), ended: "deadline", status: 504, kind: source.FailureKindTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			switch tc.ended {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Hour))
				defer cancel()
			}
			got := clockError(ctx, tc.err)
			if tc.storage {
				var fault *item.StorageFault
				if !errors.As(got, &fault) || fault.Cause != tc.err ||
					!errors.Is(got, kernel.ErrStore) || !errors.Is(got, driver) ||
					!errors.Is(got, format.ErrStorageFault) || errors.As(got, new(*source.HTTPError)) {
					t.Fatalf("lost own statement cause or classified it as upstream: %v", got)
				}
				return
			}
			if errors.Is(got, format.ErrStorageFault) {
				t.Fatalf("ordinary verdict or ended request became a storage fault: %v", got)
			}
			if tc.want != nil {
				if got != tc.want {
					t.Fatalf("got %v, want original verdict %v", got, tc.want)
				}
				return
			}
			var failure *source.HTTPError
			if !errors.As(got, &failure) || failure.Status != tc.status || failure.Kind != tc.kind {
				t.Fatalf("got %v, want upstream %d/%v", got, tc.status, tc.kind)
			}
		})
	}
}
