package registry_test

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// descriptorOf is the descriptor of a manifest body of mediaType.
func descriptorOf(mediaType, body string) string {
	return `{"mediaType":"` + mediaType + `","digest":"sha256:` + sum(body) +
		`","size":` + strconv.Itoa(len(body)) + `}`
}

// indexOf is an index of mediaType listing manifests, with its SHA-256.
func indexOf(mediaType string, manifests ...string) (string, string) {
	var ds []string
	for _, m := range manifests {
		ds = append(ds, descriptorOf(layout.MediaTypeOCIManifest, m))
	}
	body := `{"schemaVersion":2,"mediaType":"` + mediaType + `","manifests":[` +
		strings.Join(ds, ",") + `]}`
	return body, sum(body)
}

// emptyConfig is the OCI empty descriptor's content, an artifact's config.
const emptyConfig = "{}"

// referrerOf is an artifact manifest of artifactType that refers to the
// manifest subject, with a note annotation, and its SHA-256. An empty
// artifactType leaves the field out, so the config's type stands in.
func referrerOf(artifactType, subject, note string) (string, string) {
	t := ""
	if artifactType != "" {
		t = `"artifactType":"` + artifactType + `",`
	}
	body := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` + t +
		`"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:` +
		sum(emptyConfig) + `","size":2},` +
		`"layers":[{"mediaType":"application/octet-stream","digest":"sha256:` + sum(layerA) +
		`","size":7}],"subject":` + descriptorOf(layout.MediaTypeOCIManifest, subject) +
		`,"annotations":{"note":"` + note + `"}}`
	return body, sum(body)
}

// items is the item service over w's store.
func (w *world) items() *item.Service { return &item.Service{Store: w.db, Blobs: w.blobs} }

// target is folder of the image app in docker-local.
func (w *world) target(folder string) item.Target {
	return item.Target{Repo: w.docker, Path: "app/" + folder}
}

// pushAs stores body, of mediaType, as a manifest of the image app in
// folders.
func (w *world) pushAs(mediaType, body, digest string, folders ...string) error {
	m, err := layout.ParseManifest(mediaType, []byte(body), digest)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.svc.PutManifest(context.Background(), registry.Push{
		Owner: w.owner("app"), Manifest: m, Body: []byte(body), Folders: folders,
	})
}

// platforms pushes three image manifests of app by digest, as clients push
// a multi-architecture image's platforms before its index, and returns
// their bodies.
func (w *world) platforms() []string {
	w.t.Helper()
	w.upload(imageConfig, layerA, layerB)
	var out []string
	for _, layers := range [][]string{{layerA}, {layerB}, {layerA, layerB}} {
		body, digest := manifestOf(layers...)
		if err := w.push(body, digest, layout.DigestFolder(digest)); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

// An index or manifest list is stored by tag as list.manifest.json with
// its properties, beside nothing: its platform manifests stay in their own
// digest folders, where jf finds them (research 23, 5.1). Naming many
// platforms, it carries no docker.os or docker.architecture.
func TestPushIndex(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ps := w.platforms()
		for _, c := range []struct{ mediaType, tag string }{
			{layout.MediaTypeOCIIndex, "v1"},
			{layout.MediaTypeDockerList, "v2"},
		} {
			body, digest := indexOf(c.mediaType, ps[0], ps[1])
			if err := w.pushAs(c.mediaType, body, digest, c.tag); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(map[string]string{"list.manifest.json": digest},
				w.tree("app/"+c.tag)); diff != "" {
				t.Fatal(diff)
			}
			want := types.Properties{
				"docker.manifest":        {c.tag},
				"docker.manifest.digest": {"sha256:" + digest},
				"docker.manifest.type":   {c.mediaType},
				"docker.repoName":        {"app"},
				"sha256":                 {digest},
			}
			if diff := cmp.Diff(
				want,
				w.properties("app/"+c.tag+"/list.manifest.json"),
			); diff != "" {
				t.Fatal(diff)
			}
			it, err := layout.FindManifest(context.Background(), w.db.Items(), "docker-local",
				"app", digest)
			if err != nil || it.Name != layout.ListManifestFile {
				t.Fatalf("%+v %v", it, err)
			}
		}
		// An index may list an index, and a platform held only by a tag.
		inner, innerDigest := indexOf(layout.MediaTypeOCIIndex, ps[2])
		if err := w.pushAs(layout.MediaTypeOCIIndex, inner, innerDigest, "inner"); err != nil {
			t.Fatal(err)
		}
		outer := `{"schemaVersion":2,"manifests":[` +
			descriptorOf(layout.MediaTypeOCIIndex, inner) + `]}`
		if err := w.pushAs(layout.MediaTypeOCIIndex, outer, sum(outer), "outer"); err != nil {
			t.Fatal(err)
		}
	})
}

