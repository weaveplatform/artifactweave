package docker_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

// images is a store with a local and a remote Docker repository whose items
// go through the item service, on a clock that moves a minute on every
// write: a push stores in the local repository, and a download through the
// remote stores in its cache.
type images struct {
	t             *testing.T
	db            format.Store
	items         *item.Service
	local, remote repo.Config
	now           time.Time
}

func newImages(t *testing.T) *images {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	x := &images{t: t, db: db, now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	x.local = *repo.Defaults(&repo.Config{
		Key: "docker-local", Class: repo.Local, PackageType: repo.Docker,
	})
	x.remote = *repo.Defaults(&repo.Config{
		Key: "docker-remote", Class: repo.Remote, URL: "https://registry.example",
		PackageType: repo.Docker,
	})
	for _, c := range []repo.Config{x.local, x.remote} {
		if err := db.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	x.items = &item.Service{
		Store: db, Blobs: blobs, Formats: ecosystem.For,
		Now: func() time.Time { return x.now },
	}
	return x
}

// write stores body at path in c: in a remote repository's cache, as a
// download through it does.
func (x *images) write(c repo.Config, path, body string) model.Item {
	x.t.Helper()
	x.now = x.now.Add(time.Minute)
	it, err := x.items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: c, Cached: c.Class == repo.Remote, Path: path},
		Body:   strings.NewReader(body),
	})
	if err != nil {
		x.t.Fatal(err)
	}
	return it
}

// remove deletes the item at path from c, or from its cache for a remote.
func (x *images) remove(c repo.Config, path string) {
	x.t.Helper()
	x.now = x.now.Add(time.Minute)
	if _, err := x.items.Delete(
		context.Background(),
		item.Target{Repo: c, Cached: c.Class == repo.Remote, Path: path},
		"test",
	); err != nil {
		x.t.Fatal(err)
	}
}

func (x *images) reindex(c repo.Config) {
	x.t.Helper()
	if err := x.items.Reindex(context.Background(), c); err != nil {
		x.t.Fatal(err)
	}
}

// forget drops the image name from c's index and leaves its items alone.
func (x *images) forget(c repo.Config, name string) {
	x.t.Helper()
	ctx := context.Background()
	p, err := x.db.Packages().Find(ctx, c.Key, name)
	if err != nil {
		x.t.Fatal(err)
	}
	if err := x.db.Packages().Delete(ctx, p.ID); err != nil {
		x.t.Fatal(err)
	}
}

// indexedTag is a tag as the index holds it.
type indexedTag struct {
	Created time.Time
	// Files name each linked item as a client sees it, a cached one under
	// its repository's -cache key.
	Files []string
}

// indexedImage is an image as the index holds it.
type indexedImage struct {
	Created time.Time
	Tags    map[string]indexedTag
}

// index reads c's whole package index, with files named by path so that
// it compares across a reindex, which may recreate a tag under a new ID.
func (x *images) index(c repo.Config) map[string]indexedImage {
	x.t.Helper()
	ctx := context.Background()
	pkgs, _, err := x.db.Packages().List(ctx, c.Key, kernel.Page{Limit: kernel.MaxLimit})
	if err != nil {
		x.t.Fatal(err)
	}
	out := map[string]indexedImage{}
	for _, p := range pkgs {
		img := indexedImage{Created: p.Created, Tags: map[string]indexedTag{}}
		vs, err := x.db.Packages().Versions(ctx, p.ID)
		if err != nil {
			x.t.Fatal(err)
		}
		for _, v := range vs {
			ids, err := x.db.Packages().Files(ctx, v.ID)
			if err != nil {
				x.t.Fatal(err)
			}
			tag := indexedTag{Created: v.Created}
			for _, id := range ids {
				it, err := x.db.Items().GetByID(ctx, id)
				if err != nil {
					x.t.Fatal(err)
				}
				tag.Files = append(tag.Files, it.ReportedRepoKey()+"/"+it.FullPath())
			}
			slices.Sort(tag.Files)
			img.Tags[v.Version] = tag
		}
		out[p.Name] = img
	}
	return out
}

// A cached image's package is dated by its earliest cached manifest, both
// when its first manifest is cached and when the index lost it since.
func TestUnitCachedImageIsDatedByItsEarliestManifest(t *testing.T) {
	x := newImages(t)
	x.write(x.remote, "app/_uploads/sha256__"+sum("layer"), "layer")
	first := x.write(x.remote, "app/v1/manifest.json", "m1")
	if got := x.index(x.remote)["app"].Created; !got.Equal(first.Created) {
		t.Fatalf("app is dated %v, want its first manifest's %v", got, first.Created)
	}
	x.forget(x.remote, "app")
	x.write(x.remote, "app/v2/manifest.json", "m2")
	if got := x.index(x.remote)["app"].Created; !got.Equal(first.Created) {
		t.Fatalf("app indexed again is dated %v, want its earliest manifest's %v",
			got, first.Created)
	}
}

// Deleting a cached tag keeps its image a package while another cached tag
// of it remains, and the image's last manifest takes the package with it.
func TestUnitDeletingACachedTagKeepsItsImage(t *testing.T) {
	x := newImages(t)
	x.write(x.remote, "app/v1/manifest.json", "m1")
	x.write(x.remote, "app/v2/manifest.json", "m2")
	x.remove(x.remote, "app/v2/manifest.json")
	img, ok := x.index(x.remote)["app"]
	if !ok {
		t.Fatal("deleting v2 dropped app, whose v1 is still cached")
	}
	if _, ok := img.Tags["v1"]; !ok || len(img.Tags) != 1 {
		t.Fatalf("app's tags are %v, want v1 alone", img.Tags)
	}
	x.remove(x.remote, "app/v1/manifest.json")
	if img, ok := x.index(x.remote)["app"]; ok {
		t.Fatalf("app is still a package with no manifest cached: %v", img)
	}
}

// A reindex changes nothing in the index the hooks kept, and rebuilds the
// same index from an empty one, each image dated by its earliest manifest:
// a remote repository's from its cache, a local one's from its own items.
func TestUnitReindexKeepsTheHooksIndex(t *testing.T) {
	x := newImages(t)
	digest := layout.DigestFolder(sum("m2"))
	x.write(x.local, "app/v1/sha256__"+sum("layer"), "layer")
	x.write(x.local, "app/v1/manifest.json", "m1")
	x.write(x.local, "app/"+digest+"/manifest.json", "m2")
	x.write(x.local, "team/web/latest/manifest.json", "m3")
	x.write(x.remote, "app/_uploads/sha256__"+sum("layer"), "layer")
	x.write(x.remote, "app/v1/manifest.json", "m1")
	x.write(x.remote, "app/"+digest+"/manifest.json", "m2")
	x.write(x.remote, "app/v2/manifest.json", "m4")
	x.write(x.remote, "team/web/latest/manifest.json", "m3")
	for _, c := range []repo.Config{x.local, x.remote} {
		kept := x.index(c)
		if len(kept) != 2 {
			t.Fatalf("%s: the hooks indexed %v, want app and team/web", c.Key, kept)
		}
		x.reindex(c)
		if diff := cmp.Diff(kept, x.index(c)); diff != "" {
			t.Errorf("%s: a reindex changed the hooks' index (-kept +reindexed):\n%s", c.Key, diff)
		}
		for name := range kept {
			x.forget(c, name)
		}
		x.reindex(c)
		if diff := cmp.Diff(kept, x.index(c)); diff != "" {
			t.Errorf("%s: a reindex from empty differs from the hooks' index (-kept +rebuilt):\n%s",
				c.Key, diff)
		}
	}
}
