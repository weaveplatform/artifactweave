package registry_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The chart in testdata/helm as helm 4.3 pushed it: acme 1.2.3+build.4,
// signed, so its manifest holds a provenance layer and the chart.
const (
	chartTag      = "1.2.3_build.4"
	chartManifest = "75484ef38d17c0589e0483726c1b3dbc8195990a19110cf3d8962cd9993f4a6c"
	chartConfig   = "d7e3f36f8fb20d7a953f19a11be0bb7d44de5bba67ae1391de5e580582401e9c"
	chartLayer    = "e276b1eadf9a2b0758abd1888ba21ad857975140ccdbce7274e64286ac0f7ecd"
	chartProv     = "1373cef0d2e08c2b66373e673d791af873df9f3cdc40e4ba329dd39c3cbb04fe"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "helm", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// helmPush stores body as the manifest of name in c as Helm 3.18 and later
// push a chart: by its digest, then by its tag, in two requests.
func (w *world) helmPush(c repo.Config, name, body, digest, tag string) {
	w.t.Helper()
	w.pushTo(c, name, body, digest, layout.DigestFolder(digest))
	w.pushTo(c, name, body, digest, tag)
}

// helmChart uploads a chart of name at version to c's image name, as Helm
// would before its manifest, and returns the manifest in Helm's shape: no
// mediaType, a Helm config and one chart layer.
func (w *world) helmChart(c repo.Config, name, version string) (string, string) {
	w.t.Helper()
	return w.helmRebuild(c, name, version, 0)
}

// helmRebuild is helmChart for the build'th packaging of the same chart
// version, whose archive, and so whose manifest, differs from the others'.
func (w *world) helmRebuild(c repo.Config, name, version string, build int) (string, string) {
	w.t.Helper()
	config := `{"name":"` + name + `","version":"` + version + `","apiVersion":"v2"}`
	layer := "chart " + name + " " + version + " packaged " + strconv.Itoa(build)
	for _, b := range []string{config, layer} {
		w.deploy(c, layout.UploadPath(name, sum(b)), b, start)
	}
	body := `{"schemaVersion":2,"config":{"mediaType":"` + layout.MediaTypeHelmConfig + `",` +
		`"digest":"sha256:` + sum(config) + `","size":` + strconv.Itoa(len(config)) + `},` +
		`"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip",` +
		`"digest":"sha256:` + sum(layer) + `","size":` + strconv.Itoa(len(layer)) + `}]}`
	return body, sum(body)
}

func (w *world) helmRepo(edit func(*repo.Config)) repo.Config {
	c := &repo.Config{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI}
	if edit != nil {
		edit(c)
	}
	return w.repoOf(c)
}

// metadataOf is the index's metadata of the image's tag.
func (w *world) metadataOf(c repo.Config, name, tag string) string {
	w.t.Helper()
	ctx := context.Background()
	p, err := w.db.Packages().Find(ctx, c.Key, name)
	if err != nil {
		w.t.Fatal(err)
	}
	v, err := w.db.Packages().Version(ctx, p.ID, tag)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(v.Metadata)
}

// A chart pushed as Helm pushes it, with its provenance, is stored like any
// image: its tag folder holds the manifest and all three blobs, a digest
// folder holds the same manifest from the push by digest, and only the tag
// is a version, whose metadata is the chart's own name, version, with its
// "+", and appVersion. Its config sets no platform or label properties.
func TestHelmChartPush(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := w.helmRepo(nil)
		for _, f := range []string{
			"config.json", "acme-1.2.3+build.4.tgz",
			"acme-1.2.3+build.4.tgz.prov",
		} {
			body := readTestdata(t, f)
			w.deploy(c, layout.UploadPath("acme", sum(body)), body, start)
		}
		body := readTestdata(t, "manifest.json")
		if sum(body) != chartManifest {
			t.Fatalf("testdata/helm/manifest.json changed: %s", sum(body))
		}
		w.helmPush(c, "acme", body, chartManifest, chartTag)

		want := []string{chartTag, layout.DigestFolder(chartManifest)}
		if diff := cmp.Diff(sorted(want), w.folders(c, "acme")); diff != "" {
			t.Fatalf("folders:\n%s", diff)
		}
		wantTree := map[string]string{
			"manifest.json":          chartManifest,
			"sha256__" + chartConfig: chartConfig,
			"sha256__" + chartLayer:  chartLayer,
			"sha256__" + chartProv:   chartProv,
		}
		for _, dir := range want {
			if diff := cmp.Diff(wantTree, w.treeOf(c, "acme/"+dir)); diff != "" {
				t.Fatalf("%s:\n%s", dir, diff)
			}
		}
		if tags := w.tags(c, "acme"); !slices.Equal(tags, []string{chartTag}) {
			t.Fatalf("tags %v", tags)
		}
		if got := w.metadataOf(c, "acme", chartTag); got !=
			`{"chart":{"name":"acme","version":"1.2.3+build.4","appVersion":"2.0.1"}}` {
			t.Fatalf("metadata %s", got)
		}
		p := w.propertiesOf(c, "acme/"+chartTag+"/manifest.json")
		if !p.Has(layout.ManifestProperty, chartTag) ||
			!p.Has(layout.ManifestTypeProperty, layout.MediaTypeOCIManifest) ||
			len(p[layout.OSProperty]) != 0 || len(p[layout.ArchitectureProperty]) != 0 {
			t.Fatalf("properties %v", p)
		}
	})
}