// Each platform manifest of a multi-architecture image carries its own
// docker.os and docker.architecture, which a property search finds; the
// config's variant goes into neither.
func TestPlatformProperties(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(layerA)
		var bodies []string
		paths := map[string]string{}
		for arch, config := range map[string]string{
			"amd64": `{"architecture":"amd64","os":"linux"}`,
			"arm64": `{"architecture":"arm64","os":"linux","variant":"v8"}`,
		} {
			w.upload(config)
			body, digest := manifestWith(config, layerA)
			if err := w.push(body, digest, layout.DigestFolder(digest)); err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, body)
			paths[arch] = "app/" + layout.DigestFolder(digest) + "/" + layout.ManifestFile
		}
		list, listDigest := indexOf(layout.MediaTypeOCIIndex, bodies...)
		if err := w.pushAs(layout.MediaTypeOCIIndex, list, listDigest, "v1"); err != nil {
			t.Fatal(err)
		}
		for arch, p := range paths {
			got := w.properties(p)
			if diff := cmp.Diff(types.Properties{
				layout.OSProperty: {"linux"}, layout.ArchitectureProperty: {arch},
			}, types.Properties{
				layout.OSProperty:           got[layout.OSProperty],
				layout.ArchitectureProperty: got[layout.ArchitectureProperty],
			}); diff != "" {
				t.Errorf("%s: %s", arch, diff)
			}
		}
		q := aql.Query{Criteria: aql.And{
			aql.RepoSet{Refs: []aql.RepoRef{{Key: "docker-local"}}},
			&aql.PropertyCriterion{
				Part: aql.PartPair, Key: layout.ArchitectureProperty, Op: aql.OpEq, Value: "arm64",
			},
		}}
		hits, err := w.db.Items().Search(context.Background(), &q, aql.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, h := range hits {
			found = append(found, h.Item.FullPath())
		}
		if diff := cmp.Diff([]string{paths["arm64"]}, found); diff != "" {
			t.Fatal(diff)
		}
	})
}

// Every manifest an index lists must be in the image, by tag or by digest;
// each one missing is named, under any algorithm, and nothing is stored.
func TestIndexUnknownChildren(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ps := w.platforms()
		absent, _ := manifestOf(layerB, layerA)
		// In another image, which does not count.
		w.deploy(w.docker, "other/sha256__"+sum(absent)+"/manifest.json", absent, start)
		body := `{"schemaVersion":2,"manifests":[` +
			descriptorOf(layout.MediaTypeOCIManifest, ps[0]) + `,` +
			descriptorOf(layout.MediaTypeOCIManifest, absent) + `,` +
			`{"mediaType":"x","digest":"sha512:` + strings.Repeat("a", 128) + `","size":1}]}`
		err := w.pushAs(layout.MediaTypeOCIIndex, body, sum(body), "v1")
		var unknown *registry.BlobsUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("err = %v", err)
		}
		want := []string{"sha256:" + sum(absent), "sha512:" + strings.Repeat("a", 128)}
		if diff := cmp.Diff(want, unknown.Digests); diff != "" {
			t.Fatal(diff)
		}
		if _, err := w.item(w.docker, "app/v1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stored: %v", err)
		}
	})
}

// A manifest with a subject carries oci.subject and oci.artifactType, its
// own type or its config's; the subject need not be in the registry.
func TestSubjectProperties(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(emptyConfig, layerA)
		for _, c := range []struct{ artifactType, want, tag string }{
			{"application/vnd.example.sig", "application/vnd.example.sig", "sig"},
			{"", "application/vnd.oci.empty.v1+json", "untyped"},
		} {
			body, digest := referrerOf(c.artifactType, "absent subject", c.tag)
			if err := w.push(body, digest, c.tag); err != nil {
				t.Fatal(err)
			}
			p := w.properties("app/" + c.tag + "/manifest.json")
			if !p.Has("oci.subject", "sha256:"+sum("absent subject")) ||
				!p.Has("oci.artifactType", c.want) {
				t.Fatalf("%s: %v", c.tag, p)
			}
		}
	})
}

