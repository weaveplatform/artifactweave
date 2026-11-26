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
// Mutation methods run inside the engine's transaction, which also records
// the applied position, so an entry and its position commit together.
type Applier interface {
	// Apply applies an entry from its origin. held reports whether the
	// content a content entry refers to is in this instance's blob store;
	// when it is not, its fetch failed, the engine has recorded a failed
	// binary, and the kind keeps the item pending until Publish. A kind
	// skips an event type it does not subscribe to, which still counts as
	// applied.
	Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error
	// Publish makes visible the items of stream that were pending the blob
	// sha256 whose declared size matches the validated size. Items a later
	// entry deleted or replaced stay as that entry left them; other sizes
	// remain pending, even though the digest is stored.
	Publish(ctx context.Context, stream, sha256 string, size int64) error
	// Pending lists the distinct declared sizes of items of stream still
	// waiting for sha256, which validation holds the content to. No sizes
	// remain once later entries deleted or replaced every pending item,
	// and the failed binary is then cleared without fetching.
	Pending(ctx context.Context, stream, sha256 string) ([]int64, error)
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

// FailureDetails supplies a kind's optional SHA-1 for the public failed-binary
// controls. It runs in the transaction that keeps the pending change and its
// failed fetch, so the checksum survives a later replacement or deletion.
type FailureDetails interface {
	FailedSHA1(ctx context.Context, stream, sha256 string) (string, error)
}

// SnapshotProvider is the part of a kind that lists a stream's current
// state for a snapshot on the sending instance (decision 0021, Full sync).
// A kind without one takes no snapshots, and a receiver cannot resync its
// streams. FED-04 supplies the repository's.
type SnapshotProvider interface {
	// SnapshotItems lists up to limit of stream's items as this instance,
	// self, holds them, tombstones included, with keys after after in
	// byte order of key, each key once. The engine calls it inside the
	// transaction that reads the stream's sequence, page after page.
	SnapshotItems(ctx context.Context, self, stream, after string, limit int) ([]model.SnapshotEntry, error)
}

// Reconciler is the part of a kind that a full sync applies a snapshot
// through on the receiving instance. Reconcile and Discard run inside the
// engine's transaction, which also records how far the run has got.
type Reconciler interface {
	// LocalItems lists up to limit of this instance's items of origin's
	// stream, tombstones included, with keys after after in byte order of
	// key, each with the clock time and origin of its last change.
	LocalItems(ctx context.Context, origin, stream, after string, limit int) ([]model.SnapshotEntry, error)
	// Reconcile makes item en.Key of origin's stream what en says: its
	// state, or a tombstone that keeps an older change from restoring it.
	// held is Apply's: false when the content's fetch failed, and the kind
	// keeps the item pending until Publish.
	Reconcile(ctx context.Context, origin, stream string, en model.SnapshotEntry, held bool) error
	// Discard removes item key of origin's stream, tombstone included,
	// which a forced full sync finds in no snapshot entry.
	Discard(ctx context.Context, origin, stream, key string) error
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
