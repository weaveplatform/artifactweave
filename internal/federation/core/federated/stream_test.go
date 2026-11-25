package federated_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func (s store) Outbox() outbox.OutboxStore       { return s.platform.Outbox() }
func (s store) Instance() instance.InstanceStore { return s.platform.Instance() }

// items is the artifacts module's FederatedItems over a map, recording
// what was applied.
type items struct {
	put       []artifactsapi.ItemChange
	deleted   []string
	discarded []string
	// held is what List lists, in order of path.
	held    []artifactsapi.ItemChange
	listErr error
	// holds is the content Holds reports; holdsErr fails it.
	holds    map[string]bool
	holdsErr error
	err      error
	// during runs inside Put, as a local write racing the apply would.
	during func(ctx context.Context) error
}

func (i *items) Put(ctx context.Context, c artifactsapi.ItemChange) error {
	if i.during != nil {
		if err := i.during(ctx); err != nil {
			return err
		}
	}
	if i.err != nil {
		return i.err
	}
	i.put = append(i.put, c)
	return nil
}

func (i *items) Delete(_ context.Context, repoKey, path string, _ bool) error {
	if i.err != nil {
		return i.err
	}
	i.deleted = append(i.deleted, repoKey+":"+path)
	return nil
}

func (i *items) Discard(_ context.Context, repoKey, path string) error {
	if i.err != nil {
		return i.err
	}
	i.discarded = append(i.discarded, repoKey+":"+path)
	return nil
}

