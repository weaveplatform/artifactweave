package pkgindex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var at = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// indexWorld is a repository with files to link.
type indexWorld struct {
	t   *testing.T
	db  *inmem.Store
	c   repo.Config
	ids map[string]int64
}

func newIndexWorld(t *testing.T, files ...string) *indexWorld {
	t.Helper()
	ctx := context.Background()
	w := &indexWorld{t: t, db: inmem.New(), ids: map[string]int64{}}
	w.c = *repo.Defaults(&repo.Config{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM})
	if err := w.db.Repos().Create(ctx, w.c); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		it, err := w.db.Items().Put(ctx, types.Item{RepoKey: w.c.Key, Name: f})
		if err != nil {
			t.Fatal(err)
		}
		w.ids[f] = it.ID
	}
	return w
}

func (w *indexWorld) files(names ...string) []int64 {
	out := make([]int64, len(names))
	for n, f := range names {
		out[n] = w.ids[f]
	}
	return out
}

// dump renders the repository's index as "pkg@created ver@created/last:meta[files]".
func (w *indexWorld) dump() []string {
	w.t.Helper()
	ctx := context.Background()
	pkgs, _, err := w.db.Packages().List(ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	names := map[int64]string{}
	for f, id := range w.ids {
		names[id] = f
	}
	var out []string
	for _, p := range pkgs {
		out = append(out, fmt.Sprintf("%s@%s", p.Name, p.Created.Format("15:04")))
		vs, err := w.db.Packages().Versions(ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, v := range vs {
			ids, err := w.db.Packages().Files(ctx, v.ID)
			if err != nil {
				w.t.Fatal(err)
			}
			var fs []string
			for _, id := range ids {
				fs = append(fs, names[id])
			}
			slices.Sort(fs)
			out = append(out, fmt.Sprintf(" %s@%s/%s:%s%v", v.Version, v.Created.Format("15:04"),
				v.LastDownloaded.Format("15:04"), v.Metadata, fs))
		}
	}
	return out
}

func (w *indexWorld) reconcile(want ...Indexed) {
	w.t.Helper()
	if err := Reconcile(context.Background(), w.db, w.c, want); err != nil {
		w.t.Fatal(err)
	}
}

func (w *indexWorld) want(lines ...string) {
	w.t.Helper()
	if got := w.dump(); !slices.Equal(got, lines) {
		w.t.Fatalf("index:\n%q\nwant:\n%q", got, lines)
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	w := newIndexWorld(t, "a1.tgz", "a1.json", "a2.tgz", "b1.tgz")
	h := func(n int) time.Time { return at.Add(time.Duration(n) * time.Hour) }
	a := Indexed{Name: "a", Created: h(1), Versions: []IndexedVersion{
		{Version: "2.0.0", Created: h(2), Files: w.files("a2.tgz")},
		{
			Version:  "1.0.0",
			Created:  h(1),
			Metadata: []byte("m1"),
			Files:    w.files("a1.tgz", "a1.json"),
		},
	}}
	b := Indexed{Name: "b", Created: h(3), Versions: []IndexedVersion{
		{Version: "1.0.0", Created: h(3), Files: w.files("b1.tgz")},
	}}
	w.reconcile(b, a)
	full := []string{
		"a@10:00",
		" 1.0.0@10:00/00:00:m1[a1.json a1.tgz]",
		" 2.0.0@11:00/00:00:[a2.tgz]",
		"b@12:00",
		" 1.0.0@12:00/00:00:[b1.tgz]",
	}
	w.want(full...)

	// A reconcile of what is indexed changes nothing, download statistics
	// included.
	pa, _ := w.db.Packages().Find(ctx, w.c.Key, "a")
	v1, _ := w.db.Packages().Version(ctx, pa.ID, "1.0.0")
	if err := RecordDownload(ctx, w.db, w.ids["a1.tgz"], h(5)); err != nil {
		t.Fatal(err)
	}
	w.reconcile(a, b)
	full[1] = " 1.0.0@10:00/14:00:m1[a1.json a1.tgz]"
	w.want(full...)
	if again, _ := w.db.Packages().Version(ctx, pa.ID, "1.0.0"); again.ID != v1.ID {
		t.Fatalf("an unchanged version was recreated: %d, was %d", again.ID, v1.ID)
	}

	// A package's date stays its first push's; a version's follows the
	// tree, as do its metadata and files. A version linking a file the tree
	// no longer gives it is recreated with its statistics.
	a.Created = h(9)
	a.Versions[1].Created, a.Versions[1].Metadata = h(4), []byte("m2")
	a.Versions[1].Files = w.files("a1.tgz")
	w.reconcile(a, b)
	w.want(
		"a@10:00",
		" 1.0.0@13:00/14:00:m2[a1.tgz]",
		" 2.0.0@11:00/00:00:[a2.tgz]",
		"b@12:00",
		" 1.0.0@12:00/00:00:[b1.tgz]",
	)

	// What the tree no longer backs goes: a version, then a package.
	a.Versions = a.Versions[1:]
	w.reconcile(a, b)
	w.want("a@10:00", " 1.0.0@13:00/14:00:m2[a1.tgz]", "b@12:00", " 1.0.0@12:00/00:00:[b1.tgz]")
	w.reconcile(b)
	w.want("b@12:00", " 1.0.0@12:00/00:00:[b1.tgz]")
	w.reconcile()
	w.want()

	// A file in no version records nothing.
	if err := RecordDownload(ctx, w.db, w.ids["a2.tgz"], h(6)); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilePages(t *testing.T) {
	var files []string
	for n := range pageSize + 3 {
		files = append(files, fmt.Sprintf("f%03d", n))
	}
	w := newIndexWorld(t, files...)
	var want []Indexed
	for n, f := range files {
		want = append(
			want,
			Indexed{Name: fmt.Sprintf("p%03d", n), Created: at, Versions: []IndexedVersion{
				{Version: "1", Created: at, Files: w.files(f)},
			}},
		)
	}
	w.reconcile(want...)
	w.reconcile(want[:1]...)
	w.want("p000@09:00", " 1@09:00/00:00:[f000]")
}

func (f failingPackages) List(
	ctx context.Context,
	repoKey string,
	p store.Page,
) ([]types.Package, string, error) {
	if err := f.err("list"); err != nil {
		return nil, "", err
	}
	return f.PackageStore.List(ctx, repoKey, p)
}

// PutVersion fails as "putVersion" always, and as "rePut" only when it
// recreates a version it was given back.
func (f failingPackages) PutVersion(
	ctx context.Context,
	v types.PackageVersion,
) (types.PackageVersion, error) {
	if f.fail == "putVersion" || f.fail == "rePut" && v.ID != 0 {
		return types.PackageVersion{}, errBoom
	}
	return f.PackageStore.PutVersion(ctx, v)
}

func (f failingPackages) LinkFile(ctx context.Context, v types.VersionFile) error {
	if err := f.err("linkFile"); err != nil {
		return err
	}
	return f.PackageStore.LinkFile(ctx, v)
}

func (f failingPackages) RecordVersionDownload(ctx context.Context, id int64, t time.Time) error {
	if err := f.err("recordVersionDownload"); err != nil {
		return err
	}
	return f.PackageStore.RecordVersionDownload(ctx, id, t)
}

// Every store failure surfaces, from whichever step meets it.
func TestReconcileFaults(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{
		"list", "delete", "versions", "deleteVersion", "ensure", "version", "putVersion",
		"rePut", "files", "linkFile",
	} {
		w := newIndexWorld(t, "a.tgz", "b.tgz", "c.tgz")
		w.reconcile(
			Indexed{Name: "a", Versions: []IndexedVersion{
				{Version: "1", Files: w.files("a.tgz", "b.tgz")}, {Version: "2"},
			}},
			Indexed{Name: "gone"},
		)
		// Dropping "gone" and version 2, then relinking version 1 without
		// b.tgz and adding c.tgz, reaches every call.
		want := Indexed{Name: "a", Versions: []IndexedVersion{
			{Version: "1", Created: at, Files: w.files("a.tgz", "c.tgz")},
		}}
		if err := Reconcile(
			ctx,
			failing{w.db, fail},
			w.c,
			[]Indexed{want},
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("%s: %v", fail, err)
		}
	}
	w := newIndexWorld(t, "a.tgz")
	w.reconcile(
		Indexed{Name: "a", Versions: []IndexedVersion{{Version: "1", Files: w.files("a.tgz")}}},
	)
	for _, fail := range []string{"versionOf", "recordVersionDownload"} {
		if err := RecordDownload(
			ctx,
			failing{w.db, fail},
			w.ids["a.tgz"],
			at,
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("%s: %v", fail, err)
		}
	}
}
