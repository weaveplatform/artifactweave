package app

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// federationOptions are what openFederation takes from the configuration
// and the runtime.
type federationOptions struct {
	allowedPrivate []string
	blobs          blob.Store
	retention      time.Duration
}

// openFederation gives the runtime its pairing transport, opens stream
// delivery over the same signed peer client (decision 0021) and registers
// the federation module's work, the last federation steps OpenRuntime
// takes.
func (rt *Runtime) openFederation(ctx context.Context, st PeerClientStore, queue *work.Queue, o federationOptions) error {
	f, err := newPeerClients(st, o.allowedPrivate, time.Now)
	if err != nil {
		return fmt.Errorf("peer transport: %w", err)
	}
	rt.PeerRemote = peeradapter.PairRemote{Factory: f}
	if _, err := rt.Federation.OpenStreams(federation.StreamOptions{
		Blobs: o.blobs, Remote: peeradapter.StreamRemote{Factory: f}, Queue: queue, Retention: o.retention,
	}); err != nil {
		return err //nolint:wrapcheck // the module names what the engine lacks
	}
	return rt.Federation.RegisterWork(ctx, queue) //nolint:wrapcheck // the module names the work it failed to register
}
