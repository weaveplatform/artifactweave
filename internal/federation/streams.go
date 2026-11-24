package federation

import (
	"context"
	"fmt"
	"time"

	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

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
}

// OpenStreams builds the stream delivery engine (decision 0021, Ordered
// outbox) over the module's store, with no stream kind registered: FED-02
// registers repo:<key>, and a test its own. RegisterWork then gives the
// queue its work. kernel.ErrStore when the store has no outbox, peer
// records or instance identity.
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
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.streams = e
	return e, nil
}

// Streams is the engine OpenStreams built, nil before it: the peer API's
// stream operations then keep their 501 stubs.
func (m *Module) Streams() *streamsync.Engine {
	if m == nil {
		return nil
	}
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	return m.streams
}

// registerStreamWork gives the queue the engine's work when OpenStreams
// has built it.
func (m *Module) registerStreamWork(ctx context.Context, queue *work.Queue) error {
	e := m.Streams()
	if e == nil {
		return nil
	}
	store, ok := m.deps.Store.(interface{ WorkQueue() work.WorkQueue })
	if !ok {
		return fmt.Errorf("stream work queue unavailable: %w", kernel.ErrStore)
	}
	return e.Register(ctx, queue, store.WorkQueue()) //nolint:wrapcheck // Register names the kind it failed on
}