// The referrers of a digest are the manifests of the image whose subject
// it is, once each and by digest, filtered by artifact type on request.
func TestReferrers(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		ps := w.platforms()
		w.upload(emptyConfig)
		sig, sigDigest := referrerOf("application/vnd.example.sig", ps[0], "signed")
		sbom, sbomDigest := referrerOf("application/vnd.example.sbom", ps[0], "listed")
		other, otherDigest := referrerOf("application/vnd.example.sig", ps[1], "other")
		// The signature by digest and by tag is one referrer.
		for _, p := range []struct {
			body, digest string
			folders      []string
		}{
			{sig, sigDigest, []string{layout.DigestFolder(sigDigest), "sig"}},
			{sbom, sbomDigest, []string{layout.DigestFolder(sbomDigest)}},
			{other, otherDigest, []string{layout.DigestFolder(otherDigest)}},
		} {
			if err := w.push(p.body, p.digest, p.folders...); err != nil {
				t.Fatal(err)
			}
		}
		// A referrer of the same subject in a deeper image is not this
		// image's.
		deep := w.deploy(
			w.docker,
			"app/deeper/sha256__"+sum("deep")+"/manifest.json",
			"deep",
			start,
		)
		if err := w.db.Items().PatchProperties(ctx, deep.ID,
			types.Properties{"oci.subject": {"sha256:" + sum(ps[0])}}, nil); err != nil {
			t.Fatal(err)
		}
		got, err := w.svc.Referrers(ctx, w.owner("app"), "sha256:"+sum(ps[0]), "")
		if err != nil {
			t.Fatal(err)
		}
		descriptor := func(body, digest, artifactType, note string) layout.Descriptor {
			return layout.Descriptor{
				MediaType: layout.MediaTypeOCIManifest, Digest: "sha256:" + digest,
				Size: int64(len(body)), ArtifactType: artifactType,
				Annotations: map[string]string{"note": note},
			}
		}
		want := []layout.Descriptor{
			descriptor(sig, sigDigest, "application/vnd.example.sig", "signed"),
			descriptor(sbom, sbomDigest, "application/vnd.example.sbom", "listed"),
		}
		if want[0].Digest > want[1].Digest {
			want[0], want[1] = want[1], want[0]
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatal(diff)
		}
		got, err = w.svc.Referrers(ctx, w.owner("app"), "sha256:"+sum(ps[0]),
			"application/vnd.example.sbom")
		if err != nil || len(got) != 1 || got[0].Digest != "sha256:"+sbomDigest {
			t.Fatalf("%+v %v", got, err)
		}
		for _, subject := range []string{"sha256:" + sum(ps[2]), "sha512:" + strings.Repeat("a", 128)} {
			got, err = w.svc.Referrers(ctx, w.owner("app"), subject, "")
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("%s: %+v %v", subject, got, err)
			}
		}
	})
}

