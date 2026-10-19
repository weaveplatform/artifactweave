package maven_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// limited sets maxUniqueSnapshots.
func limited(n int) func(*repo.Config) {
	return func(c *repo.Config) { c.MaxUniqueSnapshots = &n }
}

// sweep runs the marked cleanups, as the periodic timer would, and
// returns how many packages it cleaned.
func (w *metaWorld) sweep() int {
	w.t.Helper()
	n, err := w.q.RunDue(w.ctx, maven.KindSnapshots)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// files lists the files under dir, sorted.
func (w *metaWorld) files(dir string) []string {
	w.t.Helper()
	var out []string
	for _, p := range w.tree() {
		if strings.HasPrefix(p, dir) && !maven.IsMetadata(p) {
			out = append(out, strings.TrimPrefix(p, dir))
		}
	}
	return out
}

// tree lists every item of the repository.
func (w *metaWorld) tree() []string {
	w.t.Helper()
	its, _, err := w.db.Items().Descendants(w.ctx, w.c.Key, false, "", store.Page{
		Limit: store.MaxLimit,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]string, 0, len(its))
	for _, it := range its {
		out = append(out, it.FullPath())
	}
	slices.Sort(out)
	return out
}

const snap = "com/acme/app/1.0-SNAPSHOT/"

// unique deploys build n of app 1.0-SNAPSHOT, its main artifact, its POM
// and, with sources, a classifier, a minute after the last build.
func (w *metaWorld) unique(n int, sources bool) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	b := fmt.Sprintf("app-1.0-%s-%d", w.now.Format("20060102.150405"), n)
	w.deploy(snap+b+".jar", nil)
	w.deploy(snap+b+".pom", nil)
	if sources {
		w.deploy(snap+b+"-sources.jar", nil)
	}
}

// With a limit of 2 a third build leaves exactly two unique builds per
// classifier (#283): build 1's main artifact and POM go, and its sources
// stay, since only builds 1 and 3 have sources. The deploys mark the
// package; nothing goes until the periodic run, which deletes through the
// item service into the trash can and queues the metadata again.
func TestMaxUniqueSnapshots(t *testing.T) {
	w := newMetaWorld(t, limited(2))
	w.unique(1, true)
	w.unique(2, false)
	w.unique(3, true)
	if got := w.queued()[maven.KindSnapshots]; got != 1 {
		t.Fatalf("marked %d times, want 1: %v", got, w.queued())
	}
	if len(w.files(snap)) != 8 {
		t.Fatalf("deleted before the run: %v", w.files(snap))
	}
	if n := w.sweep(); n != 1 {
		t.Fatalf("%d runs", n)
	}
	want := []string{
		"app-1.0-20261017.093100-1-sources.jar",
		"app-1.0-20261017.093200-2.jar", "app-1.0-20261017.093200-2.pom",
		"app-1.0-20261017.093300-3-sources.jar",
		"app-1.0-20261017.093300-3.jar", "app-1.0-20261017.093300-3.pom",
	}
	if got := w.files(snap); !slices.Equal(got, want) {
		t.Fatalf("left %v\nwant %v", got, want)
	}
	trashed, err := w.db.Trash().Find(w.ctx, w.c.Key, snap+"app-1.0-20261017.093100-1.jar")
	if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != maven.Deriver {
		t.Fatalf("trash %+v: %v", trashed, err)
	}
	if w.queued()[maven.KindMetadata] == 0 {
		t.Fatal("the cleanup did not queue the metadata")
	}
	meta, _, ok := w.get(snap + maven.MetadataFile)
	if !ok || strings.Contains(meta, "093100-1") || !strings.Contains(meta, "093300-3") {
		t.Fatalf("snapshot metadata:\n%s", meta)
	}
	// Nothing more to do: a second run deletes nothing.
	w.unique(4, false)
	w.sweep()
	if got := w.files(snap); len(got) != 6 || slices.Contains(got, want[1]) {
		t.Fatalf("after build 4: %v", got)
	}
}

// A limit of 1 keeps two builds, Artifactory's floor; builds of one moment
// go oldest number first, so build 10 outlives build 9.
func TestMaxUniqueSnapshotsFloor(t *testing.T) {
	w := newMetaWorld(t, limited(1))
	for _, n := range []int{9, 10, 11} {
		w.deploy(fmt.Sprintf("%sapp-1.0-20261017.093000-%d.jar", snap, n), nil)
	}
	w.sweep()
	want := []string{"app-1.0-20261017.093000-10.jar", "app-1.0-20261017.093000-11.jar"}
	if got := w.files(snap); !slices.Equal(got, want) {
		t.Fatalf("left %v", got)
	}
}

