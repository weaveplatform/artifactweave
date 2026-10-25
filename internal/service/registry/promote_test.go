package registry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// prod adds docker-prod, a second local Docker repository to promote to.
func (w *world) prod(tune ...func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := &repo.Config{Key: "docker-prod", Class: repo.Local, PackageType: repo.Docker}
	for _, f := range tune {
		f(c)
	}
	if err := w.db.Repos().Create(context.Background(), *repo.Defaults(c)); err != nil {
		w.t.Fatal(err)
	}
	return w.repo("docker-prod")
}

// treeIn lists the files below dir of c by name with their SHA-256s.
func (w *world) treeIn(c repo.Config, dir string) map[string]string {
	w.t.Helper()
	items, err := w.items().Tree(context.Background(), item.Target{Repo: c, Path: dir})
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

// propsIn are the properties of the item at path in c.
func (w *world) propsIn(c repo.Config, path string) types.Properties {
	w.t.Helper()
	it, err := w.item(c, path)
	if err != nil {
		w.t.Fatal(err)
	}
	p, err := w.db.Items().Properties(context.Background(), it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// versions are the tags the package index holds for image name in c.
func (w *world) versions(c repo.Config, name string) []string {
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

func (w *world) existsIn(c repo.Config, path string) bool {
	_, err := w.item(c, path)
	return err == nil
}

// A promotion copies a tag folder item for item, names the target image and
// tag in its manifest's properties, and indexes it; a move also leaves the
// source, keeping the items' history; every tag goes when none is named.
func TestAccPromote(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		prod := w.prod()
		w.upload(imageConfig, layerA, layerB)
		one, d1 := manifestOf(layerA)
		two, d2 := manifestOf(layerA, layerB)
		for _, p := range []struct{ body, digest, tag string }{{one, d1, "v1"}, {two, d2, "v2"}} {
			if err := w.push(p.body, p.digest, p.tag); err != nil {
				t.Fatal(err)
			}
		}
		src := w.owner("app")
		to := func(name string) registry.Owner {
			return registry.Owner{Repo: prod, Name: name, By: "bob"}
		}
		promote := func(name string, copyIt bool, renames ...registry.Rename) {
			t.Helper()
			if err := w.svc.Promote(ctx, registry.Promotion{
				From: src, To: to(name), Tags: renames, Copy: copyIt,
			}); err != nil {
				t.Fatal(err)
			}
		}

		// Copy, keeping the name and tag.
		promote("app", true, registry.Rename{From: "v1", To: "v1"})
		if diff := cmp.Diff(w.treeIn(w.docker, "app/v1"), w.treeIn(prod, "app/v1")); diff != "" {
			t.Fatal(diff)
		}
		if !w.existsIn(w.docker, "app/v1/manifest.json") {
			t.Fatal("a copy left the source")
		}
		if got := w.versions(prod, "app"); !cmp.Equal(got, []string{"v1"}) {
			t.Fatalf("prod's index: %v", got)
		}

		// Copy under a new name and tag: the manifest says so, and keeps
		// the platform its push set.
		promote("team/web", true, registry.Rename{From: "v1", To: "release"})
		props := w.propsIn(prod, "team/web/release/manifest.json")
		for k, want := range map[string]string{
			layout.RepoNameProperty:       "team/web",
			layout.ManifestProperty:       "release",
			layout.ManifestDigestProperty: "sha256:" + d1,
			layout.OSProperty:             "linux",
			layout.ArchitectureProperty:   "amd64",
		} {
			if !props.Has(k, want) {
				t.Errorf("%s = %q, want %q", k, props[k], want)
			}
		}
		if got := w.versions(prod, "team/web"); !cmp.Equal(got, []string{"release"}) {
			t.Fatalf("renamed index: %v", got)
		}

		// A move keeps the manifest's history and leaves the source.
		before, err := w.item(w.docker, "app/v2/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		promote("app", false, registry.Rename{From: "v2", To: "v2"})
		after, err := w.item(prod, "app/v2/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if !after.Created.Equal(before.Created) || after.CreatedBy != before.CreatedBy {
			t.Fatalf("history: %v by %s, was %v by %s",
				after.Created, after.CreatedBy, before.Created, before.CreatedBy)
		}
		moved := w.propsIn(prod, "app/v2/manifest.json")
		if !moved.Has(layout.OSProperty, "linux") ||
			!moved.Has(layout.ArchitectureProperty, "amd64") {
			t.Fatalf("moved manifest's platform: %v", moved)
		}
		if w.existsIn(w.docker, "app/v2") {
			t.Fatal("a move kept the source tag")
		}
		if got := w.versions(w.docker, "app"); !cmp.Equal(got, []string{"v1"}) {
			t.Fatalf("source index after the move: %v", got)
		}

		// No tag named: every tag of the image.
		tags, err := w.svc.PromotedTags(ctx, src, "")
		if err != nil || !cmp.Equal(tags, []string{"v1"}) {
			t.Fatalf("%v %v", tags, err)
		}
		// A repeated promotion of what the target holds changes nothing,
		// and a move of it only leaves the source.
		promote("app", false, registry.Rename{From: "v1", To: "v1"})
		if w.existsIn(w.docker, "app/v1") || !w.existsIn(prod, "app/v1/manifest.json") {
			t.Fatal("a move onto the same manifest")
		}
	})
}

// A manifest list takes the digest folders of its platform manifests along;
// a move removes those nothing left in the source image names, and keeps
// them when a renamed tag in the same image lists them still.
func TestAccPromoteMultiArch(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		prod := w.prod()
		ps := w.platforms()
		folder := func(body string) string { return layout.DigestFolder(sum(body)) }
		v1, d1 := indexOf(layout.MediaTypeOCIIndex, ps[0], ps[1])
		v2, d2 := indexOf(layout.MediaTypeDockerList, ps[1])
		if err := w.pushAs(layout.MediaTypeOCIIndex, v1, d1, "v1"); err != nil {
			t.Fatal(err)
		}
		if err := w.pushAs(layout.MediaTypeDockerList, v2, d2, "v2"); err != nil {
			t.Fatal(err)
		}
		src := w.owner("app")
		dst := registry.Owner{Repo: prod, Name: "app", By: "bob"}
		if err := w.svc.Promote(ctx, registry.Promotion{
			From: src, To: dst, Tags: []registry.Rename{{From: "v1", To: "v1"}},
		}); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]bool{
			"app/v1/list.manifest.json": true,
			"app/" + folder(ps[0]):      true,
			"app/" + folder(ps[1]):      true,
			"app/" + folder(ps[2]):      false,
		} {
			if got := w.existsIn(prod, path); got != want {
				t.Errorf("prod %s: %v, want %v", path, got, want)
			}
		}
		for path, want := range map[string]bool{
			"app/v1":               false,
			"app/" + folder(ps[0]): false, // only v1 listed it
			"app/" + folder(ps[1]): true,  // v2 lists it still
			"app/" + folder(ps[2]): true,  // never listed
		} {
			if got := w.existsIn(w.docker, path); got != want {
				t.Errorf("source %s: %v, want %v", path, got, want)
			}
		}
		// A move to another tag of the same image keeps its platforms.
		if err := w.svc.Promote(ctx, registry.Promotion{
			From: src, To: src, Tags: []registry.Rename{{From: "v2", To: "v3"}},
		}); err != nil {
			t.Fatal(err)
		}
		if w.existsIn(w.docker, "app/v2") || !w.existsIn(w.docker, "app/v3/list.manifest.json") ||
			!w.existsIn(w.docker, "app/"+folder(ps[1])) {
			t.Fatal("a rename in the same image")
		}
	})
}

