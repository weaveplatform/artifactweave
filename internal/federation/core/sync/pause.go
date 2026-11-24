package sync

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Pauser is the part of a kind that pauses sending a stream to a peer
// while the peer stays subscribed: a federated repository's disabled
// member (decision 0022, Members). A paused peer is pushed nothing and a
// pull of its answers no entries, but its acknowledged position stays,
// so pruning keeps what it has not received within the retention window
// and it catches up from that position once the pause ends. A kind
// without one never pauses.
type Pauser interface {
	Paused(ctx context.Context, stream, peer string) (bool, error)
}

// paused reports whether k pauses sending stream to peer.
func paused(ctx context.Context, k Kind, stream, peer string) (bool, error) {
	p, ok := k.(Pauser)
	if !ok {
		return false, nil
	}
	is, err := p.Paused(ctx, stream, peer)
	if err != nil {
		return false, fmt.Errorf("stream %s paused for %s: %w", stream, peer, err)
	}
	return is, nil
}

// Observe merges t into the clock, so the next entry this instance
// appends orders after it. A producer calls it with the stamp a change
// replaces, which the clock may not have seen when the stamp came from a
// snapshot or a peer whose entry it has not applied through this engine.
func (e *Engine) Observe(t outbox.HLC) { e.observe(t) }

// Wake starts now what the next sweep would for stream (Sweep): the
// dispatchers of the peers behind its last entry and the pulls. A kind
// calls it in the transaction that ends a pause or adds a subscriber, so
// they do not wait up to SweepInterval. A stream of no registered kind
// has nothing to start.
func (e *Engine) Wake(ctx context.Context, stream string) error {
	k, ok := e.Kinds.For(stream)
	if !ok {
		return nil
	}
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	return e.sweepStream(ctx, self, k, stream)
}