// The limit is read when the run comes: one set back to 0 since the
// deploys cancels the cleanup (RTDEV-93991), and so does a repository that
// is gone.
func TestMaxUniqueSnapshotsReset(t *testing.T) {
	w := newMetaWorld(t, limited(2))
	for n := 1; n <= 3; n++ {
		w.unique(n, false)
	}
	w.c.MaxUniqueSnapshots = nil
	if err := w.db.Repos().Update(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	w.sweep()
	if got := w.files(snap); len(got) != 6 {
		t.Fatalf("a reset limit deleted: %v", got)
	}
	if got := w.queued()[maven.KindSnapshots]; got != 0 {
		t.Fatalf("the cleanup is still queued: %v", w.queued())
	}
}

// Only builds the cleanup counts mark the package: a release, a
// non-unique -SNAPSHOT file under the Maven layout, and any file of a
// repository without a limit do not.
func TestMaxUniqueSnapshotsMarks(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) {
		limited(2)(c)
		c.SnapshotVersionBehavior = repo.NonUnique
	})
	w.deploy("com/acme/app/1.0/app-1.0.jar", nil)
	w.deploy(snap+"app-1.0-SNAPSHOT.jar", nil)
	if got := w.queued()[maven.KindSnapshots]; got != 0 {
		t.Fatalf("marked: %v", w.queued())
	}
	free := newMetaWorld(t, nil)
	free.unique(1, false)
	if got := free.queued()[maven.KindSnapshots]; got != 0 {
		t.Fatalf("marked without a limit: %v", free.queued())
	}
}

// Under a layout that keeps each build in a folder of its own, as Ivy's
// does, the oldest build's folders are pruned once empty, and its
// version leaves the index.
func TestMaxUniqueSnapshotsPrunes(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) {
		limited(2)(c)
		c.PackageType, c.RepoLayoutRef = repo.Ivy, "ivy-default"
	})
	for _, rev := range []string{"20261017093100", "20261017093200", "20261017093300"} {
		w.now = w.now.Add(time.Minute)
		w.deploy("org.acme/mod/1.0-"+rev+"/jars/mod-1.0-"+rev+".jar", nil)
	}
	w.sweep()
	for _, p := range w.tree() {
		if strings.Contains(p, "093100") {
			t.Fatalf("%s stayed: %v", p, w.tree())
		}
	}
	if !slices.Contains(w.tree(), "org.acme/mod") {
		t.Fatalf("the module folder went: %v", w.tree())
	}
	vs, _, err := maven.Versions(w.ctx, w.db, w.c, "org.acme:mod")
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions %v: %v", vs, err)
	}
}

// A failed delete leaves the item to retry; nothing of the build goes.
func TestMaxUniqueSnapshotsFailure(t *testing.T) {
	w := newMetaWorld(t, limited(2))
	for n := 1; n <= 3; n++ {
		w.unique(n, false)
	}
	failing := failingTrash{w.db}
	w.items.Store = failing
	n, err := w.q.RunDue(w.ctx, maven.KindSnapshots)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if got := w.files(snap); len(got) != 6 {
		t.Fatalf("a failed run deleted: %v", got)
	}
	it, err := w.db.WorkQueue().Get(w.ctx, maven.KindSnapshots, w.c.Key+"/com.acme:app")
	if err != nil || !strings.Contains(it.LastError, errTrash.Error()) {
		t.Fatalf("%+v %v", it, err)
	}
}

// A deploy whose mark fails fails with it, under deployer too, which queues
// no metadata; a run that cannot read the index fails.
func TestMaxUniqueSnapshotsStoreFailures(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) {
		limited(2)(c)
		c.SnapshotVersionBehavior = repo.Deployer
	})
	const jar = snap + "app-1.0-20261017.093000-1.jar"
	w.deploy(jar, nil)
	env := format.Env{Store: w.db, Layouts: layouts{}, Blobs: w.blobs, Enqueue: refuse}
	if err := (maven.Handler{}).Deployed(w.ctx, env, w.c, w.item(jar)); !errors.Is(err, errWork) {
		t.Fatalf("a failed mark: %v", err)
	}
	err := maven.Handler{}.Work(
		w.ctx,
		format.Env{Store: workFaults{w.db, "Find"}, Layouts: layouts{}},
		types.WorkItem{Kind: maven.KindSnapshots, Key: w.c.Key + "/com.acme:app"},
	)
	if !errors.Is(err, errWork) {
		t.Fatalf("a failed read: %v", err)
	}
}

var errTrash = errors.New("trash can full")

// failingTrash refuses every trash entry.
type failingTrash struct{ store.Store }

func (f failingTrash) Trash() store.TrashStore { return refusingTrash{f.Store.Trash()} }

type refusingTrash struct{ store.TrashStore }

func (refusingTrash) Add(context.Context, types.TrashEntry) (types.TrashEntry, error) {
	return types.TrashEntry{}, errTrash
}
