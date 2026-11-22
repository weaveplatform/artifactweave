package federation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPullLifecycleStaysIdleAndRefusesUnavailableContracts(t *testing.T) {
	store := inmem.New()
	m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Puller(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unopened pull driver available", err)
	}
	if err := m.OpenPull(t.Context(), nil); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing artifact owner accepted", err)
	}
	source := struct{ artifactsapi.PullSource }{}
	if err := store.Tx(t.Context(), func(ctx context.Context) error {
		return m.OpenPull(ctx, source)
	}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("pull initialization borrowed metadata transaction", err)
	}
	if err := m.OpenPull(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Puller(); err != nil {
		t.Fatal(err)
	}
	if err := m.OpenPull(t.Context(), source); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("double pull initialization admitted", err)
	}
	m.Close()
	if _, err := m.Puller(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("closed pull driver available", err)
	}
	if err := m.OpenPull(t.Context(), source); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("closed pull module reopened", err)
	}
}