// A referrer deployed through the REST API has properties but no type; it
// is listed without one, and a body that is no manifest without annotations.
func TestReferrersDeployedThroughREST(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		for path, body := range map[string]string{
			"app/a/manifest.json":      `{"schemaVersion":2,"config":{"mediaType":"x","digest":"sha256:` + sum("c") + `","size":1},"annotations":{"k":"v"}}`,
			"app/b/manifest.json":      "not a manifest",
			"app/c/list.manifest.json": `{"schemaVersion":2,"manifests":[],"annotations":{"list":"yes"}}`,
		} {
			it := w.deploy(w.docker, path, body, start)
			if err := w.db.Items().PatchProperties(ctx, it.ID,
				types.Properties{"oci.subject": {"sha256:" + sum("s")}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, err := w.svc.Referrers(ctx, w.owner("app"), "sha256:"+sum("s"), "")
		if err != nil || len(got) != 3 {
			t.Fatalf("%+v %v", got, err)
		}
		notes := map[string]string{}
		for _, d := range got {
			notes[d.MediaType] = strings.Join(
				[]string{d.Annotations["k"], d.Annotations["list"]},
				"",
			)
		}
		want := map[string]string{
			layout.MediaTypeOCIManifest: "v", layout.MediaTypeOCIIndex: "yes", "": "",
		}
		if diff := cmp.Diff(want, notes); diff != "" {
			t.Fatal(diff)
		}
	})
}

// Deleting a multi-architecture tag removes its platform manifests that no
// other tag holds and no other index lists, as Artifactory does from
// 7.118.0, and nothing else.
func TestDeleteMultiArch(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		ps := w.platforms()
		folder := func(body string) string { return layout.DigestFolder(sum(body)) }
		v1, d1 := indexOf(layout.MediaTypeOCIIndex, ps[0], ps[1], ps[2])
		v2, d2 := indexOf(layout.MediaTypeDockerList, ps[1])
		for _, p := range []struct{ body, digest, mediaType, tag string }{
			{v1, d1, layout.MediaTypeOCIIndex, "v1"},
			{v2, d2, layout.MediaTypeDockerList, "v2"},
		} {
			if err := w.pushAs(p.mediaType, p.body, p.digest, p.tag); err != nil {
				t.Fatal(err)
			}
		}
		// The third platform is also the image of a tag of its own.
		if err := w.push(ps[2], sum(ps[2]), "solo"); err != nil {
			t.Fatal(err)
		}
		o := w.owner("app")
		got, err := w.svc.UnreferencedPlatforms(ctx, o, "v1")
		if err != nil || !cmp.Equal(got, []string{folder(ps[0])}) {
			t.Fatalf("%v %v", got, err)
		}
		removed, err := w.svc.DeleteMultiArch(ctx, o, "v1")
		if err != nil || !cmp.Equal(removed, []string{"v1", folder(ps[0])}) {
			t.Fatalf("%v %v", removed, err)
		}
		for path, kept := range map[string]bool{
			"app/v1": false, "app/" + folder(ps[0]): false,
			"app/" + folder(ps[1]): true, "app/" + folder(ps[2]): true,
			"app/v2": true, "app/solo": true,
		} {
			_, err := w.item(w.docker, path)
			if (err == nil) != kept {
				t.Errorf("%s: kept %v, err %v", path, kept, err)
			}
		}
		// A tag holding an image has no platforms, and goes alone.
		if removed, err := w.svc.DeleteMultiArch(ctx, o, "solo"); err != nil ||
			!cmp.Equal(removed, []string{"solo"}) {
			t.Fatalf("%v %v", removed, err)
		}
		// The last list's platform goes with it, unless the list is also
		// stored by digest, which lists it still.
		if err := w.pushAs(
			layout.MediaTypeDockerList,
			v2,
			d2,
			layout.DigestFolder(d2),
			"v3",
		); err != nil {
			t.Fatal(err)
		}
		if got, err := w.svc.UnreferencedPlatforms(ctx, o, "v2"); err != nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
		for _, f := range []string{layout.DigestFolder(d2), "v3"} {
			if _, err := w.items().Delete(ctx, w.target(f), "alice"); err != nil {
				t.Fatal(err)
			}
		}
		if removed, err := w.svc.DeleteMultiArch(ctx, o, "v2"); err != nil ||
			!cmp.Equal(removed, []string{"v2", folder(ps[1])}) {
			t.Fatalf("%v %v", removed, err)
		}
		// No such tag, or a file where it would be.
		w.deploy(w.docker, "app/file", "a file", start)
		for _, tag := range []string{"absent", "file"} {
			if _, err := w.svc.DeleteMultiArch(ctx, o, tag); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: %v", tag, err)
			}
		}
		if got, err := w.svc.UnreferencedPlatforms(ctx, o, "absent"); err != nil || got != nil {
			t.Fatalf("%v %v", got, err)
		}
	})
}

