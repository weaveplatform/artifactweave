package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// An ended request must retain its cancellation mapping even when a driver
// reports a failed statement along with it; a live driver deadline is different.
func TestUnitRetainedErrorPreservesEndedRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		fault error
		want  error
	}{
		{errors.Join(kernel.ErrStore, context.Canceled), context.Canceled},
		{errors.Join(kernel.ErrStore, context.DeadlineExceeded), nil},
	} {
		got := retainedError(ctx, tc.fault)
		if tc.want != nil {
			if !errors.Is(got, tc.want) {
				t.Fatal(got)
			}
		} else {
			var failure *source.HTTPError
			if !errors.As(got, &failure) || failure.Status != http.StatusGatewayTimeout {
				t.Fatal(got)
			}
		}
	}
}
