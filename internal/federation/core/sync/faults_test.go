package sync_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

var errBoom = errors.New("the store failed")

// faultSet fails the calls named name after the first skip of them, with
// errBoom, and counts how many it failed.
type faultSet struct {
	mu   gosync.Mutex
	name string
	skip int
	hits int
}

func (f *faultSet) arm(name string, skip int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name, f.skip, f.hits = name, skip, 0
}

func (f *faultSet) check(name string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != f.name {
		return nil
	}
	if f.skip > 0 {
		f.skip--
		return nil
	}
	f.hits++
	return errBoom
}

func (f *faultSet) hit() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

// faultStore is a node's store as the engine sees it, failing where f
// says.
type faultStore struct {
	tx.Runner
	st store
	f  *faultSet
}

func (s faultStore) Peers() port.PeerStore            { return faultPeers{s.st.Peers(), s.f} }
func (s faultStore) Outbox() outbox.OutboxStore       { return faultOutbox{s.st.Outbox(), s.f} }
func (s faultStore) Resyncs() port.StreamResyncStore  { return faultResyncs{s.st.Resyncs(), s.f} }
func (s faultStore) Instance() instance.InstanceStore { return faultInstance{s.st.Instance(), s.f} }

type faultPeers struct {
	port.PeerStore
	f *faultSet
}

func (p faultPeers) Get(ctx context.Context, id string) (model.Peer, error) {
	if err := p.f.check("Peers.Get"); err != nil {
		return model.Peer{}, err
	}
	return p.PeerStore.Get(ctx, id) //nolint:wrapcheck // passed through
}

type faultResyncs struct {
	port.StreamResyncStore
	f *faultSet
}

func (r faultResyncs) Flag(ctx context.Context, v model.StreamResync) error {
	if err := r.f.check("Resyncs.Flag"); err != nil {
		return err
	}
	return r.StreamResyncStore.Flag(ctx, v) //nolint:wrapcheck // passed through
}

func (r faultResyncs) Clear(ctx context.Context, origin, receiver, s string) error {
	if err := r.f.check("Resyncs.Clear"); err != nil {
		return err
	}
	return r.StreamResyncStore.Clear(ctx, origin, receiver, s) //nolint:wrapcheck // passed through
}

type faultInstance struct {
	instance.InstanceStore
	f *faultSet
}

func (i faultInstance) ID(ctx context.Context) (string, error) {
	if err := i.f.check("Instance.ID"); err != nil {
		return "", err
	}
	return i.InstanceStore.ID(ctx) //nolint:wrapcheck // passed through
}

type faultOutbox struct {
	outbox.OutboxStore
	f *faultSet
}

func (o faultOutbox) Append(ctx context.Context, e outbox.OutboxEntry) (outbox.OutboxEntry, error) {
	if err := o.f.check("Outbox.Append"); err != nil {
		return outbox.OutboxEntry{}, err
	}
	return o.OutboxStore.Append(ctx, e) //nolint:wrapcheck // passed through
}

func (o faultOutbox) Read(ctx context.Context, origin, s string, after int64, limit int) ([]outbox.OutboxEntry, error) {
	if err := o.f.check("Outbox.Read"); err != nil {
		return nil, err
	}
	return o.OutboxStore.Read(ctx, origin, s, after, limit) //nolint:wrapcheck // passed through
}

func (o faultOutbox) LastSeq(ctx context.Context, origin, s string) (int64, error) {
	if err := o.f.check("Outbox.LastSeq"); err != nil {
		return 0, err
	}
	return o.OutboxStore.LastSeq(ctx, origin, s) //nolint:wrapcheck // passed through
}

func (o faultOutbox) Prune(ctx context.Context, origin, s string, upTo int64) (int64, error) {
	if err := o.f.check("Outbox.Prune"); err != nil {
		return 0, err
	}
	return o.OutboxStore.Prune(ctx, origin, s, upTo) //nolint:wrapcheck // passed through
}

func (o faultOutbox) PutCursor(ctx context.Context, c outbox.PeerCursor) error {
	if err := o.f.check("Outbox.PutCursor"); err != nil {
		return err
	}
	return o.OutboxStore.PutCursor(ctx, c) //nolint:wrapcheck // passed through
}

