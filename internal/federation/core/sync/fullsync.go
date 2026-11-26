package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// errRunMoved reports a run that changed while a page was applied: a
// forced request restarted it, or it ended. The page is applied again from
// what the run now says.
var errRunMoved = errors.New("the full-sync run changed")

// FullSync starts a full sync of peerID's stream on this instance, the
// operator's entry point FED-04's fullSync and forceFullSync call, and
// returns its run. While one runs, it is returned instead; forced makes it
// forced, from the start of a new snapshot, since the pages it applied in
// normal mode kept what a forced run discards. kernel.ErrNotFound for a
// peer with no record; ErrNoStream or ErrRefused when this instance does
// not receive the stream from the peer; ErrNoSnapshot when the stream's
// kind takes none.
func (e *Engine) FullSync(ctx context.Context, peerID, stream string, forced bool) (model.FullSyncRun, error) {
	p, ok, err := e.peer(ctx, peerID)
	if err != nil {
		return model.FullSyncRun{}, err
	}
	if !ok {
		return model.FullSyncRun{}, fmt.Errorf("peer %s: %w", peerID, kernel.ErrNotFound)
	}
	k, err := e.authorize(ctx, p, stream, Receive)
	if err != nil {
		return model.FullSyncRun{}, err
	}
	if _, ok := k.(Reconciler); !ok {
		return model.FullSyncRun{}, ErrNoSnapshot
	}
	return e.startResync(ctx, peerID, stream, forced)
}

// startResync flags peerID's stream on this instance for a full sync and
// starts its run, or returns the run already going, made forced when
// forced is, and queues the run's work, in one transaction.
func (e *Engine) startResync(ctx context.Context, peerID, stream string, forced bool) (model.FullSyncRun, error) {
	self, err := e.self(ctx)
	if err != nil {
		return model.FullSyncRun{}, err
	}
	var run model.FullSyncRun
	err = e.Store.Tx(ctx, func(ctx context.Context) error {
		f, ok, err := e.flagged(ctx, peerID, self, stream)
		if err != nil {
			return err
		}
		if !ok {
			m, err := e.Store.Outbox().Applied(ctx, peerID, stream)
			if err != nil {
				return fmt.Errorf("applied position on %s: %w", stream, err)
			}
			f = model.StreamResync{Origin: peerID, Receiver: self, Stream: stream, Position: m.LastSeq, Flagged: e.now()}
		}
		if f.Run != 0 {
			r, err := e.Store.Federation().FullSync(ctx, f.Run)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("full sync %d: %w", f.Run, err)
			}
			if err == nil && !r.Status.Terminal() {
				if forced && !r.Forced {
					r.Forced, r.Phase, r.Cursor, r.LastKey = true, model.FullSyncSnapshot, "", ""
					if err := e.Store.Federation().UpdateFullSync(ctx, r); err != nil {
						return fmt.Errorf("full sync %d: %w", r.ID, err)
					}
				}
				run = r
				return e.enqueue(ctx, FullSyncKind, item{Peer: peerID, Stream: stream})
			}
		}
		r := model.FullSyncRun{
			PeerInstanceID: peerID, Stream: stream, Forced: forced, Started: e.now(),
			Status: retentionapi.RunInProgress, Phase: model.FullSyncSnapshot,
		}
		if kind, rest, err := model.ParseStream(stream); err == nil && kind == model.StreamRepository {
			r.RepoKey = rest
		}
		if r, err = e.Store.Federation().CreateFullSync(ctx, r); err != nil {
			return fmt.Errorf("start a full sync of %s: %w", stream, err)
		}
		f.Run = r.ID
		if err := e.Store.Resyncs().Flag(ctx, f); err != nil {
			return fmt.Errorf("flag %s for a full sync: %w", stream, err)
		}
		run = r
		return e.enqueue(ctx, FullSyncKind, item{Peer: peerID, Stream: stream})
	})
	if err != nil {
		return model.FullSyncRun{}, err //nolint:wrapcheck // the callback names its failures
	}
	return run, nil
}

