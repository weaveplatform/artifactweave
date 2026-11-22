package federation

import (
	"context"
	"fmt"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type pullStore interface {
	Store
	port.PullStore
}

// OpenPull binds the artifact owner's cache operations after module assembly.
// Admission and every bounded step remain idle until the dispatcher calls them.
func (m *Module) OpenPull(ctx context.Context, source artifactsapi.PullSource) error {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if m.closed || m.pull != nil {
		return fmt.Errorf("replication pull lifecycle already ended or opened: %w", kernel.ErrConflict)
	}
	store, ok := m.deps.Store.(pullStore)
	if !ok || source == nil || m.deps.Access.Projects == nil || m.deps.Access.Authenticator == nil || m.deps.Access.Authorizer == nil {
		return fmt.Errorf("single-pull application contracts unavailable: %w", kernel.ErrStore)
	}
	if store.InTx(ctx) {
		return fmt.Errorf("open single pull inside metadata transaction: %w", kernel.ErrInvalid)
	}
	m.pull = &replication.Pull{Store: store, Configurations: m.Configurations(), Source: source, Restore: m.restoreTransferCaller}
	return nil
}

// Puller returns the initialized bounded owning-cache transfer driver.
func (m *Module) Puller() (replication.Pull, error) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if m.closed || m.pull == nil {
		return replication.Pull{}, fmt.Errorf("single pull is not open: %w", kernel.ErrStore)
	}
	return *m.pull, nil
}
