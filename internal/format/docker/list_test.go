package docker_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitCompare(t *testing.T) {
	names := []string{"v1", "beta", "Alpha", "alpha", "1.0", "ALPHA", "b", "B"}
	slices.SortFunc(names, layout.Compare)
	want := []string{"1.0", "ALPHA", "Alpha", "alpha", "B", "b", "beta", "v1"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Fatal(diff)
	}
}

// The listings are AQL queries, so they run on the reference evaluator and
// on SQL, where a name's _ and . must not act as wildcards.
func TestUnitListings(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"inmem": func(*testing.T) store.Store { return inmem.New() },
		"sqlite": func(t *testing.T) store.Store {
			st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
				sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	} {
		t.Run(name, func(t *testing.T) { listings(t, open(t)) })
	}
}

func listings(t *testing.T, db store.Store) {
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	configs := map[string]repo.Config{}
	for _, key := range []string{"docker-local", "other", "empty"} {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Docker})
		if err := db.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		configs[key] = c
	}
	deploy := func(key, path, body string) {
		t.Helper()
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: configs[key], Path: path}, Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	m, other := sum("m"), sum("other")
	// sha256__<hex> is an ordinary tag in a local repository, as on 7.x.
	for _, p := range []string{"v1", "Alpha", "alpha", layout.DigestFolder(m), "sha256__" + m} {
		deploy("docker-local", "a_b.c/"+p+"/manifest.json", "m")
	}
	deploy("docker-local", "a_b.c/list/list.manifest.json", "m")
	deploy("docker-local", "a_b.c/v2/manifest.json", "other")
	// Not tags: blobs, uploads, a folder with no manifest, a name the tag
	// grammar refuses and a digest folder with a short digest.
	deploy("docker-local", "a_b.c/v1/sha256__"+sum("layer"), "layer")
	deploy("docker-local", "a_b.c/_uploads/manifest.json", "m")
	deploy("docker-local", "a_b.c/blobs-only/sha256__"+sum("layer"), "layer")
	deploy("docker-local", "a_b.c/-bad/manifest.json", "m")
	deploy("docker-local", "a_b.c/sha256:abc/manifest.json", "m")
	// Another image below this one, its look-alike and another repository.
	deploy("docker-local", "a_b.c/sub/v9/manifest.json", "m")
	deploy("docker-local", "aXb.c/v8/manifest.json", "m")
	deploy("other", "a_b.c/v7/manifest.json", "m")
	// Images only by digest; and folders that are no image.
	deploy("other", "untagged/"+layout.DigestFolder(other)+"/manifest.json", "other")
	deploy("other", "plain/manifest.json", "m")
	deploy("other", "top.json", "m")

	tags, found, err := layout.Tags(ctx, db.Items(), "docker-local", "a_b.c")
	if err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	want := []string{"Alpha", "alpha", "list", "sha256__" + m, "v1", "v2"}
	if diff := cmp.Diff(want, tags); diff != "" {
		t.Error(diff)
	}
	tags, found, err = layout.Tags(ctx, db.Items(), "other", "untagged")
	if err != nil || !found || len(tags) != 0 {
		t.Errorf("untagged: %v %v %v", tags, found, err)
	}
	for _, name := range []string{"absent", "plain"} {
		if tags, found, err := layout.Tags(ctx, db.Items(), "other", name); err != nil || found ||
			len(tags) != 0 {
			t.Errorf("%s: %v %v %v", name, tags, found, err)
		}
	}

	folders, err := layout.ManifestFolders(ctx, db.Items(), "docker-local", "a_b.c", m)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"Alpha", "alpha", "list", layout.DigestFolder(m), "sha256__" + m, "v1"}
	if diff := cmp.Diff(want, folders); diff != "" {
		t.Error(diff)
	}
	if folders, err := layout.ManifestFolders(ctx, db.Items(), "docker-local", "a_b.c",
		sum("absent")); err != nil || len(folders) != 0 {
		t.Errorf("absent: %v %v", folders, err)
	}

	images, err := layout.Images(ctx, db.Items(), []string{"docker-local", "other", "empty"})
	if err != nil {
		t.Fatal(err)
	}
	wantImages := []layout.Image{
		{Repo: "docker-local", Name: "a_b.c"},
		{Repo: "docker-local", Name: "a_b.c/sub"},
		{Repo: "docker-local", Name: "aXb.c"},
		{Repo: "other", Name: "a_b.c"},
		{Repo: "other", Name: "untagged"},
	}
	if diff := cmp.Diff(wantImages, images); diff != "" {
		t.Error(diff)
	}
	if images, err := layout.Images(ctx, db.Items(), []string{"empty"}); err != nil ||
		len(images) != 0 {
		t.Errorf("empty: %v %v", images, err)
	}
	if images, err := layout.Images(ctx, db.Items(), nil); err != nil || images != nil {
		t.Errorf("no repositories: %v %v", images, err)
	}
}

func TestUnitListingsFail(t *testing.T) {
	ctx := context.Background()
	if _, _, err := layout.Tags(ctx, failingSearch{}, "r", "app"); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("tags: %v", err)
	}
	if _, err := layout.ManifestFolders(ctx, failingSearch{}, "r", "app", sum("x")); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("folders: %v", err)
	}
	if _, err := layout.Images(ctx, failingSearch{}, []string{"r"}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("images: %v", err)
	}
}
