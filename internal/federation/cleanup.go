package federation

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// Cleanup shares the same current-relationship and failure fences between
// previews, execution and the peer observation handler. It starts no worker.
func (m *Module) Cleanup() api.Cleanup {
	if m.cleanup == nil {
		return nil
	}
	return m.cleanup
}

// CleanupReceiver is the signed peer handler's observation and exclusion owner contract.
func (m *Module) CleanupReceiver() *cleanup.Service { return m.cleanup }

// OpenCleanup supplies the runtime's signed peer transport before serving.
func (m *Module) OpenCleanup(remote cleanup.Remote) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.cleanupRemote = remote
}

type cleanupRemote struct{ module *Module }

func (r cleanupRemote) Downloads(ctx context.Context, peer model.Peer, key string, request cleanup.Request) (cleanup.Answer, error) {
	r.module.pushMu.Lock()
	remote := r.module.cleanupRemote
	r.module.pushMu.Unlock()
	if remote == nil {
		return cleanup.Answer{}, cleanup.ErrUnavailable
	}
	return remote.Downloads(ctx, peer, key, request) //nolint:wrapcheck // the transport names its signed observation boundary
}

func (r cleanupRemote) Coordinate(ctx context.Context, peer model.Peer, key string, request cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
	r.module.pushMu.Lock()
	remote := r.module.cleanupRemote
	r.module.pushMu.Unlock()
	coordinated, ok := remote.(cleanup.CoordinationRemote)
	if !ok {
		return cleanup.CoordinationAnswer{}, cleanup.ErrUnavailable
	}
	return coordinated.Coordinate(ctx, peer, key, request) //nolint:wrapcheck // the signed transport names its coordination boundary.
}
