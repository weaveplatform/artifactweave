package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
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
	probeInterval  time.Duration
	// items applies other members' changes to federated repositories, and
	// feed is the store that reports every item change to the repository
	// streams (decision 0022).
	items artifactsapi.FederatedItems
	feed  interface{ SetItemFeed(artifactsapi.ItemFeed) }
}

// openFederation gives the runtime its pairing transport, opens stream
// delivery, configSync and the peer status reads over the same signed peer
// client (decision 0021) and registers the federation module's work, the
// last federation steps OpenRuntime takes.
func (rt *Runtime) openFederation(ctx context.Context, st PeerClientStore, queue *work.Queue, o federationOptions) error {
	f, err := newPeerClients(st, o.allowedPrivate, time.Now)
	if err != nil {
		return fmt.Errorf("peer transport: %w", err)
	}
	rt.PeerRemote = peeradapter.PairRemote{Factory: f}
	log := slog.Default()
	if rt.Logging != nil {
		log = rt.Logging.Logger("artifactweave.federation")
	}
	rt.Federation.OpenConfigSync(peeradapter.ConfigRemote{Factory: f}, log)
	rt.Federation.OpenLiveness(peeradapter.StatusRemote{Factory: f}, o.probeInterval)
	if _, err := rt.Federation.OpenStreams(federation.StreamOptions{
		Blobs: o.blobs, Remote: peeradapter.StreamRemote{Factory: f}, Queue: queue, Retention: o.retention,
		Items: o.items, Feed: o.feed, Log: log,
	}); err != nil {
		return err //nolint:wrapcheck // the module names what the engine lacks
	}
	return rt.Federation.RegisterWork(ctx, queue) //nolint:wrapcheck // the module names the work it failed to register
}
