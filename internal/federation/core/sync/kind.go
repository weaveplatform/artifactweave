package sync

import (
	"context"
	"fmt"
	"slices"
	"strings"
	gosync "sync"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Direction is the way a stream's entries travel between this instance and
// a peer.
type Direction int

// Directions.
const (
	// Send carries this instance's entries to the peer: pushed to it, or
	// pulled and fetched by it.
	Send Direction = iota + 1
	// Receive carries the peer's entries to this instance.
	Receive
)

// Applier applies one stream kind's entries on the receiving instance.
// Both methods run inside the engine's transaction, which also records the
// applied position, so an entry and its position commit together.
type Applier interface {
	// Apply applies an entry from its origin. held reports whether the
	// content a content entry refers to is in this instance's blob store;
	// when it is not, its fetch failed, the engine has recorded a failed
	// binary, and the kind keeps the item pending until Publish. A kind
	// skips an event type it does not subscribe to, which still counts as
	// applied.
	Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error
	// Publish makes visible the items of stream that were pending the blob
	// sha256, now that it has arrived, except those that a later entry has
	// deleted or replaced meanwhile, which stay as that entry left them.
	Publish(ctx context.Context, stream, sha256 string) error
	// Pending reports whether an item of stream still waits for the blob
	// sha256. None does once later entries have deleted or replaced every
	// item that did, and the failed binary is then cleared unfetched.
	Pending(ctx context.Context, stream, sha256 string) (bool, error)
}

// Kind is a family of streams: repo:<key> for a federated repository
// (FED-02), and the others decision 0021 names. It says which peers may
// exchange its streams with this instance and applies their entries.
type Kind interface {
	Applier
	// Name is the stream name's prefix: a stream "<name>:<rest>", or
	// "<name>" alone, belongs to the kind.
	Name() string
	// UseCases are the use cases a peer must hold, any one of them, for
	// this instance to send it the kind's streams or receive them from it.
	UseCases(d Direction) []model.UseCase
	// Streams lists the kind's streams this instance exchanges with any
	// peer, which delivery and pruning walk.
	Streams(ctx context.Context) ([]string, error)
	// Subscribers lists the instance ids of the peers this instance
	// exchanges stream with; none for a stream it does not know.
	Subscribers(ctx context.Context, stream string) ([]string, error)
	// References reports whether stream refers to the blob sha256, so that
	// its subscribers may fetch it from this instance.
	References(ctx context.Context, stream, sha256 string) (bool, error)
}

// KindOf is the name of the kind stream belongs to.
func KindOf(stream string) string {
	name, _, _ := strings.Cut(stream, ":")
	return name
}

// Registry holds the stream kinds this instance exchanges. FED-02 registers
// repo, P14 and P15 theirs; a stream of no registered kind is one this
// instance does not exchange.
type Registry struct {
	mu    gosync.RWMutex
	kinds map[string]Kind
}

// Register adds k; kernel.ErrConflict when a kind of its name is
// registered, and kernel.ErrInvalid for an empty name or one with a colon.
func (r *Registry) Register(k Kind) error {
	name := k.Name()
	if name == "" || strings.Contains(name, ":") {
		return fmt.Errorf("stream kind %q: %w", name, kernel.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kinds[name]; ok {
		return fmt.Errorf("stream kind %q is registered: %w", name, kernel.ErrConflict)
	}
	if r.kinds == nil {
		r.kinds = map[string]Kind{}
	}
	r.kinds[name] = k
	return nil
}

// For returns the kind stream belongs to.
func (r *Registry) For(stream string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[KindOf(stream)]
	return k, ok
}

// Kinds returns the registered kinds ordered by name.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.kinds))
	for _, k := range r.kinds {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b Kind) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

// holds reports whether p holds one of the use cases k requires for d.
func holds(p model.Peer, k Kind, d Direction) bool {
	return slices.ContainsFunc(k.UseCases(d), p.Has)
}