func (o faultOutbox) Cursor(ctx context.Context, peer, s string) (outbox.PeerCursor, error) {
	if err := o.f.check("Outbox.Cursor"); err != nil {
		return outbox.PeerCursor{}, err
	}
	return o.OutboxStore.Cursor(ctx, peer, s) //nolint:wrapcheck // passed through
}

func (o faultOutbox) Applied(ctx context.Context, origin, s string) (outbox.AppliedMarker, error) {
	if err := o.f.check("Outbox.Applied"); err != nil {
		return outbox.AppliedMarker{}, err
	}
	return o.OutboxStore.Applied(ctx, origin, s) //nolint:wrapcheck // passed through
}

func (o faultOutbox) LockApplied(ctx context.Context, origin, s string) (outbox.AppliedMarker, error) {
	if err := o.f.check("Outbox.LockApplied"); err != nil {
		return outbox.AppliedMarker{}, err
	}
	return o.OutboxStore.LockApplied(ctx, origin, s) //nolint:wrapcheck // passed through
}

func (o faultOutbox) SetApplied(ctx context.Context, m outbox.AppliedMarker) error {
	if err := o.f.check("Outbox.SetApplied"); err != nil {
		return err
	}
	return o.OutboxStore.SetApplied(ctx, m) //nolint:wrapcheck // passed through
}

func (o faultOutbox) RecordFailedFetch(ctx context.Context, v outbox.FailedBlobFetch) (outbox.FailedBlobFetch, error) {
	if err := o.f.check("Outbox.RecordFailedFetch"); err != nil {
		return outbox.FailedBlobFetch{}, err
	}
	return o.OutboxStore.RecordFailedFetch(ctx, v) //nolint:wrapcheck // passed through
}

func (o faultOutbox) FailedFetches(ctx context.Context, s string) ([]outbox.FailedBlobFetch, error) {
	if err := o.f.check("Outbox.FailedFetches"); err != nil {
		return nil, err
	}
	return o.OutboxStore.FailedFetches(ctx, s) //nolint:wrapcheck // passed through
}

func (o faultOutbox) ClearFailedFetch(ctx context.Context, s, sha256 string) error {
	if err := o.f.check("Outbox.ClearFailedFetch"); err != nil {
		return err
	}
	return o.OutboxStore.ClearFailedFetch(ctx, s, sha256) //nolint:wrapcheck // passed through
}

// faultBlobs is a node's blob store, failing where f says.
type faultBlobs struct {
	blob.Store
	f *faultSet
}

func (b faultBlobs) Exists(ctx context.Context, sha256 string) (bool, error) {
	if err := b.f.check("Blobs.Exists"); err != nil {
		return false, err
	}
	return b.Store.Exists(ctx, sha256) //nolint:wrapcheck // passed through
}

func (b faultBlobs) Open(ctx context.Context, sha256 string) (io.ReadSeekCloser, blob.Info, error) {
	if err := b.f.check("Blobs.Open"); err != nil {
		return nil, blob.Info{}, err
	}
	return b.Store.Open(ctx, sha256) //nolint:wrapcheck // passed through
}

func (b faultBlobs) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if err := b.f.check("Blobs.BeginUpload"); err != nil {
		return nil, err
	}
	return b.Store.BeginUpload(ctx, opts...) //nolint:wrapcheck // passed through
}

// faultKind is the test kind, failing where f says.
type faultKind struct {
	*streamtest.Kind
	f *faultSet
}

func (k faultKind) Streams(ctx context.Context) ([]string, error) {
	if err := k.f.check("Kind.Streams"); err != nil {
		return nil, err
	}
	return k.Kind.Streams(ctx) //nolint:wrapcheck // passed through
}

func (k faultKind) Subscribers(ctx context.Context, s string) ([]string, error) {
	if err := k.f.check("Kind.Subscribers"); err != nil {
		return nil, err
	}
	return k.Kind.Subscribers(ctx, s) //nolint:wrapcheck // passed through
}

func (k faultKind) References(ctx context.Context, s, sha256 string) (bool, error) {
	if err := k.f.check("Kind.References"); err != nil {
		return false, err
	}
	return k.Kind.References(ctx, s, sha256) //nolint:wrapcheck // passed through
}

func (k faultKind) Pending(ctx context.Context, s, sha256 string) (bool, error) {
	if err := k.f.check("Kind.Pending"); err != nil {
		return false, err
	}
	return k.Kind.Pending(ctx, s, sha256) //nolint:wrapcheck // passed through
}

