package registry_test

import (
	"context"
	"errors"
	"slices"
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

func ptr(n int) *int { return &n }

// folders lists the folders directly below the image name in c, without
// _uploads, sorted.
func (w *world) folders(c repo.Config, name string) []string {
	w.t.Helper()
	children, _, err := w.db.Items().Children(context.Background(), c.Key, false, name,
		store.Page{Limit: 1000})
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, it := range children {
		if it.Folder && it.Name != layout.UploadsFolder {
			out = append(out, it.Name)
		}
	}
	slices.Sort(out)
	return out
}

// trashed reports whether anything at path of c is in the trash can.
func (w *world) trashed(c repo.Config, path string) bool {
	w.t.Helper()
	entries, _, err := w.db.Trash().List(context.Background(), c.Key, path, store.Page{Limit: 10})
	if err != nil {
		w.t.Fatal(err)
	}
	return len(entries) > 0
}

// Max Unique Tags removes the pushed image's oldest tags beyond the limit
// after each push, first in first out, latest included, to the trash can;
// an overwrite moves its tag to the newest place, and another image is
// left as it is (research 23, 5.3).
func TestAccMaxUniqueTagsRemovesTheOldestTags(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		limited := w.repoOf(&repo.Config{
			Key: "docker-limited", Class: repo.Local,
			PackageType: repo.Docker, MaxUniqueTags: ptr(2),
		})
		unlimited := limited
		unlimited.MaxUniqueTags = nil
		first, firstDigest := w.image(limited, "app", layerA)
		second, secondDigest := w.image(limited, "app", layerB)
		other, otherDigest := w.image(limited, "other", layerA)

		// Another image already over the limit, as after the limit was
		// lowered.
		for i, tag := range []string{"a", "b", "c"} {
			w.at(c, i)
			w.pushTo(unlimited, "other", other, otherDigest, tag)
		}

		steps := []struct {
			body, digest, tag string
			expected          []string
		}{
			{first, firstDigest, "v1", []string{"v1"}},
			{first, firstDigest, "v2", []string{"v1", "v2"}},
			{first, firstDigest, "latest", []string{"v2", "latest"}},
			{first, firstDigest, "v1", []string{"latest", "v1"}},
			{second, secondDigest, "latest", []string{"v1", "latest"}},
			{first, firstDigest, "v3", []string{"latest", "v3"}},
			{first, firstDigest, "v4", []string{"v3", "v4"}},
		}
		for i, s := range steps {
			w.at(c, 10+i)
			w.pushTo(limited, "app", s.body, s.digest, s.tag)
			if diff := cmp.Diff(s.expected, w.tags(limited, "app")); diff != "" {
				t.Fatalf("after pushing %s (step %d):\n%s", s.tag, i, diff)
			}
			if got := w.folders(limited, "app"); !slices.Equal(sorted(s.expected), got) {
				t.Fatalf("folders after %s: %v", s.tag, got)
			}
		}
		for _, gone := range []string{"app/v1", "app/v2", "app/latest"} {
			if !w.trashed(limited, gone) {
				t.Errorf("%s is not in the trash can", gone)
			}
		}
		if diff := cmp.Diff([]string{"a", "b", "c"}, w.tags(limited, "other")); diff != "" {
			t.Fatalf("another image was trimmed:\n%s", diff)
		}
	})
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// More manifests than the retention tests' two layers.
const (
	layerC = "layer c"
	layerD = "layer d"
)

