package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Snapshot serves the peer API's GET streams/{stream}/snapshot: a page of
// up to limit items of this instance's stream, in order of key, as they
// were at the snapshot's sequence. Without a cursor it takes a new snapshot
// for caller, replacing caller's earlier one of the stream; with one it
// continues the snapshot the cursor names. ErrNoStream or ErrRefused when
// caller may not receive the stream from here; ErrNoSnapshot when the
// stream's kind takes none; kernel.ErrInvalid for a limit outside 1 to
// MaxBatch, or a cursor that is malformed or was issued for another stream
// or caller; ErrGone when the cursor's snapshot is no longer kept.
func (e *Engine) Snapshot(ctx context.Context, caller model.Peer, stream, cursor string, limit int) (SnapshotPage, error) {
	k, err := e.authorize(ctx, caller, stream, Send)
	if err != nil {
		return SnapshotPage{}, err
	}
	if limit < 1 || limit > MaxBatch {
		return SnapshotPage{}, fmt.Errorf("limit must be 1 to %d: %w", MaxBatch, kernel.ErrInvalid)
	}
	sp, ok := k.(SnapshotProvider)
	if !ok {
		return SnapshotPage{}, ErrNoSnapshot
	}
	self, err := e.self(ctx)
	if err != nil {
		return SnapshotPage{}, err
	}
	var (
		sn    model.StreamSnapshot
		after string
	)
	if cursor == "" {
		if sn, err = e.take(ctx, self, caller.InstanceID, stream, sp); err != nil {
			return SnapshotPage{}, err
		}
	} else {
		c, ok := parseCursor(cursor)
		if !ok {
			return SnapshotPage{}, fmt.Errorf("the cursor is malformed: %w", kernel.ErrInvalid)
		}
		sn, err = e.Store.Snapshots().Snapshot(ctx, c.ID)
		if errors.Is(err, kernel.ErrNotFound) {
			return SnapshotPage{}, ErrGone
		}
		if err != nil {
			return SnapshotPage{}, fmt.Errorf("read snapshot of %s: %w", stream, err)
		}
		if sn.Receiver != caller.InstanceID || sn.Stream != stream {
			return SnapshotPage{}, fmt.Errorf("the cursor belongs to another stream or caller: %w", kernel.ErrInvalid)
		}
		after = c.After
	}
	// One more than the page says whether another follows.
	entries, err := e.Store.Snapshots().SnapshotEntries(ctx, sn.ID, after, limit+1)
	if err != nil {
		return SnapshotPage{}, fmt.Errorf("read snapshot of %s: %w", stream, err)
	}
	page := SnapshotPage{Origin: self, Stream: stream, Sequence: sn.Sequence, Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		page.NextCursor = snapshotCursor{ID: sn.ID, After: entries[limit-1].Key}.String()
	}
	if err := e.Store.Snapshots().ReadSnapshot(ctx, sn.ID, e.now()); errors.Is(err, kernel.ErrNotFound) {
		// Pruned while this page was read: its next cursor would be gone.
		return SnapshotPage{}, ErrGone
	} else if err != nil {
		return SnapshotPage{}, fmt.Errorf("read snapshot of %s: %w", stream, err)
	}
	return page, nil
}

