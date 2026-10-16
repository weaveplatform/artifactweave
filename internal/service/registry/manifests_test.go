package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// An image's config, with labels, and two layers, as a client uploads
// them before its manifest.
const (
	imageConfig = `{"architecture":"amd64","os":"linux",` +
		`"config":{"Labels":{"org.example.team":"platform","version":"1"}}}`
	layerA = "layer a"
	layerB = "layer b"
)

// manifestOf is an OCI image manifest of imageConfig and the given layers,
// with its SHA-256.
func manifestOf(layers ...string) (string, string) {
	var ls []string
	for _, l := range layers {
		ls = append(ls, `{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip",`+
			`"digest":"sha256:`+sum(l)+`","size":`+strconv.Itoa(len(l))+`}`)
	}
	body := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json",` +
		`"digest":"sha256:` + sum(imageConfig) + `","size":` + strconv.Itoa(len(imageConfig)) + `},` +
		`"layers":[` + strings.Join(ls, ",") + `]}`
	h := sha256.Sum256([]byte(body))
	return body, hex.EncodeToString(h[:])
}

// upload stores contents as finished uploads of the image app.
func (w *world) upload(contents ...string) {
	w.t.Helper()
	for _, c := range contents {
		w.deploy(w.docker, layout.UploadPath("app", sum(c)), c, start)
	}
}

// push stores body as the manifest of the image app in folders.
func (w *world) push(body, digest string, folders ...string) error {
	m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(body), digest)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.svc.PutManifest(context.Background(), registry.Push{
		Owner: w.owner("app"), Manifest: m, Body: []byte(body), Folders: folders,
	})
}

// tree lists the files below dir of docker-local by name with their
// SHA-256s.
func (w *world) tree(dir string) map[string]string {
	w.t.Helper()
	items, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Tree(context.Background(),
		item.Target{Repo: w.docker, Path: dir})
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]string{}
	for _, it := range items {
		if !it.Folder {
			out[strings.TrimPrefix(it.FullPath(), dir+"/")] = it.Actual.SHA256
		}
	}
	return out
}

func (w *world) properties(path string) types.Properties {
	w.t.Helper()
	it, err := w.item(w.docker, path)
	if err != nil {
		w.t.Fatal(err)
	}
	p, err := w.db.Items().Properties(context.Background(), it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// A push by tag stores the tag folder of decision 0031 item for item: the
// manifest's exact bytes with its properties, and the config and layers
// beside it, linked to the stored content.
func TestPushByTag(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(imageConfig, layerA, layerB)
		body, digest := manifestOf(layerA, layerB)
		if err := w.push(body, digest, "v1"); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"manifest.json":               digest,
			"sha256__" + sum(imageConfig): sum(imageConfig),
			"sha256__" + sum(layerA):      sum(layerA),
			"sha256__" + sum(layerB):      sum(layerB),
		}
		if diff := cmp.Diff(want, w.tree("app/v1")); diff != "" {
			t.Fatal(diff)
		}
		wantProps := types.Properties{
			"docker.manifest":               {"v1"},
			"docker.manifest.digest":        {"sha256:" + digest},
			"docker.manifest.type":          {layout.MediaTypeOCIManifest},
			"docker.repoName":               {"app"},
			"sha256":                        {digest},
			"docker.label.org.example.team": {"platform"},
			"docker.label.version":          {"1"},
		}
		if diff := cmp.Diff(wantProps, w.properties("app/v1/manifest.json")); diff != "" {
			t.Fatal(diff)
		}
		r, _, err := w.blobs.Open(context.Background(), digest)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		if got, _ := io.ReadAll(r); string(got) != body {
			t.Fatalf("stored %q", got)
		}
		it, err := w.item(w.docker, "app/v1/manifest.json")
		if err != nil || it.CreatedBy != "alice" {
			t.Fatalf("%+v %v", it, err)
		}
	})
}

