package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// exists reports whether path is in docker-local.
func (w *world) exists(path string) bool {
	w.t.Helper()
	_, err := w.item(w.docker, path)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

// A tag delete removes the tag folder, into the trash can, and leaves the
// manifest in any other folder holding it.
func TestAccDeleteTag(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		w.upload(imageConfig, layerA)
		body, digest := manifestOf(layerA)
		if err := w.push(body, digest, "v1", "v2"); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.DeleteTag(ctx, w.owner("app"), "v1"); err != nil {
			t.Fatal(err)
		}
		if w.exists("app/v1") || !w.exists("app/v2/manifest.json") {
			t.Fatal("the wrong folder went")
		}
		trashed, _, err := w.db.Trash().List(ctx, "docker-local", "app/v1", store.Page{})
		if err != nil || len(trashed) == 0 || trashed[0].DeletedBy != "alice" {
			t.Fatalf("trash: %v %v", trashed, err)
		}
		for _, folder := range []string{"v1", "v9", "_uploads"} {
			if err := w.svc.DeleteTag(ctx, w.owner("app"), folder); !errors.Is(
				err, store.ErrNotFound,
			) {
				t.Errorf("%s: %v", folder, err)
			}
		}
		if !w.exists("app/_uploads") {
			t.Fatal("the uploads went")
		}
	})
}

// A digest delete removes every folder holding the manifest in one go.
func TestAccDeleteManifest(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		w.upload(imageConfig, layerA, layerB)
		body, digest := manifestOf(layerA)
		if err := w.push(body, digest, "v1", layout.DigestFolder(digest)); err != nil {
			t.Fatal(err)
		}
		other, otherDigest := manifestOf(layerB)
		if err := w.push(other, otherDigest, "v2"); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.DeleteManifest(ctx, w.owner("app"), digest); err != nil {
			t.Fatal(err)
		}
		byDigest := "app/" + layout.DigestFolder(digest)
		if w.exists("app/v1") || w.exists(byDigest) || !w.exists("app/v2") {
			t.Fatal("the wrong folders went")
		}
		if err := w.svc.DeleteManifest(ctx, w.owner("app"), digest); !errors.Is(
			err, store.ErrNotFound,
		) {
			t.Fatalf("again: %v", err)
		}
	})
}

func TestAccDeleteStoreFailures(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	w := newWorld(t, db)
	w.upload(imageConfig, layerA)
	body, digest := manifestOf(layerA)
	if err := w.push(body, digest, "v1"); err != nil {
		t.Fatal(err)
	}
	items := &faultyItems{ItemStore: db.Items(), search: errBoom}
	svc := w.service(&faultyStore{Store: db, items: items})
	if err := svc.DeleteManifest(ctx, w.owner("app"), digest); !errors.Is(err, errBoom) {
		t.Errorf("search: %v", err)
	}
	items.search, items.del = nil, errBoom
	for name, del := range map[string]func() error{
		"tag":      func() error { return svc.DeleteTag(ctx, w.owner("app"), "v1") },
		"manifest": func() error { return svc.DeleteManifest(ctx, w.owner("app"), digest) },
	} {
		if err := del(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	gets := &failingItemGets{ItemStore: db.Items()}
	svc = w.service(&faultyStore{Store: db, items: &faultyItems{ItemStore: gets}})
	if err := svc.DeleteTag(ctx, w.owner("app"), "v1"); !errors.Is(err, errBoom) {
		t.Errorf("get: %v", err)
	}
	if diff := cmp.Diff(map[string]string{
		"manifest.json":               digest,
		"sha256__" + sum(imageConfig): sum(imageConfig),
		"sha256__" + sum(layerA):      sum(layerA),
	}, w.tree("app/v1")); diff != "" {
		t.Fatal(diff)
	}
}

// failingItemGets fails every Get.
type failingItemGets struct{ store.ItemStore }

func (failingItemGets) Get(context.Context, string, bool, string) (types.Item, error) {
	return types.Item{}, errBoom
}