// Max Unique Tags keeps N tags of a chart pushed as Helm 3.18 pushes it,
// not N manifest folders: each tag it removes takes its digest twin along,
// to the trash can, and the twins of the tags it keeps stay (decision
// 0039; known issue RTDEV-88173).
func TestMaxUniqueTagsTakesTheDigestTwin(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		limited := w.helmRepo(func(c *repo.Config) { c.MaxUniqueTags = ptr(2) })
		var digests []string
		for i, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
			body, digest := w.helmChart(limited, "acme", v)
			digests = append(digests, digest)
			w.at(c, i+1)
			w.helmPush(limited, "acme", body, digest, v)
		}
		want := sorted([]string{
			"1.1.0", "2.0.0",
			layout.DigestFolder(digests[1]), layout.DigestFolder(digests[2]),
		})
		if diff := cmp.Diff(want, w.folders(limited, "acme")); diff != "" {
			t.Fatalf("folders:\n%s", diff)
		}
		for _, gone := range []string{"1.0.0", layout.DigestFolder(digests[0])} {
			if !w.trashed(limited, "acme/"+gone) {
				t.Errorf("%s is not in the trash can", gone)
			}
		}
		if diff := cmp.Diff([]string{"1.1.0", "2.0.0"}, w.tags(limited, "acme")); diff != "" {
			t.Fatalf("tags:\n%s", diff)
		}
	})
}

// Under Tag Retention a chart version pushed again keeps its previous
// manifest in the digest twin Helm's push by digest left, which now names
// the tag as a retained version; retention counts and trims it like any
// other, and Max Unique Tags removes the tag with its retained versions and
// its current twin.
func TestTagRetentionAdoptsTheDigestTwin(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		kept := w.helmRepo(func(c *repo.Config) { c.DockerTagRetention = ptr(3) })
		var digests []string
		for i := range 4 {
			body, digest := w.helmRebuild(kept, "acme", "1.0.0", i)
			digests = append(digests, digest)
			w.at(c, i+1)
			w.helmPush(kept, "acme", body, digest, "1.0.0")
		}
		want := sorted([]string{
			"1.0.0",
			layout.DigestFolder(digests[1]), layout.DigestFolder(digests[2]),
			layout.DigestFolder(digests[3]),
		})
		if diff := cmp.Diff(want, w.folders(kept, "acme")); diff != "" {
			t.Fatalf("folders:\n%s", diff)
		}
		if !w.trashed(kept, "acme/"+layout.DigestFolder(digests[0])) {
			t.Error("the oldest retained version is not in the trash can")
		}
		ks, err := layout.Retained(context.Background(), w.db.Items(), kept.Key, "acme", "1.0.0")
		if err != nil || len(ks) != 2 ||
			ks[0].Path != "acme/"+layout.DigestFolder(digests[1]) ||
			ks[1].Path != "acme/"+layout.DigestFolder(digests[2]) {
			t.Fatalf("retained %+v %v", ks, err)
		}
		twin := "acme/" + layout.DigestFolder(digests[3]) + "/manifest.json"
		if got := w.propertiesOf(kept, twin)[layout.ManifestProperty]; !slices.Equal(
			got, []string{layout.DigestFolder(digests[3])}) {
			t.Fatalf("the current twin names %v", got)
		}

		limited := kept
		limited.MaxUniqueTags = ptr(1)
		body, digest := w.helmChart(kept, "acme", "2.0.0")
		w.at(c, 9)
		w.helmPush(limited, "acme", body, digest, "2.0.0")
		want = sorted([]string{"2.0.0", layout.DigestFolder(digest)})
		if diff := cmp.Diff(want, w.folders(kept, "acme")); diff != "" {
			t.Fatalf("after the limit:\n%s", diff)
		}
	})
}

// With Tag Retention at 1, a chart version pushed again leaves its previous
// twin as any manifest pushed by digest stays; Max Unique Tags then removes
// the tag with its current twin only.
func TestTagRetentionOfOneLeavesTheTwin(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		r := w.helmRepo(nil)
		first, firstDigest := w.helmChart(r, "acme", "1.0.0")
		second, secondDigest := w.helmRebuild(r, "acme", "1.0.0", 1)
		w.at(c, 1)
		w.helmPush(r, "acme", first, firstDigest, "1.0.0")
		w.at(c, 2)
		w.helmPush(r, "acme", second, secondDigest, "1.0.0")
		old := "acme/" + layout.DigestFolder(firstDigest) + "/manifest.json"
		if got := w.propertiesOf(r, old)[layout.ManifestProperty]; !slices.Equal(
			got, []string{layout.DigestFolder(firstDigest)}) {
			t.Fatalf("the previous twin names %v", got)
		}

		limited := r
		limited.MaxUniqueTags = ptr(1)
		body, digest := w.helmChart(r, "acme", "2.0.0")
		w.at(c, 3)
		w.helmPush(limited, "acme", body, digest, "2.0.0")
		want := sorted([]string{
			"2.0.0", layout.DigestFolder(digest), layout.DigestFolder(firstDigest),
		})
		if diff := cmp.Diff(want, w.folders(r, "acme")); diff != "" {
			t.Fatalf("after the limit:\n%s", diff)
		}
	})
}

