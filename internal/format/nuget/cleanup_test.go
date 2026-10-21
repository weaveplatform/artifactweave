package nuget_test

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// limited sets maxUniqueSnapshots.
func limited(n int) func(*repo.Config) {
	return func(c *repo.Config) { c.MaxUniqueSnapshots = &n }
}

// sweep runs the marked cleanups, as the periodic timer would, and returns
// how many packages it cleaned.
func (w *world) sweep() int {
	w.t.Helper()
	n, err := w.q.RunDue(w.ctx, nuget.KindSnapshots)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// marked counts the queued cleanups.
func (w *world) marked() int64 {
	w.t.Helper()
	stats, err := w.st.WorkQueue().Stats(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, s := range stats {
		if s.Kind == nuget.KindSnapshots {
			return s.Pending + s.Leased + s.Parked
		}
	}
	return 0
}

// push deploys id's version, and its symbols with it when sym is set.
func (w *world) push(id, version string, sym bool) {
	w.t.Helper()
	v, _ := nuget.ParseVersion(version)
	w.deploy(nuget.FileName(id, v), pkg(id, version, ""), nil)
	if sym {
		w.deploy(nuget.SymbolsFileName(id, v), symbols(id, version), nil)
	}
}

// With a limit of 2, each release keeps its two newest pre-releases, and
// releases are never counted (decision 0038). Pushes only mark the package;
// the periodic run deletes the oldest with its symbols, into the trash can
// as the system user's.
func TestMaxUniqueSnapshots(t *testing.T) {
	w := newWorld(t, limited(2))
	w.push("Foo", "1.0.0-alpha.1", true)
	w.push("Foo", "1.0.0-alpha.2", false)
	w.push("Foo", "1.0.0", false)
	w.push("Foo", "1.0.0-Beta", false)
	w.push("Foo", "2.0.0-rc.1", false)
	w.push("Foo", "1.0.0.1-a", false)
	w.push("Foo", "1.0.0.1-b", false)
	if w.marked() != 1 {
		t.Fatalf("marked %d times", w.marked())
	}
	if len(w.versions("foo")) != 7 {
		t.Fatal("deleted before the run")
	}
	if n := w.sweep(); n != 1 {
		t.Fatalf("%d runs", n)
	}
	want := []string{"1.0.0", "1.0.0-alpha.2", "1.0.0-beta", "1.0.0.1-a", "1.0.0.1-b", "2.0.0-rc.1"}
	if got := w.versions("foo"); !slices.Equal(got, want) {
		t.Fatalf("left %v", got)
	}
	for _, p := range []string{"Foo.1.0.0-alpha.1.nupkg", "Foo.1.0.0-alpha.1.snupkg"} {
		if w.stored(p) {
			t.Fatalf("%s survived", p)
		}
		trashed, err := w.st.Trash().Find(w.ctx, w.c.Key, p)
		if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != nuget.Deleter {
			t.Fatalf("trash %+v: %v", trashed, err)
		}
	}
	// Nothing more to do: a run deletes nothing.
	w.push("Foo", "2.0.0-rc.2", false)
	w.sweep()
	if len(w.versions("foo")) != 7 {
		t.Fatalf("left %v", w.versions("foo"))
	}
}

// A limit of 1 keeps two, as Artifactory keeps at least two; versions
// deployed at the same moment go in NuGet order; and symbols pushed alone
// are no snapshot, so they are neither counted nor deleted.
func TestMaxUniqueSnapshotsMinimum(t *testing.T) {
	w := newWorld(t, limited(1))
	w.deploy("Foo.1.0.0-rc.1.snupkg", symbols("Foo", "1.0.0-rc.1"), nil)
	at := w.now
	for _, v := range []string{"1.0.0-rc.10", "1.0.0-rc.2", "1.0.0-rc.3"} {
		w.now = at
		_, err := w.tryDeploy(w.c, "Foo."+v+".nupkg", pkg("Foo", v, ""), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	w.sweep()
	want := []string{"1.0.0-rc.1", "1.0.0-rc.10", "1.0.0-rc.3"}
	if got := w.versions("foo"); !slices.Equal(got, want) {
		t.Fatalf("left %v", got)
	}
}

// The limit is read when the run comes: set back to 0 since the pushes
// that marked the package, it deletes nothing. No limit marks nothing, and
// nor does a release, a push to a remote cache or to a federated
// repository.
func TestMaxUniqueSnapshotsLimitRead(t *testing.T) {
	w := newWorld(t, limited(2))
	for _, v := range []string{"1.0.0-a", "1.0.0-b", "1.0.0-c"} {
		w.push("Foo", v, false)
	}
	zero := 0
	w.c.MaxUniqueSnapshots = &zero
	if err := w.st.Repos().Update(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	w.sweep()
	if len(w.versions("foo")) != 3 {
		t.Fatal("a limit of 0 deleted versions")
	}
	none := newWorld(t, nil)
	none.push("Foo", "1.0.0-a", false)
	fed := none.repo("nuget-fed", repo.Federated, limited(2))
	if _, err := none.tryDeploy(
		fed,
		"Foo.1.0.0-a.nupkg",
		pkg("Foo", "1.0.0-a", ""),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	release := newWorld(t, limited(2))
	release.push("Foo", "1.0.0", false)
	if none.marked() != 0 || release.marked() != 0 {
		t.Fatalf("marked %d %d", none.marked(), release.marked())
	}
}

// The cleanup of a repository that is gone, or no longer a NuGet local
// repository, of a package that is gone, or of a kind or key the format
// does not run, owes nothing.
func TestCleanupOwesNothing(t *testing.T) {
	w := newWorld(t, limited(2))
	h := nuget.Handler{}
	env := w.items.Env()
	gen := w.repo("generic-local", repo.Local, nil)
	gen.PackageType = repo.Generic
	if err := w.st.Repos().Update(w.ctx, gen); err != nil {
		t.Fatal(err)
	}
	for _, it := range []types.WorkItem{
		{Kind: nuget.KindSnapshots, Key: "missing/foo"},
		{Kind: nuget.KindSnapshots, Key: "nokey"},
		{Kind: nuget.KindSnapshots, Key: "generic-local/foo"},
		{Kind: nuget.KindSnapshots, Key: "nuget-local/missing"},
		{Kind: "nuget.other", Key: "nuget-local/foo"},
	} {
		if err := h.Work(w.ctx, env, it); err != nil {
			t.Fatalf("%+v: %v", it, err)
		}
	}
	if !slices.Equal(h.WorkKinds(), []string{nuget.KindSnapshots, nuget.KindSymbols}) ||
		h.Owed(w.c, false, "x") != nil {
		t.Fatal("the format's kinds changed")
	}
}