// A push by digest goes to the digest folder, and each ?tag= to its own
// tag folder, in one transaction.
func TestPushByDigestWithTags(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(imageConfig, layerA)
		body, digest := manifestOf(layerA)
		if err := w.push(body, digest, layout.DigestFolder(digest), "a", "b"); err != nil {
			t.Fatal(err)
		}
		for folder, name := range map[string]string{
			"sha256__" + digest: "sha256__" + digest, "a": "a", "b": "b",
		} {
			tree := w.tree("app/" + folder)
			if len(tree) != 3 || tree["manifest.json"] != digest {
				t.Errorf("%s: %v", folder, tree)
			}
			if got := w.properties("app/" + folder + "/manifest.json")["docker.manifest"]; got[0] != name {
				t.Errorf("%s: docker.manifest %v", folder, got)
			}
		}
		it, err := layout.FindManifest(context.Background(), w.db.Items(), "docker-local", "app",
			digest)
		if err != nil || it.Name != "manifest.json" {
			t.Fatalf("%+v %v", it, err)
		}
	})
}

// Every blob must be in the image: each missing one is named, under any
// algorithm, and nothing is stored.
func TestUnknownBlobs(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(layerA)
		// In another image, which does not count.
		w.deploy(w.docker, layout.UploadPath("other", sum(layerB)), layerB, start)
		body, digest := manifestOf(layerA, layerB)
		body = strings.Replace(body, "]}",
			`,{"mediaType":"x","digest":"sha512:`+strings.Repeat("a", 128)+`","size":1}]}`, 1)
		digest = sum(body)
		err := w.push(body, digest, "v1")
		var unknown *registry.BlobsUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("err = %v", err)
		}
		want := []string{
			"sha256:" + sum(
				imageConfig,
			),
			"sha256:" + sum(layerB),
			"sha512:" + strings.Repeat("a", 128),
		}
		if diff := cmp.Diff(want, unknown.Digests); diff != "" {
			t.Fatal(diff)
		}
		if !strings.Contains(err.Error(), "unknown blobs") {
			t.Fatal(err)
		}
		if _, err := w.item(w.docker, "app/v1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stored: %v", err)
		}
	})
}

// Pushing over a tag replaces the folder's contents, properties included;
// pushing what a folder holds changes nothing.
func TestOverwrite(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		w.upload(imageConfig, layerA, layerB)
		first, d1 := manifestOf(layerA)
		second, d2 := manifestOf(layerB)
		o := w.owner("app")
		replaces := func(folder, d string, want bool) {
			t.Helper()
			got, err := w.svc.Replaces(ctx, o, folder, d)
			if err != nil || got != want {
				t.Fatalf("Replaces(%s, %s) = %v %v", folder, d[:8], got, err)
			}
		}
		replaces("v1", d1, false)
		if err := w.push(first, d1, "v1"); err != nil {
			t.Fatal(err)
		}
		// The jf build properties on a layer survive a repeated push.
		it, _ := w.item(w.docker, "app/v1/sha256__"+sum(layerA))
		if err := w.db.Items().PatchProperties(ctx, it.ID,
			types.Properties{"build.name": {"app"}}, nil); err != nil {
			t.Fatal(err)
		}
		replaces("v1", d1, false)
		replaces("v1", d2, true)
		if err := w.push(first, d1, "v1"); err != nil {
			t.Fatal(err)
		}
		if p := w.properties("app/v1/sha256__" + sum(layerA)); !p.Has("build.name", "app") {
			t.Fatalf("a repeated push replaced the folder: %v", p)
		}
		if err := w.push(second, d2, "v1"); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"manifest.json":               d2,
			"sha256__" + sum(imageConfig): sum(imageConfig),
			"sha256__" + sum(layerB):      sum(layerB),
		}
		if diff := cmp.Diff(want, w.tree("app/v1")); diff != "" {
			t.Fatal(diff)
		}
		if got := w.properties("app/v1/manifest.json")["sha256"]; got[0] != d2 {
			t.Fatal(got)
		}
		// A folder of other files is replaced too, and a file where the
		// folder goes is the deploy's conflict.
		w.deploy(w.docker, "app/v2/notes.txt", "notes", start)
		replaces("v2", d1, true)
		if err := w.push(first, d1, "v2"); err != nil {
			t.Fatal(err)
		}
		if _, ok := w.tree("app/v2")["notes.txt"]; ok {
			t.Fatal("the folder kept another file")
		}
		w.deploy(w.docker, "app/v3", "a file", start)
		replaces("v3", d1, false)
		if err := w.push(first, d1, "v3"); !errors.Is(err, item.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Content garbage collection removed after the push found it is unknown,
// and nothing is stored; a config that cannot be read only loses its
// labels.
func TestBlobContentGone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, inmem.New())
	w.upload(imageConfig, layerA)
	body, digest := manifestOf(layerA)
	if err := w.blobs.Delete(ctx, sum(layerA)); err != nil {
		t.Fatal(err)
	}
	var unknown *registry.BlobsUnknownError
	if err := w.push(body, digest, "v1"); !errors.As(err, &unknown) ||
		unknown.Digests[0] != "sha256:"+sum(layerA) {
		t.Fatalf("err = %v", err)
	}
	if _, err := w.item(w.docker, "app/v1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stored: %v", err)
	}
	w2 := newWorld(t, inmem.New())
	w2.upload(imageConfig, layerA)
	if err := w2.blobs.Delete(ctx, sum(imageConfig)); err != nil {
		t.Fatal(err)
	}
	if err := w2.push(body, digest, "v1"); !errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
}

