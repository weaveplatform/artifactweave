package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// openFederation gives the runtime its pairing transport and configSync its
// own (decision 0021: the signing peer client), and registers the
// federation module's work, the last federation steps OpenRuntime takes.
func (rt *Runtime) openFederation(ctx context.Context, st PeerClientStore, queue *work.Queue, allowedPrivate []string) error {
	f, err := newPeerClients(st, allowedPrivate, time.Now)
	if err != nil {
		return fmt.Errorf("peer transport: %w", err)
	}
	rt.PeerRemote = peeradapter.PairRemote{Factory: f}
	log := slog.Default()
	if rt.Logging != nil {
		log = rt.Logging.Logger("artifactweave.federation")
	}
	rt.Federation.OpenConfigSync(peeradapter.ConfigRemote{Factory: f}, log)
	return rt.Federation.RegisterWork(ctx, queue) //nolint:wrapcheck // the module names the work it failed to register
}
