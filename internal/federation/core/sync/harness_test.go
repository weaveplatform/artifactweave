package sync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	gosync "sync"
	"testing"
	"time"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

const stream = "test:s"

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// store is one instance's metadata: the federation and platform stores
// over one shared in-memory database.
type store struct {
	*fedinmem.Store
	platform *platforminmem.Store
}

func (s store) Outbox() outbox.OutboxStore       { return s.platform.Outbox() }
func (s store) Instance() instance.InstanceStore { return s.platform.Instance() }
func (s store) Settings() settings.SettingsStore { return s.platform.Settings() }
func (s store) WorkQueue() work.WorkQueue        { return s.platform.WorkQueue() }

// node is one instance: its store, blobs, engine, queue and test kind.
type node struct {
	t      *testing.T
	id     string
	store  store
	blobs  blob.Store
	engine *streamsync.Engine
	queue  *work.Queue
	kind   *streamtest.Kind
	clock  *time.Time
}

// world is the instances, joined by a remote that calls the other
// instance's engine directly, as the peer API would.
type world struct {
	// inject fails the store, blob and kind calls it is armed for.
	inject  *faultSet
	mu      gosync.Mutex
	nodes   map[string]*node
	faults  faults
	pushes  int
	fetches int
	// snapshots counts snapshot pages asked for; onSnapshot, when set, is
	// called with each page's cursor before it is served.
	snapshots  int
	onSnapshot func(cursor string)
	// onPull, when set, is called with each pull's position before it is
	// served.
	onPull func(after int64)
}

// faults are what the remote does wrong. Each is consumed by the call it
// spoils unless sticky.
type faults struct {
	// dropAnswer applies a push, then loses its answer.
	dropAnswer int
	// down refuses every call to the instance.
	down map[string]bool
	// corrupt flips a byte of fetched content; truncate cuts it short.
	corrupt, truncate bool
	// fetchErr fails every blob fetch with itself.
	fetchErr error
}

var errDropped = errors.New("connection dropped after the request was sent")

func newWorld() *world {
	return &world{inject: &faultSet{}, nodes: map[string]*node{}, faults: faults{down: map[string]bool{}}}
}