// Tag Retention keeps a tag's previous manifests in their digest folders,
// still naming the tag, at most the setting minus one, the oldest going
// first; they are no tags, and Max Unique Tags removes them with their tag.
func TestAccTagRetentionKeepsPreviousManifests(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		kept := w.repoOf(&repo.Config{
			Key: "docker-kept", Class: repo.Local,
			PackageType: repo.Docker, DockerTagRetention: ptr(3),
		})
		var digests []string
		for i, layer := range []string{layerA, layerB, layerC, layerD} {
			body, digest := w.image(kept, "app", layer)
			digests = append(digests, digest)
			w.at(c, i+1)
			w.pushTo(kept, "app", body, digest, "v1")
		}
		want := sorted([]string{
			"v1",
			layout.DigestFolder(digests[1]), layout.DigestFolder(digests[2]),
		})
		if diff := cmp.Diff(want, w.folders(kept, "app")); diff != "" {
			t.Fatalf("kept folders:\n%s", diff)
		}
		if !w.trashed(kept, "app/"+layout.DigestFolder(digests[0])) {
			t.Error("the oldest kept manifest is not in the trash can")
		}
		if diff := cmp.Diff([]string{"v1"}, w.tags(kept, "app")); diff != "" {
			t.Fatalf("kept manifests are tags:\n%s", diff)
		}
		dir := "app/" + layout.DigestFolder(digests[1])
		if got := w.propertiesOf(kept, dir+"/manifest.json"); !slices.Equal(
			got[layout.ManifestProperty], []string{"v1"}) {
			t.Fatalf("kept manifest properties %v", got)
		}
		wantTree := map[string]string{
			"manifest.json":               digests[1],
			"sha256__" + sum(imageConfig): sum(imageConfig),
			"sha256__" + sum(layerB):      sum(layerB),
		}
		if diff := cmp.Diff(wantTree, w.treeOf(kept, dir)); diff != "" {
			t.Fatalf("kept folder:\n%s", diff)
		}
		ks, err := layout.Retained(context.Background(), w.db.Items(), kept.Key, "app", "v1")
		if err != nil || len(ks) != 2 || !layout.PushTime(ks[0]).Before(layout.PushTime(ks[1])) {
			t.Fatalf("retained %+v %v", ks, err)
		}

		// Max Unique Tags removes v1 with what it kept.
		limited := kept
		limited.MaxUniqueTags = ptr(1)
		body, digest := w.image(kept, "app", layerA)
		w.at(c, 9)
		w.pushTo(limited, "app", body, digest, "v2")
		if diff := cmp.Diff([]string{"v2"}, w.folders(kept, "app")); diff != "" {
			t.Fatalf("after the limit:\n%s", diff)
		}
	})
}

// A previous manifest that also sits in its digest folder, pushed there by
// digest in the same push, is the tag's twin, which becomes its retained
// version: the tag folder is replaced and the digest folder names the tag
// (decision 0039), in Docker repositories as in Helm OCI ones.
func TestAccTagRetentionAdoptsAManifestPushedByDigest(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		kept := w.repoOf(&repo.Config{
			Key: "docker-kept", Class: repo.Local,
			PackageType: repo.Docker, DockerTagRetention: ptr(2),
		})
		first, firstDigest := w.image(kept, "app", layerA)
		second, secondDigest := w.image(kept, "app", layerB)
		w.at(c, 1)
		w.pushTo(kept, "app", first, firstDigest, "v1", layout.DigestFolder(firstDigest))
		w.at(c, 2)
		w.pushTo(kept, "app", second, secondDigest, "v1")
		dir := "app/" + layout.DigestFolder(firstDigest)
		if got := w.propertiesOf(kept, dir+"/manifest.json")[layout.ManifestProperty]; !slices.Equal(
			got,
			[]string{"v1"},
		) {
			t.Fatalf("the digest folder's manifest names %v", got)
		}
		ks, err := layout.Retained(context.Background(), w.db.Items(), kept.Key, "app", "v1")
		if err != nil || len(ks) != 1 || ks[0].Path != dir {
			t.Fatalf("retained %+v %v", ks, err)
		}
	})
}

// listOf is an image index of the manifests whose SHA-256s are hexes.
func listOf(hexes ...string) string {
	var ms []string
	for _, h := range hexes {
		ms = append(ms, `{"mediaType":"`+layout.MediaTypeOCIManifest+`","digest":"sha256:`+h+
			`","size":1}`)
	}
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIIndex + `","manifests":[` +
		strings.Join(ms, ",") + `]}`
}