// take copies stream's items as they are now, with the stream's sequence,
// into a snapshot for receiver, a page at a time so memory stays bounded.
//
// The receiver is flagged first, in a transaction of its own, so a
// dispatcher stops pushing to it: a dispatcher reads its entries and then
// the flag, and one that saw no flag read only entries committed before
// the sequence below was read. Nothing the receiver applies from a push
// while it resynchronises is therefore newer than the snapshot, which
// lets a forced full sync replace whatever the receiver holds.
//
// The sequence is read before the items, so the copy holds every change
// up to it. On PostgreSQL a change committed while the copy runs may show
// in it as well; the receiver then applies that change's entry again
// after the snapshot, which changes nothing, so the stream still
// converges.
func (e *Engine) take(ctx context.Context, self, receiver, stream string, sp SnapshotProvider) (model.StreamSnapshot, error) {
	c, err := e.cursor(ctx, receiver, stream)
	if err != nil {
		return model.StreamSnapshot{}, err
	}
	if err := e.flag(ctx, self, receiver, stream, c.AckedSeq); err != nil {
		return model.StreamSnapshot{}, err
	}
	var sn model.StreamSnapshot
	err = e.Store.Tx(ctx, func(ctx context.Context) error {
		head, err := e.Store.Outbox().LastSeq(ctx, self, stream)
		if err != nil {
			return fmt.Errorf("stream %s head: %w", stream, err)
		}
		now := e.now()
		sn, err = e.Store.Snapshots().CreateSnapshot(ctx, model.StreamSnapshot{
			Stream: stream, Receiver: receiver, Sequence: head, Taken: now, Read: now,
		})
		if err != nil {
			return fmt.Errorf("take snapshot of %s: %w", stream, err)
		}
		after := ""
		for {
			items, err := sp.SnapshotItems(ctx, self, stream, after, MaxBatch)
			if err != nil {
				return fmt.Errorf("list %s for a snapshot: %w", stream, err)
			}
			if err := validSnapshot(after, items); err != nil {
				return fmt.Errorf("list %s for a snapshot: %w", stream, err)
			}
			if len(items) == 0 {
				return nil
			}
			if err := e.Store.Snapshots().AddSnapshotEntries(ctx, sn.ID, items); err != nil {
				return fmt.Errorf("take snapshot of %s: %w", stream, err)
			}
			if len(items) < MaxBatch {
				return nil
			}
			after = items[len(items)-1].Key
		}
	})
	if err != nil {
		return model.StreamSnapshot{}, err //nolint:wrapcheck // the callback names its failures
	}
	return sn, nil
}

// validSnapshot checks a page of snapshot entries: keys in strictly
// increasing byte order after after, and each entry one the wire carries.
func validSnapshot(after string, entries []model.SnapshotEntry) error {
	if err := ordered(after, entries); err != nil {
		return err
	}
	for _, en := range entries {
		if err := validSnapshotEntry(en); err != nil {
			return fmt.Errorf("snapshot entry %q: %w", en.Key, err)
		}
	}
	return nil
}

// ordered checks that keys are not empty and increase, in byte order,
// after after: the diff walks the snapshot and the receiver's items side
// by side, and an item out of order would be taken for one the other side
// lacks.
func ordered(after string, entries []model.SnapshotEntry) error {
	prev := after
	for i, en := range entries {
		if en.Key == "" || en.Key <= prev {
			return fmt.Errorf("item %d: keys must increase after %q: %w", i, prev, kernel.ErrInvalid)
		}
		prev = en.Key
	}
	return nil
}

// validSnapshotEntry checks what the wire carries: an origin, a payload
// that is a JSON object, and a blob reference of 64 lowercase hex digits
// with a size that is not negative; a tombstone has neither.
func validSnapshotEntry(en model.SnapshotEntry) error {
	switch {
	case en.Origin == "":
		return fmt.Errorf("an entry needs the origin of its change: %w", kernel.ErrInvalid)
	case len(en.Payload) > 0 && !jsonObject(en.Payload):
		return fmt.Errorf("an entry's payload must be a JSON object: %w", kernel.ErrInvalid)
	case en.BlobSHA256 != "" && !sha256Hex.MatchString(en.BlobSHA256):
		return fmt.Errorf("an entry's blob must be 64 lowercase hex digits: %w", kernel.ErrInvalid)
	case en.BlobSize < 0, en.BlobSHA256 == "" && en.BlobSize != 0:
		return fmt.Errorf("an entry's blob size must be a size of its blob: %w", kernel.ErrInvalid)
	case en.Deleted && (len(en.Payload) > 0 || en.BlobSHA256 != ""):
		return fmt.Errorf("a tombstone has no payload or blob: %w", kernel.ErrInvalid)
	}
	return nil
}

// snapshotCursor is a snapshot page's next_cursor: the snapshot and the
// last key served. It is opaque to the caller, who may not edit it; one
// that names another caller's snapshot is refused by the snapshot's
// receiver, so guessing ids reads nothing.
type snapshotCursor struct {
	ID    int64  `json:"s"`
	After string `json:"k"`
}

func (c snapshotCursor) String() string {
	b, _ := json.Marshal(c) //nolint:errchkjson // an integer and a string
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseCursor(s string) (snapshotCursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return snapshotCursor{}, false
	}
	var c snapshotCursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID <= 0 {
		return snapshotCursor{}, false
	}
	return c, true
}