func (k faultKind) Publish(ctx context.Context, s, sha256 string) error {
	if err := k.f.check("Kind.Publish"); err != nil {
		return err
	}
	return k.Kind.Publish(ctx, s, sha256) //nolint:wrapcheck // passed through
}

func (k faultKind) Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error {
	if err := k.f.check("Kind.Apply"); err != nil {
		return err
	}
	return k.Kind.Apply(ctx, e, held) //nolint:wrapcheck // passed through
}

// TestUnitStoreFailuresSurface fails each store, blob and kind call the
// engine makes, in turn, while every operation runs, and checks that the
// failure reaches the caller rather than being taken for an answer, so
// the queue retries the work, and that nothing is applied twice once the
// fault clears.
func TestUnitStoreFailuresSurface(t *testing.T) {
	type fault struct {
		name string
		skip int
	}
	var all []fault
	for _, name := range []string{
		"Outbox.Append", "Outbox.Read", "Outbox.LastSeq", "Outbox.Prune", "Outbox.PutCursor",
		"Outbox.Cursor", "Outbox.Applied", "Outbox.LockApplied", "Outbox.SetApplied",
		"Outbox.RecordFailedFetch", "Outbox.FailedFetches", "Outbox.ClearFailedFetch",
		"Peers.Get", "Resyncs.Flag", "Resyncs.Clear", "Instance.ID",
		"Blobs.Exists", "Blobs.Open", "Kind.Streams", "Kind.Subscribers", "Kind.References",
		"Kind.Pending", "Kind.Publish", "Kind.Apply",
	} {
		for skip := range 4 {
			all = append(all, fault{name, skip})
		}
	}
	for _, f := range all {
		t.Run(fmt.Sprintf("%s-%d", f.name, f.skip), func(t *testing.T) {
			w := newWorld()
			a, b := w.add(t), w.add(t)
			pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPull)
			body, sum := content("held by A")
			w.faults.fetchErr = errors.New("unavailable")
			a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
			a.run(streamsync.SendKind)
			w.faults.fetchErr = nil
			a.put("k", "2")
			a.put("k", "3")
			entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 1, 10))

			w.inject.arm(f.name, f.skip)
			ops := map[string]func() error{
				"append": func() error {
					_, err := a.kind.Commit(t.Context(), a.engine, stream, streamtest.Change{Type: streamtest.Put, Key: "n"}, nil)
					return err
				},
				"receive": func() error {
					_, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
						Origin: a.id, Stream: stream, FromSeq: 2, Entries: entries,
					})
					return err
				},
				"events": func() error {
					_, err := a.engine.Events(t.Context(), a.record(b.id), stream, 1, 10)
					return err
				},
				"blob": func() error {
					rc, _, err := a.engine.Blob(t.Context(), a.record(b.id), sum)
					if err == nil {
						_ = rc.Close()
					}
					return err
				},
				"dispatch": func() error { return a.engine.Dispatch(t.Context(), b.id, stream) },
				"pull":     func() error { return b.engine.PullFrom(t.Context(), a.id, stream) },
				"retry":    func() error { return b.engine.RetryFetch(t.Context(), stream, sum) },
				"sweep":    func() error { return b.engine.Sweep(t.Context()) },
				"prune": func() error {
					*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
					return a.engine.Prune(t.Context())
				},
			}
			surfaced := false
			for _, name := range []string{"append", "receive", "events", "blob", "dispatch", "pull", "retry", "sweep", "prune"} {
				err := ops[name]()
				// A failure of the peer serving a blob reaches this
				// instance as a failed fetch, with its account.
				if errors.Is(err, errBoom) || err != nil && strings.Contains(err.Error(), errBoom.Error()) {
					surfaced = true
				}
			}
			if w.inject.hit() > 0 && !surfaced {
				t.Fatalf("%s failed %d calls and no operation reported it", f.name, w.inject.hit())
			}
			w.inject.arm("", 0)
			b.run(streamsync.PullKind)
			if err := a.engine.Dispatch(t.Context(), b.id, stream); err != nil {
				t.Fatal(err)
			}
			got := b.applied(a)
			for i, s := range got {
				if s != fmt.Sprint(i+1) {
					t.Fatalf("applied %v after the fault cleared", got)
				}
			}
		})
	}
}
