package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// TombstonePruneKind runs every streamsync.PruneInterval: it removes the
// federated repositories' tombstones older than the outbox retention
// window. The kind string is persisted in work items, so it never changes.
const TombstonePruneKind = "federation.repo-tombstone-prune"

// StreamOptions are what the stream delivery engine needs beyond the
// module's store.
type StreamOptions struct {
	// Blobs holds the content entries refer to.
	Blobs blob.Store
	// Remote calls other instances: the signed peer client.
	Remote streamsync.Remote
	// Queue runs the engine's work.
	Queue *work.Queue
	// Retention is federation.outbox_retention; zero is seven days.
	Retention time.Duration
	// Items applies other members' changes to federated repositories (the
	// artifacts module's FederatedItems). With it OpenStreams registers the
	// repo:<key> stream kind; without it none is registered, for a test
	// that registers its own.
	Items artifactsapi.FederatedItems
	// Feed, when set, is given the repository stream as the store's item
	// feed, so every change to a federated repository is appended to its
	// stream in the change's transaction.
	Feed interface{ SetItemFeed(artifactsapi.ItemFeed) }
	Log  *slog.Logger
}

// OpenStreams builds the stream delivery engine (decision 0021, Ordered
// outbox) over the module's store and, given Items, registers the
// repo:<key> stream kind of federated repositories (decision 0022) and
// makes it the item feed. RegisterWork then gives the queue its work.
// kernel.ErrStore when the store has no outbox, peer records, instance
// identity or conflict-rule stamps.
func (m *Module) OpenStreams(o StreamOptions) (*streamsync.Engine, error) {
	store, ok := m.deps.Store.(streamsync.Store)
	if !ok {
		return nil, fmt.Errorf("stream store unavailable: %w", kernel.ErrStore)
	}
	if o.Blobs == nil || o.Remote == nil || o.Queue == nil {
		return nil, fmt.Errorf("stream delivery needs blobs, a peer client and a queue: %w", kernel.ErrInvalid)
	}
	e := &streamsync.Engine{
		Store: store, Kinds: &streamsync.Registry{}, Blobs: o.Blobs, Remote: o.Remote,
		Queue: o.Queue, Retention: o.Retention,
	}
	if o.Items != nil {
		ms, err := m.deps.membership()
		if err != nil {
			return nil, err
		}
		k := federated.Stream{
			Store: ms.Store, Engine: e, Items: o.Items, Repositories: m.deps.Repositories,
			SelfBase: ms.SelfBase, Log: o.Log,
		}
		if err := e.Kinds.Register(k); err != nil {
			return nil, fmt.Errorf("register the repository stream: %w", err)
		}
		if o.Feed != nil {
			o.Feed.SetItemFeed(k)
		}
		m.deps.streams.mu.Lock()
		m.repoStream = &k
		m.deps.streams.mu.Unlock()
	}
	m.deps.streams.mu.Lock()
	defer m.deps.streams.mu.Unlock()
	m.deps.streams.e = e
	return e, nil
}

// Streams is the engine OpenStreams built, nil before it: the peer API's
// stream operations then keep their 501 stubs.
func (m *Module) Streams() *streamsync.Engine {
	if m == nil {
		return nil
	}
	return m.deps.streams.get()
}

// registerStreamWork gives the queue the engine's work when OpenStreams
// has built it, and the tombstone pruning when it registered the
// repository stream.
func (m *Module) registerStreamWork(ctx context.Context, queue *work.Queue) error {
	e := m.Streams()
	if e == nil {
		return nil
	}
	store, ok := m.deps.Store.(interface{ WorkQueue() work.WorkQueue })
	if !ok {
		return fmt.Errorf("stream work queue unavailable: %w", kernel.ErrStore)
	}
	if err := e.Register(ctx, queue, store.WorkQueue()); err != nil {
		return err //nolint:wrapcheck // Register names the kind it failed on
	}
	m.deps.streams.mu.Lock()
	k := m.repoStream
	m.deps.streams.mu.Unlock()
	if k == nil {
		return nil
	}
	retention := e.Retention
	if retention <= 0 {
		retention = streamsync.DefaultRetention
	}
	again := func(ctx context.Context) error {
		if err := store.WorkQueue().Enqueue(ctx, TombstonePruneKind, "tombstones", nil, time.Now()); err != nil {
			return fmt.Errorf("queue %s: %w", TombstonePruneKind, err)
		}
		return nil
	}
	if err := queue.HandleEvery(TombstonePruneKind, streamsync.PruneInterval, func(ctx context.Context, _ work.WorkItem) error {
		if _, err := k.PruneTombstones(ctx, time.Now(), retention); err != nil {
			return err //nolint:wrapcheck // the stream names what failed
		}
		// The item is done once it returns; queueing the next keeps the
		// schedule going, as the engine's periodic work does.
		return again(ctx)
	}); err != nil {
		return fmt.Errorf("register %s: %w", TombstonePruneKind, err)
	}
	if _, err := store.WorkQueue().Get(ctx, TombstonePruneKind, "tombstones"); errors.Is(err, kernel.ErrNotFound) {
		return again(ctx)
	} else if err != nil {
		return fmt.Errorf("read %s: %w", TombstonePruneKind, err)
	}
	return nil
}
