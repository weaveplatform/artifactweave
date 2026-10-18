package registry_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tagRow is one version of an image in the index as a test compares it.
type tagRow struct {
	Order   int64
	Created time.Time
	Files   []string
}

// imageRow is one package of the index.
type imageRow struct {
	Type    repo.PackageType
	Created time.Time
	Tags    map[string]tagRow
}

// index reads c's package index: each image with its tags, each tag's
// order, push time and file paths.
func (w *world) index(c repo.Config) map[string]imageRow {
	w.t.Helper()
	ctx := context.Background()
	pkgs, _, err := w.db.Packages().List(ctx, c.Key, store.Page{Limit: 1000})
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]imageRow{}
	for _, p := range pkgs {
		row := imageRow{Type: p.Type, Created: p.Created, Tags: map[string]tagRow{}}
		vs, err := w.db.Packages().Versions(ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, v := range vs {
			ids, err := w.db.Packages().Files(ctx, v.ID)
			if err != nil {
				w.t.Fatal(err)
			}
			var files []string
			for _, id := range ids {
				it, err := w.db.Items().GetByID(ctx, id)
				if err != nil {
					w.t.Fatal(err)
				}
				files = append(files, it.FullPath())
			}
			slices.Sort(files)
			row.Tags[v.Version] = tagRow{Order: v.OrderKey, Created: v.Created, Files: files}
		}
		out[p.Name] = row
	}
	return out
}

// tags lists the image's tags in the index's order, oldest push first.
func (w *world) tags(c repo.Config, name string) []string {
	w.t.Helper()
	ctx := context.Background()
	p, err := w.db.Packages().Find(ctx, c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	vs, err := w.db.Packages().Versions(ctx, p.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, v := range vs {
		out = append(out, v.Version)
	}
	return out
}

// clock is the service's time, which a test moves between pushes.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

// at sets the service's clock to minute m after start.
func (w *world) at(c *clock, m int) { c.at = start.Add(time.Duration(m) * time.Minute) }

// image is a manifest of imageConfig and one layer of its own, with the
// blobs uploaded to name in c.
func (w *world) image(c repo.Config, name, layer string) (string, string) {
	w.t.Helper()
	for _, b := range []string{imageConfig, layer} {
		w.deploy(c, layout.UploadPath(name, sum(b)), b, start)
	}
	return manifestOf(layer)
}

// pushTo stores body as the manifest of name in c's folders.
func (w *world) pushTo(c repo.Config, name, body, digest string, folders ...string) {
	w.t.Helper()
	m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(body), digest)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.svc.PutManifest(context.Background(), registry.Push{
		Owner:    registry.Owner{Repo: c, Name: name, By: "alice"},
		Manifest: m, Body: []byte(body), Folders: folders,
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) itemsAt(c *clock) *item.Service {
	return &item.Service{Store: w.db, Blobs: w.blobs, Now: c.now}
}

func (w *world) remove(c *clock, r repo.Config, path string) {
	w.t.Helper()
	if _, err := w.itemsAt(c).Delete(context.Background(), item.Target{Repo: r, Path: path},
		"alice"); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) transfer(c *clock, from, to item.Target, move bool) {
	w.t.Helper()
	res, err := w.itemsAt(c).Transfer(context.Background(), item.Transfer{
		From: from, To: to, Move: move, FailFast: true, By: "alice",
	})
	if err != nil || res.Failed > 0 {
		w.t.Fatalf("%+v %v", res, err)
	}
}

func files(name, folder string, blobs ...string) []string {
	out := []string{name + "/" + folder + "/manifest.json"}
	for _, b := range blobs {
		out = append(out, name+"/"+folder+"/sha256__"+sum(b))
	}
	slices.Sort(out)
	return out
}

// A push makes the image a package and the tag a version whose files are
// the tag folder's, dated by the push; a push by digest keeps an image a
// package without adding a version; finished uploads are no files of any.
func TestPushIndexesTheTag(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		w.at(c, 1)
		body, digest := w.image(w.docker, "team/app", layerA)
		w.pushTo(w.docker, "team/app", body, digest, "v1")
		w.at(c, 2)
		other, otherDigest := w.image(w.docker, "only/digests", layerB)
		w.pushTo(w.docker, "only/digests", other, otherDigest, layout.DigestFolder(otherDigest))

		want := map[string]imageRow{
			"team/app": {
				Type:    repo.Docker,
				Created: start.Add(time.Minute),
				Tags: map[string]tagRow{
					"v1": {
						Order: 1, Created: start.Add(time.Minute),
						Files: files("team/app", "v1", imageConfig, layerA),
					},
				},
			},
			"only/digests": {
				Type: repo.Docker, Created: start.Add(2 * time.Minute),
				Tags: map[string]tagRow{},
			},
		}
		if diff := cmp.Diff(want, w.index(w.docker)); diff != "" {
			t.Fatal(diff)
		}
	})
}

// Pushing a different manifest over a tag moves it to the newest place and
// gives it the new manifest's files; pushing the same manifest again
// changes nothing.
func TestOverwriteMovesTheTagToTheNewestPlace(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		first, firstDigest := w.image(w.docker, "app", layerA)
		second, secondDigest := w.image(w.docker, "app", layerB)
		w.at(c, 1)
		w.pushTo(w.docker, "app", first, firstDigest, "v1")
		w.at(c, 2)
		w.pushTo(w.docker, "app", first, firstDigest, "v2")
		w.at(c, 3)
		w.pushTo(w.docker, "app", first, firstDigest, "v1")
		if diff := cmp.Diff([]string{"v1", "v2"}, w.tags(w.docker, "app")); diff != "" {
			t.Fatalf("the same manifest moved its tag:\n%s", diff)
		}
		w.at(c, 4)
		w.pushTo(w.docker, "app", second, secondDigest, "v1")
		if diff := cmp.Diff([]string{"v2", "v1"}, w.tags(w.docker, "app")); diff != "" {
			t.Fatal(diff)
		}
		got := w.index(w.docker)["app"].Tags["v1"]
		want := tagRow{
			Order: 2, Created: start.Add(4 * time.Minute),
			Files: files("app", "v1", imageConfig, layerB),
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatal(diff)
		}
	})
}

