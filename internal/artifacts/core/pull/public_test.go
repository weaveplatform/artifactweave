package pull_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPullPublicRecoveryCategoriesPreserveOwnerCauses(t *testing.T) {
	for _, test := range []struct {
		name            string
		cause, category error
	}{
		{"offline", source.ErrAssumedOffline, api.ErrPullOffline},
		{"permission", kernel.ErrForbidden, api.ErrPullDenied},
		{"credentials", kernel.ErrBadCredentials, api.ErrPullDenied},
		{"changed", source.ErrSuperseded, api.ErrPullChanged},
		{"invalid-result", source.ErrInvalidResult, api.ErrPullIntegrity},
		{"unsupported", source.ErrUnsupported, api.ErrPullUnsupported},
		{"unavailable", source.ErrUnavailable, api.ErrPullTemporary},
		{"upstream-integrity", &source.HTTPError{Kind: source.FailureKindIntegrity, Status: http.StatusBadGateway}, api.ErrPullIntegrity},
		{"upstream-denied", &source.HTTPError{Kind: source.FailureKindHTTP, Status: http.StatusForbidden}, api.ErrPullDenied},
		{"upstream-protocol", &source.HTTPError{Kind: source.FailureKindProtocol, Status: http.StatusBadGateway}, api.ErrPullUnsupported},
		{"upstream-temporary", &source.HTTPError{Kind: source.FailureKindConnectivity, Status: http.StatusBadGateway}, api.ErrPullTemporary},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := fixture(t)
			w.openErr = test.cause
			_, err := (pull.Public{Source: w.source}).Repository(t.Context(), "remote", api.PullSelection{})
			if !errors.Is(err, test.category) || !errors.Is(err, test.cause) {
				t.Fatal("published owner operation lost its recovery category or cause", err)
			}
		})
	}
}

func TestUnitPullOfflineAndNoStoreRefuseAdmissionBeforeDiscovery(t *testing.T) {
	for _, mode := range []string{"offline", "no-store"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			want := api.ErrPullOffline
			if mode == "offline" {
				w.config.Offline = new(true)
			} else {
				w.config.StoreArtifactsLocally = new(false)
				want = api.ErrPullNotStored
			}
			_, err := (pull.Public{Source: w.source}).Repository(t.Context(), "remote", api.PullSelection{Root: "selected"})
			if !errors.Is(err, want) || w.root != "" || w.peer.filesCalls != 0 {
				t.Fatal("cache control reached discovery or lost its explicit refusal", err)
			}
		})
	}
}