// A target image that already holds some or all of a list's platforms
// still gets every platform it lacks, and a move leaves none of the
// source's behind: neither a platform the target held nor one copied after
// it. A repeated promotion is the target holding them all, which 7.x
// accepts (RTDEV-42905, 7.84.15).
func TestAccPromoteMultiArchHeldPlatforms(t *testing.T) {
	for _, c := range []struct {
		name   string
		held   []int // platforms the target holds by digest beforehand
		again  bool  // the list was promoted by copy beforehand
		copyIt bool
	}{
		{name: "partial target by copy", held: []int{0}, copyIt: true},
		{name: "partial target by move", held: []int{0}},
		{name: "middle platform held by move", held: []int{1}},
		{name: "every platform held by copy", held: []int{0, 1, 2}, copyIt: true},
		{name: "every platform held by move", held: []int{0, 1, 2}},
		{name: "repeated by copy", again: true, copyIt: true},
		{name: "repeated by move", again: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, w *world, _ func() store.Store) {
				ctx := context.Background()
				prod := w.prod()
				ps := w.platforms()
				folder := func(body string) string { return "app/" + layout.DigestFolder(sum(body)) }
				for _, i := range c.held {
					if _, err := w.items().Transfer(ctx, item.Transfer{
						From: item.Target{Repo: w.docker, Path: folder(ps[i])},
						To:   item.Target{Repo: prod, Path: folder(ps[i])},
					}); err != nil {
						t.Fatal(err)
					}
				}
				list, digest := indexOf(layout.MediaTypeOCIIndex, ps...)
				if err := w.pushAs(layout.MediaTypeOCIIndex, list, digest, "v1"); err != nil {
					t.Fatal(err)
				}
				promote := func(copyIt bool) {
					t.Helper()
					if err := w.svc.Promote(ctx, registry.Promotion{
						From: w.owner(
							"app",
						),
						To:   registry.Owner{Repo: prod, Name: "app", By: "bob"},
						Tags: []registry.Rename{{From: "v1", To: "v1"}},
						Copy: copyIt,
					}); err != nil {
						t.Fatal(err)
					}
				}
				if c.again {
					promote(true)
				}
				promote(c.copyIt)
				if !w.existsIn(prod, "app/v1/list.manifest.json") {
					t.Fatal("the list did not land")
				}
				for i, p := range ps {
					if !w.existsIn(prod, folder(p)+"/manifest.json") {
						t.Errorf("platform %d is not in the target", i)
					}
					if got := w.existsIn(w.docker, folder(p)); got != c.copyIt {
						t.Errorf("platform %d in the source: %v, want %v", i, got, c.copyIt)
					}
				}
				if got := w.existsIn(w.docker, "app/v1"); got != c.copyIt {
					t.Errorf("the source tag: %v, want %v", got, c.copyIt)
				}
			})
		})
	}
}

