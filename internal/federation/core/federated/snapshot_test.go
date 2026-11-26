package federated_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

const sha2 = "2222222222222222222222222222222222222222222222222222222222222222"

var created = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// held is what the snapshot fixture's repository holds: a folder, a file
// stamped by B, a file no change stamped, a file without content, and a
// file whose stamp is a tombstone a later write outlived.
func held() []artifactsapi.ItemChange {
	file := func(p string) artifactsapi.ItemChange {
		return artifactsapi.ItemChange{
			Repository: "fed", Path: p, Size: 3, Actual: kernel.Checksums{SHA256: sha1},
			Created: created, Modified: t0, Properties: map[string][]string{"k": {"v"}},
		}
	}
	nosha := file("e-nosha")
	nosha.Actual = kernel.Checksums{}
	return []artifactsapi.ItemChange{
		{Repository: "fed", Path: "a", Folder: true, Created: created, Modified: t0},
		file("a/b"), file("c-unstamped"), nosha, file("f-outlived"),
	}
}

func (f *streamFixture) record(t *testing.T, s model.ItemStamp) {
	t.Helper()
	s.RepoKey = "fed"
	if err := f.s.Stamps().Record(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}

func newSnapshotFixture(t *testing.T) (*streamFixture, string) {
	t.Helper()
	f := newStreamFixture(t)
	f.items.held = held()
	stamp := func(wall int64, origin string) model.Stamp {
		return model.Stamp{Time: outbox.HLC{Wall: wall}, Origin: origin}
	}
	f.record(t, model.ItemStamp{Path: "a/b", Stamp: stamp(5, "b"), SHA256: sha1, Size: 3})
	pending, _ := json.Marshal(map[string]any{"path": "d-pending", "created": created, "modified": t0})
	f.record(t, model.ItemStamp{Path: "d-pending", Stamp: stamp(6, "c"), SHA256: sha2, Size: 4, Pending: pending})
	f.record(t, model.ItemStamp{Path: "f-outlived", Stamp: stamp(7, "b"), Deleted: true})
	f.record(t, model.ItemStamp{Path: "g-gone", Stamp: stamp(8, "c"), Deleted: true})
	f.record(t, model.ItemStamp{Path: "h-skipped", Stamp: stamp(9, "c"), SHA256: sha1})
	return f, must(f.s.Instance().Get(t.Context())).InstanceID
}

func keys(es []model.SnapshotEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Key)
	}
	return out
}

// The snapshot merges the items with their stamps in byte order of path,
// in pages of exactly the limit until the last, whatever lists nothing:
// a pending change is the item's state, an unstamped item is stamped with
// its modification time and this instance, a tombstone with nothing here
// is listed deleted; a file without content, and a stamp of an item that
// is not here, list nothing. Creation times travel only with
// propagateCreatedTime on.
func TestUnitStreamSnapshotItems(t *testing.T) {
	f, self := newSnapshotFixture(t)
	want := []string{"a", "a/b", "c-unstamped", "d-pending", "f-outlived", "g-gone"}
	for _, limit := range []int{100, 1, 2, 3} {
		var got []model.SnapshotEntry
		after := ""
		for range 20 {
			page, err := f.k.SnapshotItems(t.Context(), self, "repo:fed", after, limit)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, page...)
			// The engine reads a short page as the last.
			if len(page) < limit {
				break
			}
			after = page[len(page)-1].Key
		}
		if !slices.Equal(keys(got), want) {
			t.Fatalf("limit %d: %v, want %v", limit, keys(got), want)
		}
	}
	got := must(f.k.SnapshotItems(t.Context(), self, "repo:fed", "", 100))
	byKey := map[string]model.SnapshotEntry{}
	for _, e := range got {
		byKey[e.Key] = e
	}
	if e := byKey["a/b"]; e.Origin != "b" || e.Time.Wall != 5 || e.BlobSHA256 != sha1 || e.BlobSize != 3 {
		t.Fatalf("a/b = %+v", e)
	}
	if e := byKey["c-unstamped"]; e.Origin != self || e.Time.Wall != t0.UnixMilli() {
		t.Fatalf("unstamped = %+v, want this instance at its modification time", e)
	}
	if e := byKey["f-outlived"]; e.Deleted || e.Origin != self {
		t.Fatalf("outlived tombstone = %+v, want the item under its own time", e)
	}
	if e := byKey["d-pending"]; e.Origin != "c" || e.BlobSHA256 != sha2 || e.BlobSize != 4 {
		t.Fatalf("pending = %+v", e)
	}
	if e := byKey["g-gone"]; !e.Deleted || e.Payload != nil {
		t.Fatalf("tombstone = %+v", e)
	}
	var p map[string]any
	if err := json.Unmarshal(byKey["a/b"].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["created"]; ok || p["properties"] == nil {
		t.Fatalf("payload %v, want properties and no creation time", p)
	}

	if err := f.s.Settings().Put(t.Context(), settings.SettingFederationPropagateCreatedTime, true); err != nil {
		t.Fatal(err)
	}
	for _, e := range must(f.k.SnapshotItems(t.Context(), self, "repo:fed", "", 100)) {
		if e.Deleted {
			continue
		}
		var p struct{ Created time.Time }
		if err := json.Unmarshal(e.Payload, &p); err != nil || !p.Created.Equal(created) {
			t.Fatalf("%s created %v (%v), want %v", e.Key, p.Created, err, created)
		}
	}

	// The diff's own list carries no state.
	local := must(f.k.LocalItems(t.Context(), "b", "repo:fed", "", 100))
	if !slices.Equal(keys(local), want) {
		t.Fatalf("local %v, want %v", keys(local), want)
	}
	for _, e := range local {
		if e.Payload != nil {
			t.Fatalf("local %s carries a payload", e.Key)
		}
		if e.Pending != (e.Key == "d-pending") {
			t.Fatalf("local pending marker: %+v", e)
		}
	}
}

