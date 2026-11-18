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

func TestUnitRetainedOriginFileProvenance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource source.Resource
		want     bool
	}{
		{"artifact", source.Resource{Kind: source.ResourceKindArtifact, Method: http.MethodGet}, true},
		{"repository metadata file", source.Resource{Kind: source.ResourceKindMetadata, RepositoryFile: true, Method: http.MethodGet}, true},
		{"API metadata", source.Resource{Kind: source.ResourceKindMetadata, Method: http.MethodGet}, false},
		{"no-cache-file overrides provenance", source.Resource{Kind: source.ResourceKindMetadata, RepositoryFile: true, NoCacheFile: true, Method: http.MethodGet}, false},
		{"POST overrides provenance", source.Resource{Kind: source.ResourceKindMetadata, RepositoryFile: true, Method: http.MethodPost}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retainedOriginFile(source.Request{Resource: tc.resource}); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}