// A target tag holding another manifest is replaced as a push replaces it:
// nothing of the old image stays in the folder, and under Tag Retention
// the old manifest keeps its digest folder.
func TestAccPromoteOverwrite(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		two := 2
		prod := w.prod(func(c *repo.Config) { c.DockerTagRetention = &two })
		w.upload(imageConfig, layerA, layerB)
		a, da := manifestOf(layerA, layerB)
		b, db := manifestOf(layerB)
		if err := w.push(a, da, "a"); err != nil {
			t.Fatal(err)
		}
		if err := w.push(b, db, "b"); err != nil {
			t.Fatal(err)
		}
		dst := registry.Owner{Repo: prod, Name: "app", By: "bob"}
		for _, tag := range []string{"a", "b"} {
			if err := w.svc.Promote(ctx, registry.Promotion{
				From: w.owner("app"), To: dst, Copy: true,
				Tags: []registry.Rename{{From: tag, To: "latest"}},
			}); err != nil {
				t.Fatal(err)
			}
		}
		if diff := cmp.Diff(w.treeIn(w.docker, "app/b"), w.treeIn(prod, "app/latest")); diff != "" {
			t.Fatalf("the replaced tag: %s", diff)
		}
		if !w.existsIn(prod, "app/"+layout.DigestFolder(da)+"/manifest.json") {
			t.Fatal("Tag Retention did not keep the previous manifest")
		}
		replaces, err := w.svc.Replaces(ctx, dst, "latest", da)
		if err != nil || !replaces {
			t.Fatalf("%v %v", replaces, err)
		}
	})
}

func TestAccPromoteErrors(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		w.upload(imageConfig, layerA)
		body, digest := manifestOf(layerA)
		if err := w.push(body, digest, "v1", layout.DigestFolder(digest)); err != nil {
			t.Fatal(err)
		}
		o := w.owner("app")
		for name, c := range map[string]struct {
			owner registry.Owner
			tag   string
		}{
			"absent tag":      {o, "v9"},
			"reserved tag":    {o, layout.UploadsFolder},
			"digest folder":   {o, layout.DigestFolder(digest)},
			"no image":        {w.owner("nothing"), ""},
			"no tag in image": {w.owner("nothing"), "v1"},
		} {
			if _, err := w.svc.PromotedTags(
				ctx,
				c.owner,
				c.tag,
			); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if err := w.svc.Promote(ctx, registry.Promotion{
			From: o, To: o, Tags: []registry.Rename{{From: "v1", To: "v1"}},
		}); !errors.Is(err, registry.ErrSamePlace) {
			t.Fatalf("onto itself: %v", err)
		}
		if err := w.svc.Promote(ctx, registry.Promotion{
			From: o, To: w.owner("other"), Tags: []registry.Rename{{From: "v9", To: "v9"}},
		}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("absent tag: %v", err)
		}
	})
}

