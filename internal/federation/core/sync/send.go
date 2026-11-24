package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// sha256Hex is a blob reference's form on the wire.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Append records a change for this instance's peers: it stores e as the
// next entry of its stream, with this instance as origin and, unless e
// carries one, the clock's time, and starts the dispatcher of every
// subscriber this instance pushes to. Call it inside the transaction that
// makes the change, so an entry exists exactly when its change commits
// (decision 0021) and a rolled-back change is never delivered. The stream
// must be of a registered kind; its payload, when present, a JSON object.
func (e *Engine) Append(ctx context.Context, en outbox.OutboxEntry) (outbox.OutboxEntry, error) {
	k, ok := e.Kinds.For(en.Stream)
	if !ok {
		return outbox.OutboxEntry{}, fmt.Errorf("stream %q is of no registered kind: %w", en.Stream, kernel.ErrInvalid)
	}
	if err := validEntry(en); err != nil {
		return outbox.OutboxEntry{}, err
	}
	self, err := e.self(ctx)
	if err != nil {
		return outbox.OutboxEntry{}, err
	}
	en.Origin, en.Seq, en.Created = self, 0, e.now()
	if en.Time == (outbox.HLC{}) {
		en.Time = e.tick()
	} else {
		e.observe(en.Time)
	}
	stored, err := e.Store.Outbox().Append(ctx, en)
	if err != nil {
		return outbox.OutboxEntry{}, fmt.Errorf("append to %s: %w", en.Stream, err)
	}
	subs, err := k.Subscribers(ctx, en.Stream)
	if err != nil {
		return outbox.OutboxEntry{}, fmt.Errorf("stream %s subscribers: %w", en.Stream, err)
	}
	for _, id := range subs {
		p, ok, err := e.peer(ctx, id)
		if err != nil {
			return outbox.OutboxEntry{}, err
		}
		if ok && p.Pushes() && holds(p, k, Send) {
			if err := e.enqueue(ctx, SendKind, item{Peer: id, Stream: en.Stream}); err != nil {
				return outbox.OutboxEntry{}, err
			}
		}
	}
	return stored, nil
}

// validEntry checks what the wire carries: an event type, a payload that
// is a JSON object, and a blob reference of 64 lowercase hex digits with a
// size that is not negative.
func validEntry(en outbox.OutboxEntry) error {
	switch {
	case en.EventType == "":
		return fmt.Errorf("an entry needs an event type: %w", kernel.ErrInvalid)
	case len(en.Payload) > 0 && !jsonObject(en.Payload):
		return fmt.Errorf("an entry's payload must be a JSON object: %w", kernel.ErrInvalid)
	case en.BlobSHA256 != "" && !sha256Hex.MatchString(en.BlobSHA256):
		return fmt.Errorf("an entry's blob must be 64 lowercase hex digits: %w", kernel.ErrInvalid)
	case en.BlobSize < 0, en.BlobSHA256 == "" && en.BlobSize != 0:
		return fmt.Errorf("an entry's blob size must be a size of its blob: %w", kernel.ErrInvalid)
	}
	return nil
}

func jsonObject(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && m != nil
}