// openFailing is a blob store whose content cannot be opened.
type openFailing struct{ blob.Store }

func (openFailing) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errBoom
}

func TestUnreadableConfigLosesItsLabels(t *testing.T) {
	w := newWorld(t, inmem.New())
	w.upload(imageConfig, layerA)
	w.svc.Blobs = openFailing{w.blobs}
	body, digest := manifestOf(layerA)
	if err := w.push(body, digest, "v1"); err != nil {
		t.Fatal(err)
	}
	if p := w.properties("app/v1/manifest.json"); p.Has("docker.label.version", "1") ||
		!p.Has("sha256", digest) {
		t.Fatal(p)
	}
}

// getFailing fails Get for one path.
type getFailing struct {
	store.ItemStore
	path string
}

func (g getFailing) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	p string,
) (types.Item, error) {
	if p == g.path {
		return types.Item{}, errBoom
	}
	return g.ItemStore.Get(ctx, repoKey, cached, p) //nolint:wrapcheck // test double
}

func TestManifestStoreFailures(t *testing.T) {
	ctx := context.Background()
	body, digest := manifestOf(layerA)
	cases := map[string]func(t *testing.T, f *faulty) error{
		"find a blob": func(_ *testing.T, f *faulty) error {
			f.st.items.search = errBoom
			return f.push(body, digest, "v1")
		},
		"store the bytes": func(_ *testing.T, f *faulty) error {
			f.bl.begin = errBoom
			return f.push(body, digest, "v1")
		},
		"commit the bytes": func(_ *testing.T, f *faulty) error {
			f.bl.commit = errBoom
			return f.push(body, digest, "v1")
		},
		"replace a folder": func(_ *testing.T, f *faulty) error {
			if err := f.push(body, digest, "v1"); err != nil {
				return err
			}
			f.st.items.del = errBoom
			second, d2 := manifestOf(layerB)
			return f.push(second, d2, "v1")
		},
		"look at a folder": func(_ *testing.T, f *faulty) error {
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/v1"}
			_, err := f.svc.Replaces(ctx, f.owner("app"), "v1", digest)
			if !errors.Is(err, errBoom) {
				return err
			}
			return f.push(body, digest, "v1")
		},
		"look at a folder's manifest": func(_ *testing.T, f *faulty) error {
			if err := f.push(body, digest, "v1"); err != nil {
				return err
			}
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/v1/manifest.json"}
			_, err := f.svc.Replaces(ctx, f.owner("app"), "v1", digest)
			return err
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFaulty(t)
			f.upload(imageConfig, layerA, layerB)
			if err := c(t, f); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