// The parents of a manifest are the image's lists that name it, each once
// with the tags it sits under; a list stored by digest adds no tag.
func TestAccParentManifests(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		ps := w.platforms()
		v1, d1 := indexOf(layout.MediaTypeOCIIndex, ps[0], ps[1])
		v2, d2 := indexOf(layout.MediaTypeDockerList, ps[1])
		if err := w.pushAs(
			layout.MediaTypeOCIIndex,
			v1,
			d1,
			layout.DigestFolder(d1),
			"v1",
			"v1b",
		); err != nil {
			t.Fatal(err)
		}
		if err := w.pushAs(layout.MediaTypeDockerList, v2, d2, "v2"); err != nil {
			t.Fatal(err)
		}
		for tag, body := range map[string]string{"amd": ps[0], "arm": ps[1], "solo": ps[2]} {
			if err := w.push(body, sum(body), tag); err != nil {
				t.Fatal(err)
			}
		}
		one := registry.Parent{Digest: "sha256:" + d1, Tags: []string{"v1", "v1b"}}
		two := registry.Parent{Digest: "sha256:" + d2, Tags: []string{"v2"}}
		both := []registry.Parent{one, two}
		if d2 < d1 {
			both = []registry.Parent{two, one}
		}
		o := w.owner("app")
		for tag, want := range map[string][]registry.Parent{
			"solo": {},
			"amd":  {one},
			"arm":  both,
		} {
			got, err := w.svc.ParentManifests(ctx, o, tag)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("%s: %s", tag, diff)
			}
		}
		for _, tag := range []string{"absent", layout.UploadsFolder} {
			if _, err := w.svc.ParentManifests(ctx, o, tag); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: %v", tag, err)
			}
		}
	})
}

func TestAccPromoteStoreFailures(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, f *faulty) (prod repo.Config, list string) {
		t.Helper()
		prod = f.prod()
		ps := f.platforms()
		body, digest := indexOf(layout.MediaTypeOCIIndex, ps[0])
		if err := f.pushAs(layout.MediaTypeOCIIndex, body, digest, "v1"); err != nil {
			t.Fatal(err)
		}
		return prod, digest
	}
	promote := func(f *faulty, prod repo.Config, copyIt bool) error {
		return f.svc.Promote(ctx, registry.Promotion{
			From: f.owner("app"), To: registry.Owner{Repo: prod, Name: "app", By: "bob"},
			Tags: []registry.Rename{{From: "v1", To: "v1"}}, Copy: copyIt,
		})
	}
	cases := map[string]func(t *testing.T, f *faulty) error{
		"list the tags": func(_ *testing.T, f *faulty) error {
			f.st.items.search = errBoom
			_, err := f.svc.PromotedTags(ctx, f.owner("app"), "")
			return err
		},
		"find the tag": func(_ *testing.T, f *faulty) error {
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/v1/manifest.json"}
			_, err := f.svc.PromotedTags(ctx, f.owner("app"), "v1")
			return err
		},
		"the list's properties": func(t *testing.T, f *faulty) error {
			prod, _ := setup(t, f)
			f.st.items.ItemStore = propertiesFailing{f.db.Items()}
			return promote(f, prod, true)
		},
		"read the list": func(t *testing.T, f *faulty) error {
			prod, list := setup(t, f)
			f.svc.Blobs = openFailingFor{f.blobs, list}
			return promote(f, prod, true)
		},
		"look for a platform": func(t *testing.T, f *faulty) error {
			prod, _ := setup(t, f)
			ps, err := f.svc.ParentManifests(ctx, f.owner("app"), "v1")
			if err != nil || len(ps) != 0 {
				t.Fatalf("%v %v", ps, err)
			}
			body, _ := manifestOf(layerA)
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/" + layout.DigestFolder(sum(body))}
			return promote(f, prod, true)
		},
		"move the tag": func(t *testing.T, f *faulty) error {
			prod, _ := setup(t, f)
			f.st.items.del = errBoom
			return promote(f, prod, false)
		},
		"list the image's manifests": func(t *testing.T, f *faulty) error {
			setup(t, f)
			f.st.items.search = errBoom
			_, err := f.svc.ParentManifests(ctx, f.owner("app"), "v1")
			return err
		},
		"read a parent": func(t *testing.T, f *faulty) error {
			_, list := setup(t, f)
			body, _ := manifestOf(layerA)
			if err := f.push(body, sum(body), "amd"); err != nil {
				return err
			}
			f.svc.Blobs = openFailingFor{f.blobs, list}
			_, err := f.svc.ParentManifests(ctx, f.owner("app"), "amd")
			return err
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFaulty(t)
			f.upload(imageConfig, layerA, layerB)
			body, digest := manifestOf(layerA)
			if err := f.push(body, digest, "v0"); err != nil {
				t.Fatal(err)
			}
			if err := c(t, f); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