// RemoveTag removes a tag folder to the trash can and drops its version;
// with Platforms a list takes the platform manifests no other list names
// and no other tag holds, and with Retained the tag's kept manifests go
// too.
func TestAccRemoveTag(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		c := &clock{}
		w.svc.Now = c.now
		o := w.owner("app")
		p1, p2, p3, p4 := `{"p":1}`, `{"p":2}`, `{"p":3}`, `{"p":4}`
		for _, p := range []string{p1, p2, p3, p4} {
			w.deploy(w.docker, "app/"+layout.DigestFolder(sum(p))+"/manifest.json", p, start)
		}
		multi := listOf(sum(p1), sum(p2), sum(p1), sum(p4))
		w.deploy(w.docker, "app/multi/list.manifest.json", multi, start)
		w.deploy(w.docker, "app/other/list.manifest.json", listOf(sum(p2)), start)
		// Another tag holds p4's manifest itself.
		w.deploy(w.docker, "app/direct/manifest.json", p4, start)
		w.deploy(w.docker, "app/notes/readme.txt", "hello", start)
		old := listOf(sum(p3))
		items := &item.Service{Store: w.db, Blobs: w.blobs, Now: c.now}
		kept := "app/" + layout.DigestFolder(sum(old)) + "/list.manifest.json"
		if _, err := items.Deploy(ctx, item.Deploy{
			Target:     item.Target{Repo: w.docker, Path: kept},
			Body:       strings.NewReader(old),
			Properties: types.Properties{layout.ManifestProperty: {"multi"}},
		}); err != nil {
			t.Fatal(err)
		}
		tags := sorted(w.tags(w.docker, "app"))
		if diff := cmp.Diff([]string{"direct", "multi", "other"}, tags); diff != "" {
			t.Fatal(diff)
		}

		for _, tag := range []string{"nope", "notes", layout.DigestFolder(sum(p2))} {
			err := w.svc.RemoveTag(ctx, o, tag, registry.Removal{})
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: %v", tag, err)
			}
		}
		both := registry.Removal{Platforms: true, Retained: true}
		if err := w.svc.RemoveTag(ctx, o, "multi", both); err != nil {
			t.Fatal(err)
		}
		digests := func(ps ...string) []string {
			var out []string
			for _, p := range ps {
				out = append(out, layout.DigestFolder(sum(p)))
			}
			return out
		}
		want := sorted(append([]string{"direct", "notes", "other"}, digests(p2, p4)...))
		if diff := cmp.Diff(want, w.folders(w.docker, "app")); diff != "" {
			t.Fatalf("after removing multi:\n%s", diff)
		}
		for _, gone := range append([]string{"multi"}, digests(p1, p3)...) {
			if !w.trashed(w.docker, "app/"+gone) {
				t.Errorf("%s is not in the trash can", gone)
			}
		}
		// Without Platforms the list's platform manifest stays.
		if err := w.svc.RemoveTag(ctx, o, "other", registry.Removal{}); err != nil {
			t.Fatal(err)
		}
		want = sorted(append([]string{"direct", "notes"}, digests(p2, p4)...))
		if diff := cmp.Diff(want, w.folders(w.docker, "app")); diff != "" {
			t.Fatalf("after removing other:\n%s", diff)
		}
		if tags := w.tags(w.docker, "app"); !slices.Equal(tags, []string{"direct"}) {
			t.Fatalf("tags %v", tags)
		}
	})
}

// treeOf lists the files below dir of c by name with their SHA-256s.
func (w *world) treeOf(c repo.Config, dir string) map[string]string {
	w.t.Helper()
	items, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Tree(context.Background(),
		item.Target{Repo: c, Path: dir})
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

func (w *world) propertiesOf(c repo.Config, path string) types.Properties {
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