// Resync runs peerID's stream's full sync on this instance (FullSyncKind):
// a few snapshot pages, or pulls after the snapshot, then the next run
// continues where the run's record says, a restart included. A stream that
// is not flagged is probed: a peer that answers 410 to a pull from this
// instance's position starts the full sync, which is how a receiver the
// origin can no longer push to learns that it must resync.
func (e *Engine) Resync(ctx context.Context, peerID, stream string) error {
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	f, ok, err := e.flagged(ctx, peerID, self, stream)
	if err != nil {
		return err
	}
	p, known, err := e.peer(ctx, peerID)
	if err != nil {
		return err
	}
	var k Kind
	if known {
		k, err = e.authorize(ctx, p, stream, Receive)
		if err != nil && !errors.Is(err, ErrNoStream) && !errors.Is(err, ErrRefused) {
			return err
		}
	}
	if !ok {
		if k == nil {
			return nil
		}
		return e.probe(ctx, p, stream)
	}
	rc, _ := k.(Reconciler)
	if k == nil || rc == nil {
		// Nothing to resync from: the trust or subscription has ended, or
		// the kind takes no snapshots. The flag goes, so the stream is not
		// held back for a run that cannot happen.
		return e.stop(ctx, f, "this instance no longer receives the stream from the peer by snapshot")
	}
	run, err := e.currentRun(ctx, f)
	if err != nil {
		return err
	}
	for range runBatches {
		var done bool
		if run.Phase == model.FullSyncStream {
			done, err = e.catchUp(ctx, p, k, &run)
		} else {
			err = e.snapshotPage(ctx, p, rc, &run)
		}
		if errors.Is(err, errRunMoved) {
			if run, err = e.currentRun(ctx, f); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return e.note(ctx, run, err)
		}
		if done {
			return e.complete(ctx, run)
		}
	}
	return e.enqueue(ctx, FullSyncKind, item{Peer: peerID, Stream: stream})
}

// probe asks peer for its entries after this instance's position, one at
// most, and starts a full sync when they are gone. Nothing is applied: a
// peer that pushes sends them itself.
func (e *Engine) probe(ctx context.Context, p model.Peer, stream string) error {
	m, err := e.Store.Outbox().Applied(ctx, p.InstanceID, stream)
	if err != nil {
		return fmt.Errorf("applied position on %s: %w", stream, err)
	}
	_, err = e.Remote.Pull(ctx, p, stream, m.LastSeq, 1)
	if errors.Is(err, ErrGone) {
		_, err = e.startResync(ctx, p.InstanceID, stream, false)
		return err
	}
	if err != nil {
		return fmt.Errorf("probe %s at %s: %w", stream, p.InstanceID, err)
	}
	return nil
}

// currentRun is the run f names, or a new one when it names none that is
// still going.
func (e *Engine) currentRun(ctx context.Context, f model.StreamResync) (model.FullSyncRun, error) {
	if f.Run != 0 {
		r, err := e.Store.Federation().FullSync(ctx, f.Run)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return model.FullSyncRun{}, fmt.Errorf("full sync %d: %w", f.Run, err)
		}
		if err == nil && !r.Status.Terminal() {
			return r, nil
		}
	}
	return e.startResync(ctx, f.Origin, f.Stream, false)
}

