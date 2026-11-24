package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// StampsStore is a store with the conflict rule's item records.
type StampsStore interface {
	tx.Runner
	Stamps() port.ItemStampStore
}

// errRolledBack fails the transaction that checks a record rolls back.
var errRolledBack = errors.New("storetest: rolled back")

func sha(c byte) string { return strings.Repeat(string(c), 64) }

func at(wall int64, logical uint32, origin string) model.Stamp {
	return model.Stamp{Time: outbox.HLC{Wall: wall, Logical: logical}, Origin: origin}
}

// RunStamps runs the item stamp contract (decision 0021, Conflict rule) on
// stores from newStore: a record reads back as stored; Replace sets only
// over the stamp it was given, or where there was none, so a stale read
// never overwrites a newer record; PendingOn and Holds tell waiting from
// live content per repository; tombstones are pruned by time and Clear
// empties one repository; and a rolled-back record is not kept.
func RunStamps(t *testing.T, newStore func(*testing.T) StampsStore) {
	t.Helper()
	t.Run("StampsReplace", func(t *testing.T) { stampsReplace(t, newStore(t).Stamps()) })
	t.Run("StampsPending", func(t *testing.T) { stampsPending(t, newStore(t).Stamps()) })
	t.Run("StampsPrune", func(t *testing.T) { stampsPrune(t, newStore(t)) })
}

func stampsReplace(t *testing.T, st port.ItemStampStore) {
	t.Helper()
	ctx := t.Context()
	if _, err := st.Stamp(ctx, "fed", "a/b.jar"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing stamp: %v", err)
	}
	live := model.ItemStamp{RepoKey: "fed", Path: "a/b.jar", Stamp: at(100, 7, "jfrt@a"), SHA256: sha('a'), Size: 3}
	if err := st.Record(ctx, live); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Stamp(ctx, "fed", "a/b.jar"); err != nil || !reflect.DeepEqual(got, live) {
		t.Fatalf("stamp = %+v, %v; want %+v", got, err, live)
	}
	// A stale read cannot replace what another change recorded.
	newer := live
	newer.Stamp, newer.Deleted, newer.SHA256, newer.Size = at(200, 0, "jfrt@b"), true, "", 0
	stale := at(100, 6, "jfrt@a")
	if ok, err := st.Replace(ctx, &stale, newer); err != nil || ok {
		t.Fatalf("replace over a stale stamp = %v, %v", ok, err)
	}
	if ok, err := st.Replace(ctx, nil, newer); err != nil || ok {
		t.Fatalf("replace of none over a record = %v, %v", ok, err)
	}
	if ok, err := st.Replace(ctx, &live.Stamp, newer); err != nil || !ok {
		t.Fatalf("replace = %v, %v", ok, err)
	}
	if got, _ := st.Stamp(ctx, "fed", "a/b.jar"); !reflect.DeepEqual(got, newer) {
		t.Fatalf("replaced = %+v, want %+v", got, newer)
	}
	fresh := model.ItemStamp{RepoKey: "fed", Path: "c.txt", Stamp: at(150, 0, "jfrt@c"), Folder: true}
	if ok, err := st.Replace(ctx, nil, fresh); err != nil || !ok {
		t.Fatalf("replace of none = %v, %v", ok, err)
	}
}

// stampsPending: waiting content is not held; live content is, per
// repository.
func stampsPending(t *testing.T, st port.ItemStampStore) {
	t.Helper()
	ctx := t.Context()
	waiting := model.ItemStamp{
		RepoKey: "fed", Path: "w/2", Stamp: at(300, 0, "jfrt@b"),
		SHA256: sha('b'), Size: 9, Pending: []byte(`{"path":"w/2"}`),
	}
	waiting1 := waiting
	waiting1.Path = "w/1"
	other := waiting
	other.RepoKey = "other"
	held := model.ItemStamp{RepoKey: "fed", Path: "h", Stamp: at(300, 1, "jfrt@a"), SHA256: sha('c'), Size: 1}
	for _, v := range []model.ItemStamp{waiting, waiting1, other, held} {
		if err := st.Record(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.PendingOn(ctx, "fed", sha('b'))
	if err != nil || len(got) != 2 || got[0].Path != "w/1" || got[1].Path != "w/2" ||
		string(got[1].Pending) != `{"path":"w/2"}` {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	for _, c := range []struct {
		repo, sha string
		want      bool
	}{{"fed", sha('c'), true}, {"fed", sha('b'), false}, {"other", sha('c'), false}, {"fed", sha('a'), false}} {
		if is, err := st.Holds(ctx, c.repo, c.sha); err != nil || is != c.want {
			t.Fatalf("holds %s %s = %v, %v", c.repo, c.sha[:1], is, err)
		}
	}
}

// stampsPrune: tombstones before the cutoff go and later ones and live
// records stay; Clear empties one repository; a rolled-back record is not
// kept.
func stampsPrune(t *testing.T, s StampsStore) {
	t.Helper()
	ctx := t.Context()
	st := s.Stamps()
	for _, v := range []model.ItemStamp{
		{RepoKey: "fed", Path: "early", Stamp: at(200, 4, "jfrt@b"), Deleted: true},
		{RepoKey: "fed", Path: "late", Stamp: at(200, 5, "jfrt@a"), Deleted: true},
		{RepoKey: "fed", Path: "live", Stamp: at(100, 0, "jfrt@a"), SHA256: sha('a')},
		{RepoKey: "other", Path: "kept", Stamp: at(300, 0, "jfrt@a")},
	} {
		if err := st.Record(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.PruneTombstones(ctx, outbox.HLC{Wall: 200, Logical: 5}); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	for p, want := range map[string]bool{"early": false, "late": true, "live": true} {
		if _, err := st.Stamp(ctx, "fed", p); (err == nil) != want {
			t.Fatalf("%s after pruning: %v", p, err)
		}
	}
	if err := st.Clear(ctx, "fed"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Stamp(ctx, "fed", "live"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("cleared: %v", err)
	}
	if _, err := st.Stamp(ctx, "other", "kept"); err != nil {
		t.Fatalf("other repository: %v", err)
	}
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: "gone", Stamp: at(1, 0, "jfrt@a")}); err != nil {
			return fmt.Errorf("record: %w", err)
		}
		return errRolledBack
	})
	if !errors.Is(err, errRolledBack) {
		t.Fatal(err)
	}
	if _, err := st.Stamp(ctx, "fed", "gone"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("rolled back record kept: %v", err)
	}
}