// List lists the items the fixture holds, held, after after.
func (i *items) List(_ context.Context, _, after string, limit int, properties bool) ([]artifactsapi.ItemChange, error) {
	if i.listErr != nil {
		return nil, i.listErr
	}
	var out []artifactsapi.ItemChange
	for _, c := range i.held {
		if c.Path > after && len(out) < limit {
			if !properties {
				c.Properties = nil
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func (i *items) Holds(_ context.Context, _, sha256 string) (bool, error) {
	return i.holds[sha256], i.holdsErr
}

const sha1 = "1111111111111111111111111111111111111111111111111111111111111111"

// streamFixture is instance A with the federated repository "fed" whose
// members are B (enabled) and C (disabled), and its repository stream.
type streamFixture struct {
	s     store
	items *items
	repos *repositories
	k     federated.Stream
	e     *streamsync.Engine
	clock time.Time
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	f := &streamFixture{s: newStore(t), items: &items{}, clock: t0}
	if err := instance.EnsureInstance(t.Context(), f.s); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Federation().SetMembers(t.Context(), "fed", []model.FederationMember{
		{PeerInstanceID: "b", BaseURL: bBase, RemoteRepoKey: "fed", Enabled: true},
		{PeerInstanceID: "c", BaseURL: cBase, RemoteRepoKey: "fed", Enabled: false},
	}); err != nil {
		t.Fatal(err)
	}
	q, err := work.New(f.s, work.Options{Workers: 1, Lease: time.Minute, Holder: "a"})
	if err != nil {
		t.Fatal(err)
	}
	f.repos = newRepositories(repo.Config{Key: "fed", Class: repo.Federated, Members: []repo.Member{
		{URL: selfBase + "/artifactory/fed", Enabled: repo.Flex(true)},
		{URL: bBase + "/fed", Enabled: repo.Flex(true)},
		{URL: cBase + "/fed", Enabled: repo.Flex(false)},
	}})
	f.e = &streamsync.Engine{Store: f.s, Kinds: &streamsync.Registry{}, Queue: q, Now: func() time.Time { return f.clock }}
	f.k = federated.Stream{
		Store: f.s, Engine: f.e, Items: f.items, Repositories: f.repos,
		SelfBase: func(context.Context) (string, error) { return selfBase + "/artifactory", nil },
	}
	if err := f.e.Kinds.Register(f.k); err != nil {
		t.Fatal(err)
	}
	return f
}

func entry(typ, origin string, wall int64, p string) outbox.OutboxEntry {
	return outbox.OutboxEntry{
		Origin: origin, Stream: "repo:fed", Seq: 1, Time: outbox.HLC{Wall: wall}, EventType: typ,
		Payload: json.RawMessage(p), BlobSHA256: sha1, BlobSize: 3,
	}
}

func (f *streamFixture) stamp(t *testing.T, path string) model.ItemStamp {
	t.Helper()
	s, err := f.s.Stamps().Stamp(t.Context(), "fed", path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *streamFixture) entries(t *testing.T) []outbox.OutboxEntry {
	t.Helper()
	self := must(f.s.Instance().Get(t.Context())).InstanceID
	es, err := f.s.Outbox().Read(t.Context(), self, "repo:fed", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestStreamProducer appends a change's whole state in its transaction and
// records its stamp; a repository without members appends nothing; the
// clock orders a change after the stamp it replaces.
func TestUnitStreamProducer(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	if err := f.k.ItemChanged(ctx, artifactsapi.ItemChange{Repository: "local", Path: "x"}); err != nil {
		t.Fatal(err)
	}
	// A peer's change stamped far ahead of this clock is what the item holds.
	ahead := model.Stamp{Time: outbox.HLC{Wall: t0.Add(time.Hour).UnixMilli(), Logical: 4}, Origin: "zz"}
	if err := f.s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: "a/b.jar", Stamp: ahead}); err != nil {
		t.Fatal(err)
	}
	changes := []artifactsapi.ItemChange{
		{
			Repository: "fed", Path: "a/b.jar", Size: 3, Actual: kernel.Checksums{SHA256: sha1, SHA1: "s1"},
			Created: t0, CreatedBy: "u", Modified: t0, ModifiedBy: "u", Properties: map[string][]string{"k": {"v"}},
		},
		{Repository: "fed", Path: "a", Folder: true},
		{Repository: "fed", Path: "a/b.jar", Deleted: true},
	}
	for _, c := range changes {
		if err := f.k.ItemChanged(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	es := f.entries(t)
	if len(es) != 3 {
		t.Fatalf("entries = %+v", es)
	}
	if es[0].EventType != federated.EventPut || es[0].BlobSHA256 != sha1 || es[0].BlobSize != 3 ||
		!strings.Contains(string(es[0].Payload), `"properties":{"k":["v"]}`) {
		t.Fatalf("put = %+v %s", es[0], es[0].Payload)
	}
	if !(model.Stamp{Time: es[0].Time, Origin: es[0].Origin}).After(ahead) {
		t.Fatalf("change at %v does not order after the stamp it replaced %v", es[0].Time, ahead.Time)
	}
	if es[1].BlobSHA256 != "" || string(es[1].Payload) != `{"path":"a","folder":true}` {
		t.Fatalf("folder = %+v %s", es[1], es[1].Payload)
	}
	if es[2].EventType != federated.EventDelete || es[2].BlobSHA256 != "" || string(es[2].Payload) != `{"path":"a/b.jar"}` {
		t.Fatalf("delete = %+v %s", es[2], es[2].Payload)
	}
	if got := f.stamp(t, "a/b.jar"); !got.Deleted || got.Time != es[2].Time || got.Origin != es[2].Origin {
		t.Fatalf("tombstone = %+v", got)
	}
	if held, _ := f.k.References(ctx, "repo:fed", sha1); held {
		t.Fatal("deleted content still referenced")
	}
}

// TestStreamConflictRule applies the later change, breaks a tie by the
// higher origin, and keeps a tombstone against an older put.
func TestUnitStreamConflictRule(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	put := `{"path":"p","properties":{"k":["%s"]}}`
	steps := []struct {
		e       outbox.OutboxEntry
		applied bool
	}{
		{entry(federated.EventPut, "m", 100, strings.Replace(put, "%s", "m100", 1)), true},
		{entry(federated.EventPut, "z", 90, strings.Replace(put, "%s", "z90", 1)), false},
		// Same time: the higher origin wins, the lower loses.
		{entry(federated.EventPut, "n", 100, strings.Replace(put, "%s", "n100", 1)), true},
		{entry(federated.EventPut, "a", 100, strings.Replace(put, "%s", "a100", 1)), false},
		{entry(federated.EventDelete, "b", 200, `{"path":"p"}`), true},
		// The tombstone stops a late put ordered before it.
		{entry(federated.EventPut, "z", 150, strings.Replace(put, "%s", "z150", 1)), false},
		{entry(federated.EventPut, "a", 201, strings.Replace(put, "%s", "a201", 1)), true},
	}
	for i, s := range steps {
		before := len(f.items.put) + len(f.items.deleted)
		if err := f.k.Apply(ctx, s.e, true); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if applied := len(f.items.put)+len(f.items.deleted) > before; applied != s.applied {
			t.Fatalf("step %d (%s@%d %s): applied = %v", i, s.e.Origin, s.e.Time.Wall, s.e.EventType, applied)
		}
	}
	var got []string
	for _, c := range f.items.put {
		got = append(got, c.Properties["k"][0])
	}
	if want := []string{"m100", "n100", "a201"}; !slices.Equal(got, want) || !slices.Equal(f.items.deleted, []string{"fed:p"}) {
		t.Fatalf("puts = %v, deletes = %v", got, f.items.deleted)
	}
	if c := f.items.put[0]; c.Actual.SHA256 != sha1 || c.Size != 3 || c.Repository != "fed" {
		t.Fatalf("change = %+v", c)
	}
	if s := f.stamp(t, "p"); s.Origin != "a" || s.Time.Wall != 201 || s.Deleted {
		t.Fatalf("stamp = %+v", s)
	}
}

// TestStreamPending keeps a winning content change whose blob did not
// arrive, leaves the item alone, and applies it when the blob does.
func TestUnitStreamPending(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 100, `{"path":"w"}`), false); err != nil {
		t.Fatal(err)
	}
	if len(f.items.put) != 0 {
		t.Fatalf("applied before the blob: %+v", f.items.put)
	}
	if size, ok, err := f.k.Pending(ctx, "repo:fed", sha1); err != nil || !ok || size != 3 {
		t.Fatalf("pending = %d, %v, %v", size, ok, err)
	}
	if held, _ := f.k.References(ctx, "repo:fed", sha1); held {
		t.Fatal("pending content referenced")
	}
	if err := f.k.Publish(ctx, "repo:fed", sha1); err != nil {
		t.Fatal(err)
	}
	if len(f.items.put) != 1 || f.items.put[0].Path != "w" {
		t.Fatalf("published = %+v", f.items.put)
	}
	if _, ok, _ := f.k.Pending(ctx, "repo:fed", sha1); ok {
		t.Fatal("still pending")
	}
	if held, _ := f.k.References(ctx, "repo:fed", sha1); !held {
		t.Fatal("published content not referenced")
	}
	// A pending change a later delete overtook is not applied.
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 300, `{"path":"v"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := f.k.Apply(ctx, entry(federated.EventDelete, "c", 400, `{"path":"v"}`), true); err != nil {
		t.Fatal(err)
	}
	if err := f.k.Publish(ctx, "repo:fed", sha1); err != nil || len(f.items.put) != 1 {
		t.Fatalf("publish after delete = %v, %+v", err, f.items.put)
	}
	for _, stream := range []string{"test:x", "repo:"} {
		if _, ok, err := f.k.Pending(ctx, stream, sha1); ok || err != nil {
			t.Fatalf("%s pending = %v, %v", stream, ok, err)
		}
		if err := f.k.Publish(ctx, stream, sha1); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStreamApplySkips skips what it cannot read or place, and fails what
// the store or a racing write spoils, so the sender retries.
func TestUnitStreamApplySkips(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	for _, e := range []outbox.OutboxEntry{
		entry("item.other", "b", 1, `{"path":"p"}`),
		entry(federated.EventPut, "b", 1, `{"nopath":true}`),
		entry(federated.EventPut, "b", 1, `[]`),
		{Stream: "test:x", EventType: federated.EventPut, Payload: json.RawMessage(`{"path":"p"}`)},
	} {
		if err := f.k.Apply(ctx, e, true); err != nil {
			t.Fatalf("%s: %v", e.Payload, err)
		}
	}
	if len(f.items.put) != 0 {
		t.Fatalf("applied = %+v", f.items.put)
	}
	for _, skip := range []error{kernel.ErrConflict, kernel.ErrNotFound} {
		f.items.err = skip
		if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 5, `{"path":"p"}`), true); err != nil {
			t.Fatalf("%v: %v", skip, err)
		}
		if _, err := f.s.Stamps().Stamp(ctx, "fed", "p"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%v: a skipped change recorded its stamp: %v", skip, err)
		}
	}
	// A pending change that cannot be placed once its blob arrives is
	// published as skipped, and waits no more.
	f.items.err = nil
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 5, `{"path":"q"}`), false); err != nil {
		t.Fatal(err)
	}
	f.items.err = kernel.ErrConflict
	if err := f.k.Publish(ctx, "repo:fed", sha1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.k.Pending(ctx, "repo:fed", sha1); ok {
		t.Fatal("skipped change still pending")
	}
	f.items.err = kernel.ErrStore
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 9, `{"path":"r"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := f.k.Publish(ctx, "repo:fed", sha1); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("publish with a failing store = %v", err)
	}
	f.items.err = kernel.ErrStore
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 6, `{"path":"p"}`), true); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("store failure = %v", err)
	}
	// A local write committing the item's stamp during the apply makes the
	// apply's stamp stale: it fails, to apply again against the new stamp.
	f.items.err = nil
	f.items.during = func(ctx context.Context) error {
		return f.s.Stamps().Record(ctx, model.ItemStamp{
			RepoKey: "fed", Path: "p",
			Stamp: model.Stamp{Time: outbox.HLC{Wall: 500}, Origin: "a"},
		})
	}
	if err := f.k.Apply(ctx, entry(federated.EventPut, "b", 7, `{"path":"p"}`), true); err == nil {
		t.Fatal("raced apply succeeded")
	}
}

// TestStreamMembership lists the member repositories' streams, subscribes
// disabled members too, and pauses a disabled member, or every member
// while this instance's own is disabled.
func TestUnitStreamMembership(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	if err := f.s.Federation().SetMembers(ctx, "other", []model.FederationMember{
		{PeerInstanceID: "b", BaseURL: bBase, RemoteRepoKey: "other", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := must(f.k.Streams(ctx)); !slices.Equal(got, []string{"repo:fed", "repo:other"}) {
		t.Fatalf("streams = %v", got)
	}
	if got := must(f.k.Subscribers(ctx, "repo:fed")); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("subscribers = %v", got)
	}
	if got := must(f.k.Subscribers(ctx, "test:fed")); got != nil {
		t.Fatalf("other kind's subscribers = %v", got)
	}
	paused := func(stream, peer string) bool {
		t.Helper()
		p, err := f.k.Paused(ctx, stream, peer)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if paused("repo:fed", "b") || !paused("repo:fed", "c") || paused("test:fed", "c") {
		t.Fatal("pause by member")
	}
	f.repos.values["fed"].Members[0].Enabled.Value = false
	if !paused("repo:fed", "b") || paused("repo:other", "b") {
		t.Fatal("pause by own member")
	}
	f.repos.getErr = kernel.ErrStore
	if _, err := f.k.Paused(ctx, "repo:fed", "b"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("paused with a failing store = %v", err)
	}
	if got := (federated.Stream{}).UseCases(streamsync.Receive); !slices.Equal(got, []model.UseCase{model.UseFederatedRepo}) {
		t.Fatalf("use cases = %v", got)
	}
	if (federated.Stream{}).Name() != "repo" {
		t.Fatal("name")
	}
}

// TestStreamPruneTombstones removes tombstones older than retention only.
func TestUnitStreamPruneTombstones(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	for i, at := range []time.Time{t0.Add(-8 * 24 * time.Hour), t0.Add(-6 * 24 * time.Hour)} {
		if err := f.s.Stamps().Record(ctx, model.ItemStamp{
			RepoKey: "fed", Path: string(rune('a' + i)), Deleted: true,
			Stamp: model.Stamp{Time: outbox.HLC{Wall: at.UnixMilli()}, Origin: "b"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := f.k.PruneTombstones(ctx, t0, 7*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned = %d, %v", n, err)
	}
	if _, err := f.s.Stamps().Stamp(ctx, "fed", "b"); err != nil {
		t.Fatalf("recent tombstone: %v", err)
	}
}