// snapshotPage reads the run's next snapshot page from p and applies it.
// Content the page refers to is fetched before the apply transaction; the
// transaction applies the differences and stores the run's position, so a
// page applies exactly once even across a restart. After the last page the
// applied position becomes the snapshot's sequence and the run moves on
// to the stream.
func (e *Engine) snapshotPage(ctx context.Context, p model.Peer, rc Reconciler, run *model.FullSyncRun) error {
	page, err := e.Remote.Snapshot(ctx, p, run.Stream, run.Cursor, e.batch())
	if errors.Is(err, ErrGone) || err == nil && run.Cursor != "" && page.Sequence != run.SnapshotSeq {
		// The snapshot is gone, or another replaced it: start over, which
		// applies again only what differs.
		return e.restart(ctx, run)
	}
	if err != nil {
		return fmt.Errorf("snapshot of %s from %s: %w", run.Stream, p.InstanceID, err)
	}
	lower := ""
	if run.Cursor != "" {
		lower = run.LastKey
	}
	if err := validSnapshot(lower, page.Entries); err != nil {
		return fmt.Errorf("snapshot of %s from %s: %w", run.Stream, p.InstanceID, err)
	}
	last := page.NextCursor == ""
	if !last && len(page.Entries) == 0 {
		return fmt.Errorf("snapshot of %s from %s: %w: a page before the last has no entries",
			run.Stream, p.InstanceID, kernel.ErrInvalid)
	}
	ops, err := e.diff(ctx, rc, p.InstanceID, run.Stream, run.Forced, lower, last, page.Entries)
	if err != nil {
		return err
	}
	fetched, err := e.fetchPage(ctx, p, ops)
	if err != nil {
		return err
	}
	next := *run
	next.SnapshotSeq, next.Cursor = page.Sequence, page.NextCursor
	if n := len(page.Entries); n > 0 {
		next.LastKey = page.Entries[n-1].Key
	}
	if last {
		next.Phase, next.Cursor, next.LastKey = model.FullSyncStream, "", ""
	}
	err = e.Store.Tx(ctx, func(ctx context.Context) error {
		if err := e.unmoved(ctx, *run); err != nil {
			return err
		}
		if _, err := e.regrant(ctx, p.InstanceID, run.Stream, Receive); err != nil {
			return err
		}
		// Decided again inside the transaction: the receiver's own writes,
		// and other origins' entries, may have changed an item since.
		ops, err := e.diff(ctx, rc, p.InstanceID, run.Stream, run.Forced, lower, last, page.Entries)
		if err != nil {
			return err
		}
		failed, err := e.applyPage(ctx, p.InstanceID, run.Stream, rc, ops, fetched)
		if err != nil {
			return err
		}
		if failed {
			next.Status = retentionapi.RunInProgressWithErrors
		}
		if last {
			if err := e.resumeAt(ctx, p.InstanceID, run.Stream, page.Sequence); err != nil {
				return err
			}
		}
		next.Message = ""
		if err := e.Store.Federation().UpdateFullSync(ctx, next); err != nil {
			return fmt.Errorf("full sync %d: %w", run.ID, err)
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the callback names its failures
	}
	*run = next
	return nil
}

// fetched is, per digest a snapshot page refers to, whether the receiver
// now holds the content, and if not why.
type fetched map[string]blobFetch

type blobFetch struct {
	held   bool
	reason string
}

// fetchPage fetches the content ops refer to from p, once per digest,
// outside any transaction.
func (e *Engine) fetchPage(ctx context.Context, p model.Peer, ops []op) (fetched, error) {
	out := fetched{}
	for _, o := range ops {
		sha := o.entry.BlobSHA256
		if o.discard || sha == "" {
			continue
		}
		if _, seen := out[sha]; seen {
			continue
		}
		ok, reason, err := e.ensureBlob(ctx, []model.Peer{p}, sha, o.entry.BlobSize)
		if err != nil {
			return nil, err
		}
		out[sha] = blobFetch{held: ok, reason: reason}
	}
	return out, nil
}

// applyPage applies ops to origin's stream in the caller's transaction.
// An item whose content is not held applies pending, with a failed binary
// and its retry; failed is true when one did.
func (e *Engine) applyPage(
	ctx context.Context, origin, stream string, rc Reconciler, ops []op, got fetched,
) (failed bool, err error) {
	for _, o := range ops {
		if o.discard {
			if err := rc.Discard(ctx, origin, stream, o.key); err != nil {
				return false, fmt.Errorf("discard %s of %s: %w", o.key, stream, err)
			}
			continue
		}
		e.observe(o.entry.Time)
		sha := o.entry.BlobSHA256
		ok := sha == "" || got[sha].held
		if err := rc.Reconcile(ctx, origin, stream, o.entry, ok); err != nil {
			return false, fmt.Errorf("reconcile %s of %s: %w", o.key, stream, err)
		}
		if ok {
			continue
		}
		reason := got[sha].reason
		if reason == "" {
			// Not in the diff before the transaction: the item changed
			// since, so its content was never asked for.
			reason = "the item changed while its snapshot page was applied"
		}
		if err := e.recordFailure(ctx, stream, sha, reason); err != nil {
			return false, err
		}
		if err := e.enqueue(ctx, FetchKind, item{Stream: stream, SHA256: sha}); err != nil {
			return false, err
		}
		failed = true
	}
	return failed, nil
}

// resumeAt makes seq origin's applied position on stream, so the stream
// resumes after the snapshot's sequence; a position already past it, which
// nothing here produces, is kept.
func (e *Engine) resumeAt(ctx context.Context, origin, stream string, seq int64) error {
	m, err := e.Store.Outbox().LockApplied(ctx, origin, stream)
	if err != nil {
		return fmt.Errorf("applied position on %s: %w", stream, err)
	}
	if m.LastSeq >= seq {
		return nil
	}
	m.LastSeq = seq
	if err := e.Store.Outbox().SetApplied(ctx, m); err != nil {
		return fmt.Errorf("record applied position on %s: %w", stream, err)
	}
	return nil
}

// catchUp applies p's entries after the snapshot's sequence, a page per
// call, until none remain. Its first pull also acknowledges the position
// on p, which clears p's flag, so p sends again from there. done is true
// once the stream is caught up.
func (e *Engine) catchUp(ctx context.Context, p model.Peer, k Kind, run *model.FullSyncRun) (bool, error) {
	m, err := e.Store.Outbox().Applied(ctx, p.InstanceID, run.Stream)
	if err != nil {
		return false, fmt.Errorf("applied position on %s: %w", run.Stream, err)
	}
	page, err := e.Remote.Pull(ctx, p, run.Stream, m.LastSeq, e.batch())
	if errors.Is(err, ErrGone) {
		// Pruned again before this instance caught up: another snapshot.
		return false, e.restart(ctx, run)
	}
	if err != nil {
		return false, fmt.Errorf("pull %s from %s: %w", run.Stream, p.InstanceID, err)
	}
	if len(page.Entries) > 0 {
		if err := consecutive(m.Expect(), page.Entries); err != nil {
			return false, fmt.Errorf("pull %s from %s: %w", run.Stream, p.InstanceID, err)
		}
		if err := e.applyFrom(ctx, p, k, run.Stream, page.Entries, true); err != nil {
			return false, err
		}
	}
	return page.NextAfter == 0, nil
}

// restart sends the run back to the start of a new snapshot.
func (e *Engine) restart(ctx context.Context, run *model.FullSyncRun) error {
	next := *run
	next.Phase, next.Cursor, next.LastKey = model.FullSyncSnapshot, "", ""
	err := e.Store.Tx(ctx, func(ctx context.Context) error {
		if err := e.unmoved(ctx, *run); err != nil {
			return err
		}
		if err := e.Store.Federation().UpdateFullSync(ctx, next); err != nil {
			return fmt.Errorf("full sync %d: %w", run.ID, err)
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the callback names its failures
	}
	*run = next
	return nil
}

// unmoved refuses, with errRunMoved, a run whose stored position is no
// longer run's.
func (e *Engine) unmoved(ctx context.Context, run model.FullSyncRun) error {
	cur, err := e.Store.Federation().FullSync(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("full sync %d: %w", run.ID, err)
	}
	if cur.Status.Terminal() || cur.Forced != run.Forced || cur.Phase != run.Phase || cur.Cursor != run.Cursor {
		return errRunMoved
	}
	return nil
}

// complete records the run's end and clears the stream's flag, in one
// transaction, so the origin's pushes apply again.
func (e *Engine) complete(ctx context.Context, run model.FullSyncRun) error {
	err := e.Store.Tx(ctx, func(ctx context.Context) error {
		if err := e.unmoved(ctx, run); err != nil {
			return err
		}
		status := retentionapi.RunCompleted
		if run.Status == retentionapi.RunInProgressWithErrors {
			// A content fetch failed: its failed binary is retried, and
			// the run says so.
			status = retentionapi.RunCompletedWithErrors
		}
		run.Status = status
		run.Finished, run.Message = e.now(), ""
		if err := e.Store.Federation().UpdateFullSync(ctx, run); err != nil {
			return fmt.Errorf("full sync %d: %w", run.ID, err)
		}
		self, err := e.self(ctx)
		if err != nil {
			return err
		}
		if err := e.Store.Resyncs().Clear(ctx, run.PeerInstanceID, self, run.Stream); err != nil {
			return fmt.Errorf("clear the full-sync flag of %s: %w", run.Stream, err)
		}
		return nil
	})
	if errors.Is(err, errRunMoved) {
		// Restarted forced meanwhile: the next run of the work goes on.
		return e.enqueue(ctx, FullSyncKind, item{Peer: run.PeerInstanceID, Stream: run.Stream})
	}
	return err //nolint:wrapcheck // the callback names its failures
}

// stop ends f's run, if one is going, as failed with why, and clears f.
func (e *Engine) stop(ctx context.Context, f model.StreamResync, why string) error {
	return e.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the callback names its failures
		if f.Run != 0 {
			r, err := e.Store.Federation().FullSync(ctx, f.Run)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("full sync %d: %w", f.Run, err)
			}
			if err == nil && !r.Status.Terminal() {
				r.Status, r.Finished, r.Message = retentionapi.RunFailed, e.now(), why
				if err := e.Store.Federation().UpdateFullSync(ctx, r); err != nil {
					return fmt.Errorf("full sync %d: %w", r.ID, err)
				}
			}
		}
		if err := e.Store.Resyncs().Clear(ctx, f.Origin, f.Receiver, f.Stream); err != nil {
			return fmt.Errorf("clear the full-sync flag of %s: %w", f.Stream, err)
		}
		return nil
	})
}

// note records err on the run, for FED-05 to show, and returns it, so the
// queue retries with back-off.
func (e *Engine) note(ctx context.Context, run model.FullSyncRun, err error) error {
	cur, rerr := e.Store.Federation().FullSync(ctx, run.ID)
	if rerr == nil && !cur.Status.Terminal() {
		cur.Message = err.Error()
		rerr = e.Store.Federation().UpdateFullSync(ctx, cur)
	}
	if rerr != nil {
		return errors.Join(err, fmt.Errorf("full sync %d: %w", run.ID, rerr))
	}
	return err
}

// op is one difference a snapshot page makes on the receiver: an entry to
// reconcile, or, in a forced run, a key of the receiver's to discard.
type op struct {
	discard bool
	key     string
	entry   model.SnapshotEntry
}

// diff compares a snapshot page with the receiver's items of origin's
// stream whose keys fall after lower and, unless the page is the last, up
// to its last key. An entry the receiver lacks is applied, a tombstone
// included, so an older change cannot restore what it deleted. Where both
// hold the key, the entry is applied when the conflict rule says it wins,
// or, in a forced run, whenever it is another change than the receiver's.
// A key only the receiver holds is kept, except by a forced run, which
// discards it. The receiver's items are read a page at a time; the ops
// hold the page's entries and, in a forced run, the keys discarded.
func (e *Engine) diff(
	ctx context.Context, rc Reconciler, origin, stream string, forced bool, lower string, last bool,
	entries []model.SnapshotEntry,
) ([]op, error) {
	local := &localItems{rc: rc, origin: origin, stream: stream, after: lower, limit: e.batch()}
	var ops []op
	onlyLocal := func(l model.SnapshotEntry) {
		if forced {
			ops = append(ops, op{discard: true, key: l.Key})
		}
	}
	for _, s := range entries {
		var (
			l     model.SnapshotEntry
			match bool
		)
		for {
			cur, ok, err := local.peek(ctx)
			if err != nil {
				return nil, err
			}
			if !ok || cur.Key > s.Key {
				break
			}
			local.next()
			if cur.Key == s.Key {
				l, match = cur, true
				break
			}
			onlyLocal(cur)
		}
		switch {
		case !match:
			ops = append(ops, op{key: s.Key, entry: s})
		case l.Stamp() == s.Stamp() && l.Deleted == s.Deleted && !l.Pending:
			// The same change: nothing to do, in either mode.
		case forced || s.Stamp().After(l.Stamp()) || l.Pending && l.Stamp() == s.Stamp():
			ops = append(ops, op{key: s.Key, entry: s})
		}
	}
	if last {
		for {
			cur, ok, err := local.peek(ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			local.next()
			onlyLocal(cur)
		}
	}
	return ops, nil
}

// localItems reads the receiver's items of a stream in key order, a page
// at a time.
type localItems struct {
	rc             Reconciler
	origin, stream string
	after          string
	limit          int
	buf            []model.SnapshotEntry
	done           bool
}

func (l *localItems) peek(ctx context.Context) (model.SnapshotEntry, bool, error) {
	if len(l.buf) == 0 && !l.done {
		items, err := l.rc.LocalItems(ctx, l.origin, l.stream, l.after, l.limit)
		if err != nil {
			return model.SnapshotEntry{}, false, fmt.Errorf("items of %s: %w", l.stream, err)
		}
		if err := ordered(l.after, items); err != nil {
			return model.SnapshotEntry{}, false, fmt.Errorf("items of %s: %w", l.stream, err)
		}
		l.buf, l.done = items, len(items) < l.limit
		if n := len(items); n > 0 {
			l.after = items[n-1].Key
		}
	}
	if len(l.buf) == 0 {
		return model.SnapshotEntry{}, false, nil
	}
	return l.buf[0], true, nil
}

func (l *localItems) next() { l.buf = l.buf[1:] }
