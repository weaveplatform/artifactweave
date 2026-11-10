package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

var errIndex = errors.New("index boom")

// faulty fails the package and item store methods its maps name and
// delegates the rest, transactions included.
type faulty struct {
	format.Store
	pkgs, items map[string]bool
}

func (f *faulty) Packages() port.PackageStore {
	return faultyPackages{f.Store.Packages(), f.pkgs}
}

func (f *faulty) Items() port.ItemStore { return faultyReads{f.Store.Items(), f.items} }

type faultyPackages struct {
	port.PackageStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, v model.Package) (model.Package, error) {
	if p.fail["Ensure"] {
		return model.Package{}, errIndex
	}
	return p.PackageStore.Ensure(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Find(ctx context.Context, repoKey, name string) (model.Package, error) {
	if p.fail["Find"] {
		return model.Package{}, errIndex
	}
	return p.PackageStore.Find(ctx, repoKey, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) List(
	ctx context.Context, repoKey string, pg kernel.Page,
) ([]model.Package, string, error) {
	if p.fail["List"] {
		return nil, "", errIndex
	}
	return p.PackageStore.List(ctx, repoKey, pg)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Delete(ctx context.Context, id int64) error {
	if p.fail["Delete"] {
		return errIndex
	}
	return p.PackageStore.Delete(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) PutVersion(
	ctx context.Context, v model.PackageVersion,
) (model.PackageVersion, error) {
	if p.fail["PutVersion"] {
		return model.PackageVersion{}, errIndex
	}
	return p.PackageStore.PutVersion(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context, id int64, v string,
) (model.PackageVersion, error) {
	if p.fail["Version"] {
		return model.PackageVersion{}, errIndex
	}
	return p.PackageStore.Version(ctx, id, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	if p.fail["Versions"] {
		return nil, errIndex
	}
	return p.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) DeleteVersion(ctx context.Context, id int64) error {
	if p.fail["DeleteVersion"] {
		return errIndex
	}
	return p.PackageStore.DeleteVersion(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) LinkFile(ctx context.Context, f model.VersionFile) error {
	if p.fail["LinkFile"] {
		return errIndex
	}
	return p.PackageStore.LinkFile(ctx, f)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if p.fail["Files"] {
		return nil, errIndex
	}
	return p.PackageStore.Files(ctx, id)
}

type faultyReads struct {
	port.ItemStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (r faultyReads) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (model.Item, error) {
	if r.fail["Get"] {
		return model.Item{}, errIndex
	}
	return r.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (r faultyReads) Children(
	ctx context.Context, key string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	if r.fail["Children"] {
		return nil, "", errIndex
	}
	return r.ItemStore.Children(ctx, key, cached, folder, p)
}

//nolint:wrapcheck // test double
func (r faultyReads) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if r.fail["Search"] {
		return nil, errIndex
	}
	return r.ItemStore.Search(ctx, q, o)
}

// indexed is a Docker repository whose image app has the tag v1 (a
// manifest and a blob) and a manifest pushed by digest, and whose image
// fresh has a tag the index has lost.
type indexed struct {
	db                       format.Store
	c                        repo.Config
	items                    *item.Service
	manifest, blob, byDigest model.Item
	freshManifest, stray     model.Item
}

func newIndexed(t *testing.T) *indexed {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	x := &indexed{db: db, c: c, items: &item.Service{
		Store: db, Blobs: blobs,
		Now:     func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC) },
		Formats: ecosystem.For,
	}}
	deploy := func(path, body string) model.Item {
		t.Helper()
		it, err := x.items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
		})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	x.manifest = deploy("app/v1/manifest.json", "m1")
	x.blob = deploy("app/v1/sha256__"+sum("b"), "b")
	x.byDigest = deploy("app/"+layout.DigestFolder(sum("m2"))+"/manifest.json", "m2")
	x.stray = deploy("app/notes/readme.txt", "hello")
	x.freshManifest = deploy("fresh/v1/manifest.json", "m3")
	p, err := db.Packages().Find(ctx, c.Key, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Packages().Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	return x
}

// strayLink links a file from outside v1's folder into its version.
func (x *indexed) strayLink(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	p, err := x.db.Packages().Find(ctx, x.c.Key, "app")
	if err != nil {
		t.Fatal(err)
	}
	v, err := x.db.Packages().Version(ctx, p.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.db.Packages().LinkFile(ctx, model.VersionFile{
		VersionID: v.ID, ItemID: x.stray.ID,
	}); err != nil {
		t.Fatal(err)
	}
}

// removeQuietly deletes path from the tree without the item service, so
// no hook has run when the test calls one.
func (x *indexed) removeQuietly(t *testing.T, path string) {
	t.Helper()
	if _, err := x.db.Items().Delete(context.Background(), x.c.Key, false, path); err != nil {
		t.Fatal(err)
	}
}

// Every store failure in the index hooks and the reindex comes back to the
// caller, which rolls its transaction back.
func TestUnitIndexFailures(t *testing.T) {
	h := layout.Handler{}
	deployed := func(pick func(x *indexed) model.Item) func(*indexed, format.Store) error {
		return func(x *indexed, st format.Store) error {
			return h.Deployed(context.Background(), format.Env{Store: st}, x.c, pick(x))
		}
	}
	manifest := deployed(func(x *indexed) model.Item { return x.manifest })
	fresh := deployed(func(x *indexed) model.Item { return x.freshManifest })
	deleted := func(x *indexed, st format.Store) error {
		return h.Deleted(
			context.Background(),
			format.Env{Store: st},
			x.c,
			[]model.Item{x.manifest, x.byDigest},
		)
	}
	reindex := func(x *indexed, st format.Store) error {
		return h.Reindex(context.Background(), format.Env{Store: st}, x.c)
	}
	noManifest := func(t *testing.T, x *indexed) { x.removeQuietly(t, "app/v1/manifest.json") }
	noManifests := func(t *testing.T, x *indexed) {
		x.removeQuietly(t, "app/v1/manifest.json")
		x.removeQuietly(t, x.byDigest.FullPath())
	}
	cases := []struct {
		name        string
		setup       func(*testing.T, *indexed)
		pkgs, items []string
		run         func(*indexed, format.Store) error
	}{
		{name: "tag manifest: get", items: []string{"Get"}, run: manifest},
		{name: "tag manifest: find", pkgs: []string{"Find"}, run: manifest},
		{name: "tag manifest: version", pkgs: []string{"Version"}, run: manifest},
		{name: "tag manifest: children", items: []string{"Children"}, run: manifest},
		{name: "tag manifest: files", pkgs: []string{"Files"}, run: manifest},
		{name: "tag manifest: link", pkgs: []string{"LinkFile"}, run: manifest},
		{
			name: "relink: delete version", pkgs: []string{"DeleteVersion"},
			setup: func(t *testing.T, x *indexed) { x.strayLink(t) }, run: manifest,
		},
		{
			name: "relink: put version", pkgs: []string{"PutVersion"},
			setup: func(t *testing.T, x *indexed) { x.strayLink(t) }, run: manifest,
		},
		{name: "new image: search", items: []string{"Search"}, run: fresh},
		{name: "new image: ensure", pkgs: []string{"Ensure"}, run: fresh},
		{
			name: "new image: put version", pkgs: []string{"PutVersion"},
			setup: func(t *testing.T, x *indexed) {
				ctx := context.Background()
				if _, err := x.db.Packages().Ensure(ctx, model.Package{
					RepoKey: x.c.Key, Name: "fresh", Type: repo.Docker,
				}); err != nil {
					t.Fatal(err)
				}
			}, run: fresh,
		},
		{
			name: "blob: find", pkgs: []string{"Find"},
			run: deployed(func(x *indexed) model.Item { return x.blob }),
		},
		{
			name: "blob: version", pkgs: []string{"Version"},
			run: deployed(func(x *indexed) model.Item { return x.blob }),
		},
		{
			name: "digest manifest: find", pkgs: []string{"Find"},
			run: deployed(func(x *indexed) model.Item { return x.byDigest }),
		},
		{name: "deleted tag: find", pkgs: []string{"Find"}, setup: noManifest, run: deleted},
		{name: "deleted tag: version", pkgs: []string{"Version"}, setup: noManifest, run: deleted},
		{
			name: "deleted tag: delete version", pkgs: []string{"DeleteVersion"},
			setup: noManifest, run: deleted,
		},
		{name: "deleted image: search", items: []string{"Search"}, setup: noManifest, run: deleted},
		{
			name: "deleted image: delete package", pkgs: []string{"Delete"},
			setup: noManifests, run: deleted,
		},
		{name: "reindex: search", items: []string{"Search"}, run: reindex},
		{name: "reindex: list", pkgs: []string{"List"}, run: reindex},
		{name: "reindex: versions", pkgs: []string{"Versions"}, run: reindex},
		{
			name:  "reindex: drop version",
			pkgs:  []string{"DeleteVersion"},
			setup: noManifest,
			run:   reindex,
		},
		{name: "reindex: drop package", pkgs: []string{"Delete"}, setup: noManifests, run: reindex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newIndexed(t)
			if tc.setup != nil {
				tc.setup(t, x)
			}
			f := &faulty{Store: x.db, pkgs: map[string]bool{}, items: map[string]bool{}}
			for _, m := range tc.pkgs {
				f.pkgs[m] = true
			}
			for _, m := range tc.items {
				f.items[m] = true
			}
			if err := tc.run(x, f); !errors.Is(err, errIndex) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A manifest whose time was never set is dated by its creation.
func TestUnitPushTime(t *testing.T) {
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	if got := layout.PushTime(model.Item{Created: at}); !got.Equal(at) {
		t.Fatal(got)
	}
	if got := layout.PushTime(model.Item{Created: at, Modified: at.Add(time.Hour)}); !got.Equal(
		at.Add(time.Hour)) {
		t.Fatal(got)
	}
}
