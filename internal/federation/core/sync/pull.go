package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Events serves a peer that pulls (the peer API's GET
// streams/{stream}/events): up to limit of this instance's entries on
// stream after after, which the caller has applied. after also
// acknowledges: it becomes the caller's cursor unless the cursor is
// already past it. ErrNoStream or ErrRefused when caller may not receive
// the stream from here; kernel.ErrInvalid for a limit outside 1 to
// MaxBatch or an after outside 0 to the stream's last sequence; ErrGone,
// with the caller flagged for a full sync, when the entries after it have
// been pruned.
func (e *Engine) Events(ctx context.Context, caller model.Peer, stream string, after int64, limit int) (Page, error) {
	if _, err := e.authorize(ctx, caller, stream, Send); err != nil {
		return Page{}, err
	}
	if limit < 1 || limit > MaxBatch {
		return Page{}, fmt.Errorf("limit must be 1 to %d: %w", MaxBatch, kernel.ErrInvalid)
	}
	self, err := e.self(ctx)
	if err != nil {
		return Page{}, err
	}
	head, err := e.Store.Outbox().LastSeq(ctx, self, stream)
	if err != nil {
		return Page{}, fmt.Errorf("stream %s head: %w", stream, err)
	}
	if after < 0 || after > head {
		return Page{}, fmt.Errorf("after must be 0 to %d, the stream's last sequence: %w", head, kernel.ErrInvalid)
	}
	entries, err := e.Store.Outbox().Read(ctx, self, stream, after, limit)
	if err != nil {
		return Page{}, fmt.Errorf("read %s after %d: %w", stream, after, err)
	}
	if after < head && (len(entries) == 0 || entries[0].Seq != after+1) {
		if err := e.flag(ctx, self, caller.InstanceID, stream, after); err != nil {
			return Page{}, err
		}
		return Page{}, ErrGone
	}
	if err := e.acknowledge(ctx, caller.InstanceID, stream, after); err != nil {
		return Page{}, err
	}
	page := Page{Origin: self, Entries: entries}
	if n := len(entries); n > 0 && entries[n-1].Seq < head {
		page.NextAfter = entries[n-1].Seq
	}
	return page, nil
}

// acknowledge moves peer's cursor up to after; it never moves back, so a
// delayed pull cannot undo a later one's acknowledgement. A position that
// lines up with the outbox again clears a full-sync flag, which is how a
// receiver that has caught up through a snapshot (PEER-05) leaves it.
func (e *Engine) acknowledge(ctx context.Context, peer, stream string, after int64) error {
	c, err := e.cursor(ctx, peer, stream)
	if err != nil {
		return err
	}
	if c.AckedSeq < after || c.LastSent.IsZero() {
		c.AckedSeq, c.LastSent, c.LastError = max(c.AckedSeq, after), e.now(), ""
		if err := e.Store.Outbox().PutCursor(ctx, c); err != nil {
			return fmt.Errorf("store peer %s cursor on %s: %w", peer, stream, err)
		}
	}
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	if err := e.Store.Resyncs().Clear(ctx, self, peer, stream); err != nil {
		return fmt.Errorf("clear %s's full-sync flag on %s: %w", peer, stream, err)
	}
	return nil
}

// PullFrom reads peer's entries on stream after this instance's applied
// position and applies them, a page at a time (PullKind), for a peer this
// instance pulls from because the peer cannot push to it. A 410 flags this
// instance for a full sync of the stream. A peer this instance does not
// pull from, or may not receive the stream from, is not asked.
func (e *Engine) PullFrom(ctx context.Context, peerID, stream string) error {
	p, ok, err := e.peer(ctx, peerID)
	if err != nil || !ok || !p.Pulls() {
		return err
	}
	k, err := e.authorize(ctx, p, stream, Receive)
	if errors.Is(err, ErrNoStream) || errors.Is(err, ErrRefused) {
		return nil
	} else if err != nil {
		return err
	}
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	for range runBatches {
		m, err := e.Store.Outbox().Applied(ctx, peerID, stream)
		if err != nil {
			return fmt.Errorf("applied position on %s: %w", stream, err)
		}
		page, err := e.Remote.Pull(ctx, p, stream, m.LastSeq, e.batch())
		if errors.Is(err, ErrGone) {
			return e.flag(ctx, peerID, self, stream, m.LastSeq)
		}
		if err != nil {
			return fmt.Errorf("pull %s from %s: %w", stream, peerID, err)
		}
		if len(page.Entries) == 0 {
			return nil
		}
		if err := consecutive(m.Expect(), page.Entries); err != nil {
			return fmt.Errorf("pull %s from %s: %w", stream, peerID, err)
		}
		if err := e.apply(ctx, p, k, stream, page.Entries); err != nil {
			return err
		}
		if page.NextAfter == 0 {
			return nil
		}
	}
	return e.enqueue(ctx, PullKind, item{Peer: peerID, Stream: stream})
}