// A tag is a version while its folder holds a manifest, and an image a
// package while it holds any manifest, whatever is deleted around them.
func TestDeletesKeepTheIndex(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		body, digest := w.image(w.docker, "app", layerA)
		w.at(c, 1)
		w.pushTo(w.docker, "app", body, digest, "v1", "v2", "v3", layout.DigestFolder(digest))

		// A layer alone leaves the tag with one file fewer.
		w.remove(c, w.docker, "app/v1/sha256__"+sum(layerA))
		if got := w.index(w.docker)["app"].Tags["v1"].Files; !slices.Equal(
			got, files("app", "v1", imageConfig)) {
			t.Fatalf("v1 files %v", got)
		}
		// The manifest alone ends the tag, though its blobs remain.
		w.remove(c, w.docker, "app/v2/manifest.json")
		// A whole tag folder ends it too.
		w.remove(c, w.docker, "app/v3")
		if diff := cmp.Diff([]string{"v1"}, w.tags(w.docker, "app")); diff != "" {
			t.Fatal(diff)
		}
		// The last tag goes; the manifest pushed by digest keeps the
		// image a package.
		w.remove(c, w.docker, "app/v1")
		if row, ok := w.index(w.docker)["app"]; !ok || len(row.Tags) != 0 {
			t.Fatalf("app %+v %v", row, ok)
		}
		// Its last manifest goes, and so does the package.
		w.remove(c, w.docker, "app/"+layout.DigestFolder(digest))
		if got := w.index(w.docker); len(got) != 0 {
			t.Fatalf("index %+v", got)
		}
	})
}

// Copies, moves and restores land files one at a time, the manifest
// before or after its blobs; each leaves the index as a push would.
func TestCopiesMovesAndRestoresKeepTheIndex(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		c := &clock{}
		w.svc.Now = c.now
		mirror := w.repoOf(&repo.Config{
			Key: "docker-mirror", Class: repo.Local,
			PackageType: repo.Docker,
		})
		body, digest := w.image(w.docker, "app", layerA)
		w.at(c, 1)
		w.pushTo(w.docker, "app", body, digest, "v1")

		w.at(c, 2)
		w.transfer(c, item.Target{Repo: w.docker, Path: "app/v1"},
			item.Target{Repo: mirror, Path: "app/v1"}, false)
		w.at(c, 3)
		w.transfer(c, item.Target{Repo: w.docker, Path: "app"},
			item.Target{Repo: w.docker, Path: "moved"}, true)
		// A copy keeps its files' times, as Artifactory 7.x does by default
		// (#349), so the copied tag is dated by the push.
		got := w.index(mirror)["app"].Tags["v1"]
		if !got.Created.Equal(start.Add(time.Minute)) ||
			!slices.Equal(got.Files, files("app", "v1", imageConfig, layerA)) {
			t.Fatalf("copy %+v", got)
		}
		idx := w.index(w.docker)
		if _, ok := idx["app"]; ok {
			t.Fatalf("the moved image stays indexed: %+v", idx)
		}
		if got := idx["moved"].Tags["v1"]; !got.Created.Equal(start.Add(time.Minute)) ||
			!slices.Equal(got.Files, files("moved", "v1", imageConfig, layerA)) {
			t.Fatalf("move keeps the push time and the files: %+v", got)
		}

		w.at(c, 4)
		w.remove(c, w.docker, "moved")
		if got := w.index(w.docker); len(got) != 0 {
			t.Fatalf("index %+v", got)
		}
		bin := trash.Service{Store: w.db, Items: w.itemsAt(c)}
		if _, err := bin.Restore(ctx, w.docker.Key, "moved",
			trash.Destination{Repo: w.docker, Path: "moved"}, "alice"); err != nil {
			t.Fatal(err)
		}
		if got := w.index(w.docker)["moved"].Tags["v1"]; !got.Created.Equal(
			start.Add(time.Minute),
		) ||
			len(got.Files) != 3 {
			t.Fatalf("restore %+v", got)
		}
	})
}

