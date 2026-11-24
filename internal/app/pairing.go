package app

import (
	"context"
	"fmt"
	"time"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// openFederation gives the runtime its pairing transport and registers the
// federation module's work, the last federation steps OpenRuntime takes.
func (rt *Runtime) openFederation(ctx context.Context, st PeerClientStore, queue *work.Queue, allowedPrivate []string) error {
	remote, err := newPairingRemote(st, allowedPrivate)
	if err != nil {
		return err
	}
	rt.PeerRemote = remote
	return rt.Federation.RegisterWork(ctx, queue) //nolint:wrapcheck // the module names the work it failed to register
}

// newPairingRemote is the transport pairing calls other instances through
// (decision 0021): the signing peer client, which reaches peers as the
// remote and push clients reach their targets.
func newPairingRemote(st PeerClientStore, allowedPrivate []string) (pairing.Remote, error) {
	f, err := newPeerClients(st, allowedPrivate, time.Now)
	if err != nil {
		return nil, fmt.Errorf("pairing transport: %w", err)
	}
	return peeradapter.PairRemote{Factory: f}, nil
}