func TestUnitStreamSnapshotItemsEdges(t *testing.T) {
	f, self := newSnapshotFixture(t)
	for _, s := range []string{"other:x", "repo:"} {
		if got, err := f.k.SnapshotItems(t.Context(), self, s, "", 10); err != nil || got != nil {
			t.Fatalf("%s: %v, %v", s, got, err)
		}
		if got, err := f.k.LocalItems(t.Context(), self, s, "", 10); err != nil || got != nil {
			t.Fatalf("%s: %v, %v", s, got, err)
		}
	}
	f.items.listErr = kernel.ErrNotFound
	if got, err := f.k.SnapshotItems(t.Context(), self, "repo:fed", "", 10); err != nil || len(got) != 0 {
		t.Fatalf("not federated: %v, %v", got, err)
	}
	f.items.listErr = kernel.ErrStore
	if _, err := f.k.SnapshotItems(t.Context(), self, "repo:fed", "", 10); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("list failure: %v", err)
	}
	f.items.listErr = nil
	f.record(t, model.ItemStamp{Path: "b-bad", Stamp: model.Stamp{Origin: "c"}, Pending: []byte("{")})
	if _, err := f.k.SnapshotItems(t.Context(), self, "repo:fed", "", 10); err == nil {
		t.Fatal("an unreadable pending change was listed")
	}
}