func (w *world) add(t *testing.T) *node {
	t.Helper()
	shared := memtx.New()
	st := store{Store: fedinmem.New(shared), platform: platforminmem.New(shared)}
	if err := instance.EnsureInstance(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	id := must(st.Instance().Get(t.Context())).InstanceID
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := t0
	q, err := work.New(st, work.Options{
		Workers: 1, Lease: time.Minute, MaxAttempts: 3, Holder: id,
		Backoff: func(int) time.Duration { return 0 },
		// The queue keeps the node's clock, so what the engine queues at
		// its time is due.
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	n := &node{t: t, id: id, store: st, blobs: blobs, queue: q, clock: &clock}
	n.kind = &streamtest.Kind{Store: st}
	reg := &streamsync.Registry{}
	if err := reg.Register(faultKind{n.kind, w.inject}); err != nil {
		t.Fatal(err)
	}
	n.engine = &streamsync.Engine{
		Store: faultStore{Runner: st, st: st, f: w.inject}, Kinds: reg,
		Blobs: faultBlobs{blobs, w.inject}, Remote: remote{w: w, from: n}, Queue: q,
		Now: func() time.Time { return *n.clock },
	}
	if err := n.engine.Register(t.Context(), q, st.WorkQueue()); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.nodes[id] = n
	w.mu.Unlock()
	return n
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// pair records each of a and b as the other's peer, with the use cases and
// the delivery each records for the other, and subscribes both to stream.
func pair(t *testing.T, a, b *node, use []model.UseCase, aRecordsB, bRecordsA model.Delivery) {
	t.Helper()
	put(t, a, b, use, aRecordsB)
	put(t, b, a, use, bRecordsA)
	a.kind.Subscribe(stream, b.id)
	b.kind.Subscribe(stream, a.id)
}

// put records peer on n.
func put(t *testing.T, n, peer *node, use []model.UseCase, d model.Delivery) {
	t.Helper()
	id := must(peer.store.Instance().Get(t.Context()))
	if err := n.store.Peers().Put(t.Context(), model.Peer{
		InstanceID: peer.id, BaseURL: "http://" + peer.id, PublicKey: id.PublicKey, ProtocolVersion: 1,
		UseCases: use, Delivery: d, LastSeen: t0,
	}); err != nil {
		t.Fatal(err)
	}
}

// record is n's record of peer.
func (n *node) record(peer string) model.Peer {
	n.t.Helper()
	p, err := n.store.Peers().Get(n.t.Context(), peer)
	if err != nil {
		n.t.Fatal(err)
	}
	return p
}

// commit makes c on n and appends its entry.
func (n *node) commit(c streamtest.Change) outbox.OutboxEntry {
	n.t.Helper()
	en, err := n.kind.Commit(n.t.Context(), n.engine, stream, c, nil)
	if err != nil {
		n.t.Fatal(err)
	}
	return en
}

func (n *node) put(key, value string) outbox.OutboxEntry {
	return n.commit(streamtest.Change{Type: streamtest.Put, Key: key, Value: value})
}

// run runs n's due items of kind until none is due; a periodic kind,
// which queues its next run at once on the node's stopped clock, runs one
// pass.
func (n *node) run(kind string) {
	n.t.Helper()
	if kind == streamsync.SweepKind || kind == streamsync.PruneKind {
		if _, err := n.queue.RunDue(n.t.Context(), kind); err != nil {
			n.t.Fatal(err)
		}
		return
	}
	for range 20 {
		ran, err := n.queue.RunDue(n.t.Context(), kind)
		if err != nil {
			n.t.Fatal(err)
		}
		if ran == 0 {
			return
		}
	}
	n.t.Fatalf("%s still due after 20 passes", kind)
}

// state is what n holds of origin's stream.
func (n *node) state(origin *node) streamtest.State {
	n.t.Helper()
	s, err := n.kind.State(n.t.Context(), stream, origin.id)
	if err != nil {
		n.t.Fatal(err)
	}
	return s
}

// applied is the sequences n applied from origin, in the order applied.
func (n *node) applied(origin *node) []string { return n.state(origin).Applied }

func (n *node) cursor(peer *node) int64 {
	n.t.Helper()
	c, err := n.store.Outbox().Cursor(n.t.Context(), peer.id, stream)
	if errors.Is(err, kernel.ErrNotFound) {
		return 0
	}
	if err != nil {
		n.t.Fatal(err)
	}
	return c.AckedSeq
}

func (n *node) resyncs() []model.StreamResync {
	n.t.Helper()
	return must(n.store.Resyncs().Resyncs(n.t.Context(), ""))
}

func (n *node) failed() []outbox.FailedBlobFetch {
	n.t.Helper()
	return must(n.store.Outbox().FailedFetches(n.t.Context(), stream))
}

func seqs(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

func wantApplied(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
}

// remote calls the other node's engine as the peer API would: as the
// caller the receiver's record of from names.
type remote struct {
	w    *world
	from *node
}

func (r remote) target(peer model.Peer) (*node, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	to := r.w.nodes[peer.InstanceID]
	if to == nil || r.w.faults.down[peer.InstanceID] {
		return nil, fmt.Errorf("peer %s: connection refused", peer.InstanceID)
	}
	return to, nil
}

// caller is the receiver's record of the calling instance; an instance it
// has no record of is answered 401, as the signature middleware would.
func caller(to *node, from string) (model.Peer, error) {
	p, err := to.store.Peers().Get(context.Background(), from)
	if err != nil {
		return model.Peer{}, fmt.Errorf("401: %w", err)
	}
	return p, nil
}

func (r remote) Push(ctx context.Context, peer model.Peer, b streamsync.Batch) (int64, error) {
	to, err := r.target(peer)
	if err != nil {
		return 0, err
	}
	c, err := caller(to, r.from.id)
	if err != nil {
		return 0, err
	}
	// The wire carries no origin or stream per entry.
	entries := make([]outbox.OutboxEntry, len(b.Entries))
	for i, en := range b.Entries {
		en.Origin, en.Stream = "", ""
		entries[i] = en
	}
	b.Entries = entries
	applied, err := to.engine.Receive(ctx, c, b)
	r.w.mu.Lock()
	r.w.pushes++
	drop := r.w.faults.dropAnswer > 0
	if drop {
		r.w.faults.dropAnswer--
	}
	r.w.mu.Unlock()
	if drop {
		return 0, errDropped
	}
	return applied, err
}

func (r remote) Pull(ctx context.Context, peer model.Peer, s string, after int64, limit int) (streamsync.Page, error) {
	to, err := r.target(peer)
	if err != nil {
		return streamsync.Page{}, err
	}
	c, err := caller(to, r.from.id)
	if err != nil {
		return streamsync.Page{}, err
	}
	r.w.mu.Lock()
	hook := r.w.onPull
	r.w.mu.Unlock()
	if hook != nil {
		hook(after)
	}
	return to.engine.Events(ctx, c, s, after, limit)
}

func (r remote) Snapshot(ctx context.Context, peer model.Peer, s, cursor string, limit int) (streamsync.SnapshotPage, error) {
	to, err := r.target(peer)
	if err != nil {
		return streamsync.SnapshotPage{}, err
	}
	c, err := caller(to, r.from.id)
	if err != nil {
		return streamsync.SnapshotPage{}, err
	}
	r.w.mu.Lock()
	r.w.snapshots++
	hook := r.w.onSnapshot
	r.w.mu.Unlock()
	if hook != nil {
		hook(cursor)
	}
	return to.engine.Snapshot(ctx, c, s, cursor, limit)
}

func (r remote) FetchBlob(ctx context.Context, peer model.Peer, sha256 string, into func(io.Reader) error) error {
	to, err := r.target(peer)
	if err != nil {
		return err
	}
	r.w.mu.Lock()
	r.w.fetches++
	f := r.w.faults
	r.w.mu.Unlock()
	if f.fetchErr != nil {
		return f.fetchErr
	}
	c, err := caller(to, r.from.id)
	if err != nil {
		return err
	}
	rc, _, err := to.engine.Blob(ctx, c, sha256)
	if err != nil {
		return fmt.Errorf("404: %w", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	switch {
	case f.corrupt:
		body[0] ^= 0xff
	case f.truncate:
		body = body[:len(body)-1]
	}
	return into(bytes.NewReader(body))
}
