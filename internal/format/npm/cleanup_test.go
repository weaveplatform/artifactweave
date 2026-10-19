package npm_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// limit sets the world's repository's maxUniqueSnapshots.
func (w *world) limit(n int) {
	w.t.Helper()
	w.c.MaxUniqueSnapshots = &n
	if err := w.st.Repos().Update(w.ctx, w.c); err != nil {
		w.t.Fatal(err)
	}
}

// marked is how many packages wait for their cleanup.
func (w *world) marked() int64 {
	w.t.Helper()
	stats, err := w.st.WorkQueue().Stats(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, s := range stats {
		if s.Kind == npm.KindSnapshots {
			return s.Pending + s.Leased + s.Parked
		}
	}
	return 0
}

// sweep runs the marked cleanups, as the periodic timer would, through
// the work queue the item service registers the npm format's kinds with.
func (w *world) sweep() int {
	w.t.Helper()
	q, err := work.New(w.st, work.Options{Now: func() time.Time { return w.now }})
	if err != nil {
		w.t.Fatal(err)
	}
	w.items.Periodic = map[string]time.Duration{npm.KindSnapshots: time.Hour}
	if err := w.items.RegisterWork(q); err != nil {
		w.t.Fatal(err)
	}
	n, err := q.RunDue(w.ctx, npm.KindSnapshots)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// versions is left-pad's indexed versions, sorted.
func (w *world) versions() []string {
	w.t.Helper()
	var out []string
	for v := range w.index()["left-pad"] {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// maxUniqueSnapshots applies to npm pre-releases, as 7.x applies it to npm
// repositories (known issue RTDEV-45622): the pre-releases of one base
// version are its unique builds, and past the limit the oldest go, at the
// periodic run, into the trash can. Releases and other base versions'
// pre-releases are counted apart, and latest moves off a version removed.
func TestMaxUniqueSnapshots(t *testing.T) {
	w := newWorld(t)
	w.limit(2)
	for _, v := range []string{"1.0.0-beta.1", "1.0.0-beta.2", "1.0.0", "1.0.0-rc.1", "2.0.0-rc.1"} {
		w.deployVersion(v)
	}
	// npm dist-tag add 1.0.0-beta.1 latest: the cleanup removes it anyway,
	// and latest moves by rule 4.
	w.annotate(canonical("1.0.0-beta.1"), types.Properties{npm.PropDistTag: {"latest"}})
	if got := w.marked(); got != 1 {
		t.Fatalf("marked %d packages", got)
	}
	if got := w.versions(); len(got) != 5 {
		t.Fatalf("deleted before the run: %v", got)
	}
	if n := w.sweep(); n != 1 {
		t.Fatalf("%d runs", n)
	}
	want := []string{"1.0.0", "1.0.0-beta.2", "1.0.0-rc.1", "2.0.0-rc.1"}
	if diff := cmp.Diff(want, w.versions()); diff != "" {
		t.Fatalf("versions (-want +got):\n%s", diff)
	}
	if got := w.latestOf("left-pad"); got != "1.0.0" {
		t.Fatalf("latest %s", got)
	}
	trashed, err := w.st.Trash().Find(w.ctx, w.c.Key, canonical("1.0.0-beta.1"))
	if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != "_system_" {
		t.Fatalf("trash %+v: %v", trashed, err)
	}
	if got := w.marked(); got != 0 {
		t.Fatalf("still marked: %d", got)
	}
}

// A limit of 1 keeps two builds (format.MinUniqueSnapshots), and a limit
// set back to 0 since the deploy deletes nothing (RTDEV-93991).
func TestMaxUniqueSnapshotsLimits(t *testing.T) {
	w := newWorld(t)
	w.limit(1)
	for _, v := range []string{"1.0.0-a", "1.0.0-b", "1.0.0-c"} {
		w.deployVersion(v)
	}
	w.sweep()
	if diff := cmp.Diff([]string{"1.0.0-b", "1.0.0-c"}, w.versions()); diff != "" {
		t.Fatalf("versions (-want +got):\n%s", diff)
	}
	w.deployVersion("1.0.0-d")
	w.limit(0)
	w.sweep()
	if got := w.versions(); len(got) != 3 {
		t.Fatalf("a limit of 0 deleted: %v", got)
	}
}

// Nothing is marked without a limit, for a release, for a tarball off its
// canonical path, or in a repository that is not local; a mark whose
// package or repository is gone, or no longer local, deletes nothing.
func TestMaxUniqueSnapshotsMarks(t *testing.T) {
	w := newWorld(t)
	w.deployVersion("1.0.0-beta.1")
	w.limit(2)
	w.deployVersion("1.0.0")
	w.deploy("uploads/left-pad.tgz", pkg("left-pad", "1.0.0-beta.2", ""), nil)
	if got := w.marked(); got != 0 {
		t.Fatalf("marked %d", got)
	}
	h := npm.Handler{}
	env := format.Env{Store: w.st, Blobs: w.items.Blobs, Remove: func(
		context.Context, repo.Config, []string, string,
	) error {
		t.Fatal("removed")
		return nil
	}}
	federated := w.c
	federated.Key, federated.Class = "npm-federated", repo.Federated
	if err := w.st.Repos().Create(w.ctx, federated); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		w.c.Key + "/gone", "missing/left-pad", federated.Key + "/left-pad", w.c.Key,
	} {
		if err := h.Work(
			w.ctx,
			env,
			types.WorkItem{Kind: npm.KindSnapshots, Key: key},
		); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
}

// A store failure fails the cleanup's run, which the queue retries, and a
// failure to mark the package fails the deploy.
func TestMaxUniqueSnapshotsFaults(t *testing.T) {
	w := newWorld(t)
	w.limit(2)
	for _, v := range []string{"1.0.0-a", "1.0.0-b", "1.0.0-c"} {
		w.deployVersion(v)
	}
	h := npm.Handler{}
	run := types.WorkItem{Kind: npm.KindSnapshots, Key: w.c.Key + "/left-pad"}
	for _, fail := range []string{"Find", "Versions", "Files", "GetByID"} {
		st := &faulty{Store: w.st, f: &faults{pass: map[string]int{fail: 0}}}
		if err := h.Work(w.ctx, format.Env{Store: st}, run); !errors.Is(err, errFault) {
			t.Errorf("%s: %v", fail, err)
		}
	}
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, canonical("1.0.0-c"))
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: w.st, Blobs: w.items.Blobs, Enqueue: func(
		context.Context, string, string, []byte,
	) error {
		return errFault
	}}
	if err := h.Deployed(w.ctx, env, w.c, it); !errors.Is(err, errFault) {
		t.Errorf("mark: %v", err)
	}
}