// A reindex rebuilds from the item tree the index the hooks kept.
func TestReindexRebuildsTheSameIndex(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		c := &clock{}
		w.svc.Now = c.now
		first, firstDigest := w.image(w.docker, "app", layerA)
		second, secondDigest := w.image(w.docker, "app", layerB)
		w.at(c, 1)
		w.pushTo(w.docker, "app", first, firstDigest, "v1", "latest")
		w.at(c, 2)
		w.pushTo(w.docker, "app", second, secondDigest, "v2", layout.DigestFolder(secondDigest))
		w.at(c, 3)
		w.pushTo(w.docker, "app", second, secondDigest, "latest")
		w.at(c, 4)
		w.transfer(c, item.Target{Repo: w.docker, Path: "app/v2"},
			item.Target{Repo: w.docker, Path: "team/app/v2"}, false)
		w.at(c, 5)
		w.remove(c, w.docker, "app/v1/manifest.json")
		// Blobs in a folder that holds no manifest, and a stray file, are
		// no tags.
		w.deploy(w.docker, "app/notes/readme.txt", "hello", start)
		w.deploy(w.docker, "top.txt", "hello", start)

		before := w.index(w.docker)
		if len(before) != 2 || len(before["app"].Tags) != 2 {
			t.Fatalf("index %+v", before)
		}
		if err := (layout.Handler{}).Reindex(ctx, format.Env{Store: w.db}, w.docker); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(before, w.index(w.docker)); diff != "" {
			t.Fatalf("the hooks' index changed:\n%s", diff)
		}

		// Damage: a version with no tag folder, a link to a file outside
		// its folder, and a package lost.
		app, err := w.db.Packages().Find(ctx, w.docker.Key, "app")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Packages().PutVersion(ctx, types.PackageVersion{
			PackageID: app.ID, Version: "ghost", Created: start,
		}); err != nil {
			t.Fatal(err)
		}
		v2, err := w.db.Packages().Version(ctx, app.ID, "v2")
		if err != nil {
			t.Fatal(err)
		}
		stray, err := w.item(w.docker, "app/notes/readme.txt")
		if err != nil {
			t.Fatal(err)
		}
		if err := w.db.Packages().LinkFile(ctx, types.VersionFile{
			VersionID: v2.ID, ItemID: stray.ID,
		}); err != nil {
			t.Fatal(err)
		}
		team, err := w.db.Packages().Find(ctx, w.docker.Key, "team/app")
		if err != nil {
			t.Fatal(err)
		}
		if err := w.db.Packages().Delete(ctx, team.ID); err != nil {
			t.Fatal(err)
		}
		if err := (layout.Handler{}).Reindex(ctx, format.Env{Store: w.db}, w.docker); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(before, w.index(w.docker)); diff != "" {
			t.Fatalf("the repaired index:\n%s", diff)
		}

		// From nothing, an image is dated by its earliest manifest, here
		// the one pushed by digest after v1's manifest went.
		for _, name := range []string{"app", "team/app"} {
			p, err := w.db.Packages().Find(ctx, w.docker.Key, name)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.db.Packages().Delete(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := (layout.Handler{}).Reindex(ctx, format.Env{Store: w.db}, w.docker); err != nil {
			t.Fatal(err)
		}
		want := before
		row := want["app"]
		row.Created = start.Add(2 * time.Minute)
		want["app"] = row
		if diff := cmp.Diff(want, w.index(w.docker)); diff != "" {
			t.Fatalf("rebuilt from nothing:\n%s", diff)
		}

		// A tree without images leaves no index.
		w.remove(c, w.docker, "app")
		w.remove(c, w.docker, "team")
		if err := (layout.Handler{}).Reindex(ctx, format.Env{Store: w.db}, w.docker); err != nil {
			t.Fatal(err)
		}
		if got := w.index(w.docker); len(got) != 0 {
			t.Fatalf("index %+v", got)
		}
	})
}

// repoOf creates the repository c and returns it as stored.
func (w *world) repoOf(c *repo.Config) repo.Config {
	w.t.Helper()
	if err := w.db.Repos().Create(context.Background(), *repo.Defaults(c)); err != nil {
		w.t.Fatal(err)
	}
	return w.repo(c.Key)
}