// present reports whether path is in c.
func (w *world) present(c repo.Config, path string) bool {
	w.t.Helper()
	_, err := w.item(c, path)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

// pushListTo stores the image index body as the manifest of name in c's
// folder.
func (w *world) pushListTo(c repo.Config, name, body, folder string) {
	w.t.Helper()
	m, err := layout.ParseManifest(layout.MediaTypeOCIIndex, []byte(body), sum(body))
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.svc.PutManifest(context.Background(), registry.Push{
		Owner:    registry.Owner{Repo: c, Name: name, By: "alice"},
		Manifest: m, Body: []byte(body), Folders: []string{folder},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// A twin that another tag still holds, or that a list names, is not lone:
// Tag Retention leaves it naming itself and Max Unique Tags leaves it in
// place until nothing else keeps it. A digest folder naming another tag is
// that tag's retained version and stays, and a client's own delete of a tag
// removes only the tag (decision 0031).
func TestTwinsKeptByOthers(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		c := &clock{}
		w.svc.Now = c.now
		r := w.helmRepo(func(c *repo.Config) { c.DockerTagRetention = ptr(2) })
		owner := func(name string) registry.Owner {
			return registry.Owner{Repo: r, Name: name, By: "alice"}
		}
		twin := func(name, digest string) string {
			return name + "/" + layout.DigestFolder(digest)
		}
		names := func(dir, want string) {
			t.Helper()
			got := w.propertiesOf(r, dir+"/manifest.json")[layout.ManifestProperty]
			if !slices.Equal(got, []string{want}) {
				t.Fatalf("%s names %v, want %s", dir, got, want)
			}
		}
		removeTwin := registry.Removal{Twin: true}

		// Another tag holds x: neither a push over v1 nor removing v2 takes
		// x's twin, and removing v3, x's last tag, does.
		x, xd := w.helmChart(r, "held", "1.0.0")
		y, yd := w.helmChart(r, "held", "1.1.0")
		w.at(c, 1)
		w.helmPush(r, "held", x, xd, "v1")
		w.pushTo(r, "held", x, xd, "v2", "v3")
		w.at(c, 2)
		w.pushTo(r, "held", y, yd, "v1")
		names(twin("held", xd), layout.DigestFolder(xd))
		if err := w.svc.RemoveTag(ctx, owner("held"), "v2", removeTwin); err != nil {
			t.Fatal(err)
		}
		names(twin("held", xd), layout.DigestFolder(xd))
		if err := w.svc.RemoveTag(ctx, owner("held"), "v3", removeTwin); err != nil {
			t.Fatal(err)
		}
		if w.present(r, twin("held", xd)) {
			t.Error("x's twin outlived its last tag")
		}

		// A list names x: a push over its tag and removing its other tag
		// leave its twin.
		x, xd = w.helmChart(r, "listed", "1.0.0")
		y, yd = w.helmChart(r, "listed", "1.1.0")
		w.helmPush(r, "listed", x, xd, "v1")
		list, _ := indexOf(layout.MediaTypeOCIIndex, x)
		w.pushListTo(r, "listed", list, "multi")
		w.pushTo(r, "listed", y, yd, "v1")
		names(twin("listed", xd), layout.DigestFolder(xd))
		w.pushTo(r, "listed", x, xd, "v2")
		if err := w.svc.RemoveTag(ctx, owner("listed"), "v2", removeTwin); err != nil {
			t.Fatal(err)
		}
		names(twin("listed", xd), layout.DigestFolder(xd))

		// x's digest folder is v1's retained version, so removing v3, which
		// holds x too, leaves it.
		x, xd = w.helmChart(r, "retained", "1.0.0")
		y, yd = w.helmChart(r, "retained", "1.1.0")
		w.pushTo(r, "retained", x, xd, "v1")
		w.pushTo(r, "retained", y, yd, "v1")
		names(twin("retained", xd), "v1")
		w.pushTo(r, "retained", x, xd, "v3")
		if err := w.svc.RemoveTag(ctx, owner("retained"), "v3", removeTwin); err != nil {
			t.Fatal(err)
		}
		names(twin("retained", xd), "v1")

		// A client's delete of a tag leaves its twin.
		x, xd = w.helmChart(r, "deleted", "1.0.0")
		w.helmPush(r, "deleted", x, xd, "v1")
		if err := w.svc.DeleteTag(ctx, owner("deleted"), "v1"); err != nil {
			t.Fatal(err)
		}
		names(twin("deleted", xd), layout.DigestFolder(xd))
	})
}
