package federation

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Binaries supplies the administrator failed-binary controls and the
// repository-read-authorized binary state projection (FED-06).
func (m *Module) Binaries() (federated.Binaries, error) {
	store, ok := m.deps.Store.(federated.BinaryStore)
	if !ok {
		return federated.Binaries{}, fmt.Errorf("failed binary store unavailable: %w", kernel.ErrStore)
	}
	return federated.Binaries{
		Store: store, Repositories: m.deps.Repositories, Authorize: m.deps.pairingAdministrator,
		Streams: m.Streams,
		Read: func(ctx context.Context, key string) error {
			_, _, err := m.deps.currentStatusReader(ctx, key, "")
			return err
		},
	}, nil
}