// Dispatch pushes stream's entries after peer's acknowledged position, a
// batch at a time, and stores each answered position as the cursor
// (SendKind). On 409 it resumes from the sequence the peer expects. A
// failure is recorded on the cursor and returned, and the queue retries it
// with back-off. A peer that pulls, has no record or is not granted the
// stream is not pushed to. Nor is one flagged for a full sync: a snapshot
// was taken for it, or it is behind the pruned floor, which flags it. Such
// a peer is pushed only the first entry still held, which it refuses with
// 409 and which makes it probe this instance, find the gap and resync
// (Resync); a peer that accepts it was lined up after all, and is sent to
// again.
func (e *Engine) Dispatch(ctx context.Context, peerID, stream string) error {
	p, ok, err := e.peer(ctx, peerID)
	if err != nil || !ok || !p.Pushes() {
		return err
	}
	if _, err := e.authorize(ctx, p, stream, Send); errors.Is(err, ErrNoStream) || errors.Is(err, ErrRefused) {
		return nil
	} else if err != nil {
		return err
	}
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	for range runBatches {
		c, err := e.cursor(ctx, peerID, stream)
		if err != nil {
			return err
		}
		head, err := e.Store.Outbox().LastSeq(ctx, self, stream)
		if err != nil {
			return fmt.Errorf("stream %s head: %w", stream, err)
		}
		if c.AckedSeq >= head {
			return nil
		}
		entries, err := e.Store.Outbox().Read(ctx, self, stream, c.AckedSeq, e.batch())
		if err != nil {
			return fmt.Errorf("read %s after %d: %w", stream, c.AckedSeq, err)
		}
		if len(entries) == 0 || entries[0].Seq != c.AckedSeq+1 {
			if err := e.flag(ctx, self, peerID, stream, c.AckedSeq); err != nil {
				return err
			}
			return e.notify(ctx, p, c, self, entries)
		}
		// The flag is read after the entries: see take.
		if _, held, err := e.flagged(ctx, self, peerID, stream); err != nil || held {
			return err
		}
		applied, err := e.Remote.Push(ctx, p, Batch{Origin: self, Stream: stream, FromSeq: c.AckedSeq + 1, Entries: entries})
		var conflict *ConflictError
		switch {
		case errors.As(err, &conflict):
			applied = conflict.Expected - 1
			if applied < 0 || applied > head {
				// The peer claims entries this outbox never held: its state
				// is not this stream's, and only a full sync can say which.
				return e.fail(ctx, c, fmt.Errorf("%w: peer %s expects sequence %d of %s, past its last %d",
					errAhead, peerID, conflict.Expected, stream, head))
			}
		case err != nil:
			return e.fail(ctx, c, err)
		}
		c.AckedSeq, c.LastSent, c.LastError = min(applied, head), e.now(), ""
		if err := e.Store.Outbox().PutCursor(ctx, c); err != nil {
			return fmt.Errorf("store peer %s cursor on %s: %w", peerID, stream, err)
		}
	}
	// More remain: the next run continues, after any other due work.
	return e.enqueue(ctx, SendKind, item{Peer: peerID, Stream: stream})
}

// notify pushes a peer behind the pruned floor the first entry this
// instance still holds of c's stream. The peer refuses it with 409, since
// it expects an earlier one, and that refusal makes it probe and resync. A
// peer that applies it was at the floor: its cursor and flag are brought
// up to date, and the stream is dispatched to it again. No entry is held
// when the peer is behind only an empty stream's floor, and then there is
// nothing to tell.
func (e *Engine) notify(ctx context.Context, p model.Peer, c outbox.PeerCursor, self string, entries []outbox.OutboxEntry) error {
	if len(entries) == 0 {
		return nil
	}
	first := entries[:1]
	applied, err := e.Remote.Push(ctx, p, Batch{Origin: self, Stream: c.Stream, FromSeq: first[0].Seq, Entries: first})
	var conflict *ConflictError
	switch {
	case errors.As(err, &conflict), errors.Is(err, ErrBusy):
		return nil
	case err != nil:
		return e.fail(ctx, c, err)
	}
	c.AckedSeq, c.LastSent, c.LastError = applied, e.now(), ""
	if err := e.Store.Outbox().PutCursor(ctx, c); err != nil {
		return fmt.Errorf("store peer %s cursor on %s: %w", c.PeerInstanceID, c.Stream, err)
	}
	if err := e.Store.Resyncs().Clear(ctx, self, c.PeerInstanceID, c.Stream); err != nil {
		return fmt.Errorf("clear %s's full-sync flag on %s: %w", c.PeerInstanceID, c.Stream, err)
	}
	return e.enqueue(ctx, SendKind, item{Peer: c.PeerInstanceID, Stream: c.Stream})
}

// fail records err on the cursor, keeping its position, and returns it.
func (e *Engine) fail(ctx context.Context, c outbox.PeerCursor, err error) error {
	c.LastError = err.Error()
	if perr := e.Store.Outbox().PutCursor(ctx, c); perr != nil {
		return errors.Join(err, fmt.Errorf("store peer %s cursor: %w", c.PeerInstanceID, perr))
	}
	return fmt.Errorf("push %s to %s: %w", c.Stream, c.PeerInstanceID, err)
}