// Reconcile makes each item what the winning entry says, records its
// stamp, waits for content it does not hold, and removes for a tombstone;
// Discard removes an item and the stamps below it.
func TestUnitStreamReconcile(t *testing.T) {
	f := newStreamFixture(t)
	f.clock = t0.Add(time.Hour)
	f.k.Now = func() time.Time { return f.clock }
	payload := func(p string, folder bool, withCreated bool) []byte {
		m := map[string]any{"path": "ignored", "folder": folder, "modified": t0}
		if withCreated {
			m["created"] = created
		}
		b, _ := json.Marshal(m)
		return b
	}
	at := func(wall int64) outbox.HLC { return outbox.HLC{Wall: wall} }
	for _, c := range []struct {
		en   model.SnapshotEntry
		held bool
	}{
		{model.SnapshotEntry{Key: "dir", Time: at(1), Origin: "b", Payload: payload("dir", true, false)}, false},
		{model.SnapshotEntry{
			Key: "dir/f", Time: at(2), Origin: "b", Payload: payload("dir/f", false, true),
			BlobSHA256: sha1, BlobSize: 3,
		}, true},
		{model.SnapshotEntry{
			Key: "wait", Time: at(3), Origin: "c", Payload: payload("wait", false, true),
			BlobSHA256: sha2, BlobSize: 4,
		}, false},
		{model.SnapshotEntry{Key: "gone", Time: at(4), Origin: "c", Deleted: true}, false},
		{model.SnapshotEntry{Key: "bad", Time: at(5), Origin: "c", Payload: []byte("{")}, true},
	} {
		if err := f.k.Reconcile(t.Context(), "b", "repo:fed", c.en, c.held); err != nil {
			t.Fatalf("%s: %v", c.en.Key, err)
		}
	}
	if len(f.items.put) != 2 || f.items.put[0].Path != "dir" || !f.items.put[0].Created.Equal(f.clock) ||
		f.items.put[1].Path != "dir/f" || !f.items.put[1].Created.Equal(created) || f.items.put[1].Actual.SHA256 != sha1 {
		t.Fatalf("put %+v", f.items.put)
	}
	if !slices.Equal(f.items.deleted, []string{"fed:gone", "fed:gone"}) {
		t.Fatalf("deleted %v", f.items.deleted)
	}
	if s := f.stamp(t, "wait"); len(s.Pending) == 0 || s.SHA256 != sha2 || s.Origin != "c" {
		t.Fatalf("waiting stamp %+v", s)
	}
	if s := f.stamp(t, "gone"); !s.Deleted || s.Time.Wall != 4 {
		t.Fatalf("tombstone %+v", s)
	}
	if _, err := f.s.Stamps().Stamp(t.Context(), "fed", "bad"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("an unreadable entry was stamped: %v", err)
	}
	// A later entry replaces the stamp it read.
	if err := f.k.Reconcile(t.Context(), "b", "repo:fed", model.SnapshotEntry{
		Key: "gone", Time: at(9), Origin: "b", Payload: payload("gone", true, false),
	}, false); err != nil {
		t.Fatal(err)
	}
	if s := f.stamp(t, "gone"); s.Deleted || s.Time.Wall != 9 {
		t.Fatalf("replaced stamp %+v", s)
	}

	// An apply the artifacts module skips records nothing; a failure is
	// the page's.
	f.items.err = kernel.ErrConflict
	if err := f.k.Reconcile(t.Context(), "b", "repo:fed", model.SnapshotEntry{
		Key: "skip", Time: at(10), Origin: "b", Payload: payload("skip", true, false),
	}, false); err != nil {
		t.Fatal(err)
	}
	f.items.err = kernel.ErrStore
	if err := f.k.Reconcile(t.Context(), "b", "repo:fed", model.SnapshotEntry{
		Key: "fail", Time: at(10), Origin: "b", Deleted: true,
	}, false); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("failure: %v", err)
	}
	f.items.err = nil
	if err := f.k.Reconcile(t.Context(), "b", "other:x", model.SnapshotEntry{Key: "x"}, false); err != nil {
		t.Fatal(err)
	}

	// Discard removes the item and every stamp below it.
	if err := f.k.Discard(t.Context(), "b", "repo:fed", "dir"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.items.discarded, []string{"fed:dir"}) {
		t.Fatalf("discarded %v", f.items.discarded)
	}
	for _, p := range []string{"dir", "dir/f"} {
		if _, err := f.s.Stamps().Stamp(t.Context(), "fed", p); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s stamp after discard: %v", p, err)
		}
	}
	if err := f.k.Discard(t.Context(), "b", "other:x", "dir"); err != nil {
		t.Fatal(err)
	}
	f.items.err = kernel.ErrNotFound
	if err := f.k.Discard(t.Context(), "b", "repo:fed", "dir"); err != nil {
		t.Fatalf("not federated: %v", err)
	}
	f.items.err = kernel.ErrStore
	if err := f.k.Discard(t.Context(), "b", "repo:fed", "dir"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("discard failure: %v", err)
	}
}

func TestUnitPropagatesCreatedTime(t *testing.T) {
	s := newStore(t)
	if on, err := federated.PropagatesCreatedTime(t.Context(), s.Settings()); err != nil || on {
		t.Fatalf("unset: %v, %v", on, err)
	}
	if err := s.Settings().Put(t.Context(), settings.SettingFederationPropagateCreatedTime, true); err != nil {
		t.Fatal(err)
	}
	if on, err := federated.PropagatesCreatedTime(t.Context(), s.Settings()); err != nil || !on {
		t.Fatalf("set: %v, %v", on, err)
	}
}

// Content a full sync lists without a stamp, such as a file stored before
// the repository was federated, may be fetched by the members too.
func TestUnitStreamReferencesUnstampedContent(t *testing.T) {
	f := newStreamFixture(t)
	if held, err := f.k.References(t.Context(), "repo:fed", sha2); err != nil || held {
		t.Fatalf("nothing holds it: %v, %v", held, err)
	}
	f.items.holds = map[string]bool{sha2: true}
	if held, err := f.k.References(t.Context(), "repo:fed", sha2); err != nil || !held {
		t.Fatalf("an unstamped file holds it: %v, %v", held, err)
	}
	f.items.holdsErr = kernel.ErrNotFound
	if held, err := f.k.References(t.Context(), "repo:fed", sha2); err != nil || held {
		t.Fatalf("not federated: %v, %v", held, err)
	}
	f.items.holdsErr = kernel.ErrStore
	if _, err := f.k.References(t.Context(), "repo:fed", sha2); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("lookup failure: %v", err)
	}
}