// A platform listed by the deleted list whose folder is already gone is
// skipped, and one under another algorithm has no folder to remove.
func TestDeleteMultiArchMissingPlatforms(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		ps := w.platforms()
		body := `{"schemaVersion":2,"manifests":[` +
			descriptorOf(layout.MediaTypeOCIManifest, ps[0]) + `,` +
			`{"mediaType":"x","digest":"sha512:` + strings.Repeat("a", 128) + `","size":1}]}`
		// Deployed through the REST API, so it was never checked.
		it := w.deploy(w.docker, "app/v1/list.manifest.json", body, start)
		if err := w.db.Items().PatchProperties(ctx, it.ID, types.Properties{
			"docker.manifest.type": {layout.MediaTypeOCIIndex},
		}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := w.items().
			Delete(ctx, w.target(layout.DigestFolder(sum(ps[0]))), "alice"); err != nil {
			t.Fatal(err)
		}
		removed, err := w.svc.DeleteMultiArch(ctx, w.owner("app"), "v1")
		if err != nil || !cmp.Equal(removed, []string{"v1"}) {
			t.Fatalf("%v %v", removed, err)
		}
	})
}

// propertiesFailing fails reading properties.
type propertiesFailing struct{ store.ItemStore }

func (propertiesFailing) Properties(context.Context, int64) (types.Properties, error) {
	return nil, errBoom
}

// openFailingFor is a blob store whose content hex cannot be opened.
type openFailingFor struct {
	blob.Store
	hex string
}

func (o openFailingFor) Open(
	ctx context.Context,
	hex string,
) (io.ReadSeekCloser, blob.Info, error) {
	if hex == o.hex {
		return nil, blob.Info{}, errBoom
	}
	return o.Store.Open(ctx, hex) //nolint:wrapcheck // test double
}

func TestIndexStoreFailures(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, f *faulty, ps []string) error{
		"find a child": func(_ *testing.T, f *faulty, ps []string) error {
			f.st.items.search = errBoom
			body, digest := indexOf(layout.MediaTypeOCIIndex, ps[0])
			return f.pushAs(layout.MediaTypeOCIIndex, body, digest, "v9")
		},
		"find referrers": func(_ *testing.T, f *faulty, ps []string) error {
			f.st.items.search = errBoom
			_, err := f.svc.Referrers(ctx, f.owner("app"), "sha256:"+sum(ps[0]), "")
			return err
		},
		"read a referrer": func(_ *testing.T, f *faulty, ps []string) error {
			sig, digest := referrerOf("application/vnd.example.sig", ps[0], "x")
			if err := f.push(sig, digest, "sig"); err != nil {
				return err
			}
			f.svc.Blobs = openFailingFor{f.blobs, digest}
			_, err := f.svc.Referrers(ctx, f.owner("app"), "sha256:"+sum(ps[0]), "")
			return err
		},
		"get the list": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/v1/list.manifest.json"}
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"the list's properties": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.ItemStore = propertiesFailing{f.db.Items()}
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"read the list": func(_ *testing.T, f *faulty, ps []string) error {
			_, digest := indexOf(layout.MediaTypeOCIIndex, ps[0], ps[1])
			f.svc.Blobs = openFailingFor{f.blobs, digest}
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"list the image": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.search = errBoom
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"read another list": func(_ *testing.T, f *faulty, ps []string) error {
			body, digest := indexOf(layout.MediaTypeDockerList, ps[1])
			if err := f.pushAs(layout.MediaTypeDockerList, body, digest, "v2"); err != nil {
				return err
			}
			f.svc.Blobs = openFailingFor{f.blobs, digest}
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"look at a platform": func(_ *testing.T, f *faulty, ps []string) error {
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/sha256__" + sum(ps[0])}
			_, err := f.svc.UnreferencedPlatforms(ctx, f.owner("app"), "v1")
			return err
		},
		"look at the tag": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.ItemStore = getFailing{f.db.Items(), "app/v1"}
			_, err := f.svc.DeleteMultiArch(ctx, f.owner("app"), "v1")
			return err
		},
		"find the platforms": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.search = errBoom
			_, err := f.svc.DeleteMultiArch(ctx, f.owner("app"), "v1")
			return err
		},
		"delete": func(_ *testing.T, f *faulty, _ []string) error {
			f.st.items.del = errBoom
			_, err := f.svc.DeleteMultiArch(ctx, f.owner("app"), "v1")
			return err
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFaulty(t)
			ps := f.platforms()
			f.upload(emptyConfig)
			body, digest := indexOf(layout.MediaTypeOCIIndex, ps[0], ps[1])
			if err := f.pushAs(layout.MediaTypeOCIIndex, body, digest, "v1"); err != nil {
				t.Fatal(err)
			}
			if err := c(t, f, ps); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
