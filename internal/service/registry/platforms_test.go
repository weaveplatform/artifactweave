package registry_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Configs of a multi-architecture image's other platform and of its
// attestation manifests, which buildx lists under unknown/unknown.
const (
	armConfig    = `{"architecture":"arm64","os":"linux"}`
	attestConfig = `{"attestation":"in-toto"}`
)

// entry is an index entry for the image manifest body, naming platform as
// os/architecture[/variant]; "" names none, and "/" an empty one.
func entry(body, platform string) string {
	d := `{"mediaType":"` + layout.MediaTypeOCIManifest + `","digest":"sha256:` + sum(body) +
		`","size":` + strconv.Itoa(len(body))
	if platform != "" {
		parts := strings.Split(platform, "/")
		d += `,"platform":{"os":"` + parts[0] + `","architecture":"` + parts[1] + `"`
		if len(parts) == 3 {
			d += `,"variant":"` + parts[2] + `"`
		}
		d += `}`
	}
	return d + `}`
}

// platformIndex is an OCI image index of entries, with its SHA-256.
func platformIndex(entries ...string) (string, string) {
	body := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIIndex + `","manifests":[` +
		strings.Join(entries, ",") + `]}`
	return body, sum(body)
}

// multiArch is a buildx-style image of name in c: an amd64 and an arm64
// manifest and an attestation for each, pushed by digest as clients push
// them before the index, which it returns with its SHA-256. bodies holds
// the four platform manifests in the index's order.
type multiArch struct {
	bodies     []string
	list, hash string
}

func (w *world) multiArch(c repo.Config, name string) multiArch {
	w.t.Helper()
	for _, b := range []string{imageConfig, armConfig, attestConfig, layerA, layerB} {
		w.deploy(c, layout.UploadPath(name, sum(b)), b, start)
	}
	var out multiArch
	var entries []string
	for _, p := range []struct{ config, layer, platform string }{
		{imageConfig, layerA, "linux/amd64"},
		{armConfig, layerB, "linux/arm64"},
		{attestConfig, layerA, "unknown/unknown"},
		{attestConfig, layerB, "unknown/unknown"},
	} {
		body, digest := manifestWith(p.config, p.layer)
		w.pushTo(c, name, body, digest, layout.DigestFolder(digest))
		out.bodies = append(out.bodies, body)
		entries = append(entries, entry(body, p.platform))
	}
	out.list, out.hash = platformIndex(entries...)
	return out
}

// pushList stores the index body of name in c's folders.
func (w *world) pushList(c repo.Config, name, body, digest string, folders ...string) {
	w.t.Helper()
	m, err := layout.ParseManifest(layout.MediaTypeOCIIndex, []byte(body), digest)
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

// digestFolders are the digest folders of bodies.
func digestFolders(bodies ...string) []string {
	var out []string
	for _, b := range bodies {
		out = append(out, layout.DigestFolder(sum(b)))
	}
	return out
}

// A list names a descriptive tag <tag>-<os>-<architecture> for each entry
// with a platform: unknown stands in for an empty field, the variant is
// left out, a name given twice holds the last entry giving it, and a name
// outside the tag grammar is skipped. An image manifest, a digest folder
// and an entry with no platform name none.
func TestAccPlatformTags(t *testing.T) {
	amd, _ := manifestWith(imageConfig, layerA)
	arm, _ := manifestWith(armConfig, layerB)
	att1, _ := manifestWith(attestConfig, layerA)
	att2, _ := manifestWith(attestConfig, layerB)
	empty, _ := manifestWith(attestConfig, layerA, layerB)
	body, hex := platformIndex(
		entry(amd, "linux/amd64"), entry(arm, "linux/arm/v7"), entry(att1, ""),
		entry(att1, "unknown/unknown"), entry(att2, "unknown/unknown"), entry(empty, "/"),
		`{"mediaType":"`+layout.MediaTypeOCIManifest+`","digest":"sha512:`+
			strings.Repeat("a", 128)+`","size":1,"platform":{"os":"linux","architecture":"s390x"}}`,
	)
	m, err := layout.ParseManifest(layout.MediaTypeOCIIndex, []byte(body), hex)
	if err != nil {
		t.Fatal(err)
	}
	want := []registry.PlatformTag{
		{Tag: "v1-linux-amd64", Hex: sum(amd)},
		{Tag: "v1-linux-arm", Hex: sum(arm)},
		{Tag: "v1-unknown-unknown", Hex: sum(empty)},
	}
	if diff := cmp.Diff(want, registry.PlatformTags(m, "v1")); diff != "" {
		t.Fatal(diff)
	}
	long := strings.Repeat("t", 120)
	for name, got := range map[string][]registry.PlatformTag{
		"a digest folder": registry.PlatformTags(m, layout.DigestFolder(hex)),
		"a long tag":      registry.PlatformTags(m, long),
	} {
		if len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
	image, _ := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(amd), sum(amd))
	if got := registry.PlatformTags(image, "v1"); len(got) != 0 {
		t.Errorf("an image: %v", got)
	}
}

// A list pushed to a tag also tags each platform manifest
// <tag>-<os>-<architecture>, the attestations <tag>-unknown-unknown, each a
// tag folder holding the manifest and its blobs with the tag in its
// properties, as 7.x does from 7.21.2. Pushing the list again changes
// nothing, and a push by digest with tags tags the platforms for each.
func TestAccListPushAddsPlatformTags(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ma := w.multiArch(w.docker, "app")
		for range 2 {
			w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
		}
		tags := []string{"v1", "v1-linux-amd64", "v1-linux-arm64", "v1-unknown-unknown"}
		want := sorted(append(slices.Clone(tags), digestFolders(ma.bodies...)...))
		if diff := cmp.Diff(want, w.folders(w.docker, "app")); diff != "" {
			t.Fatalf("folders:\n%s", diff)
		}
		if diff := cmp.Diff(tags, sorted(w.tags(w.docker, "app"))); diff != "" {
			t.Fatalf("tags:\n%s", diff)
		}
		if diff := cmp.Diff(map[string]string{
			"manifest.json":             sum(ma.bodies[1]),
			"sha256__" + sum(armConfig): sum(armConfig),
			"sha256__" + sum(layerB):    sum(layerB),
		}, w.tree("app/v1-linux-arm64")); diff != "" {
			t.Fatalf("v1-linux-arm64:\n%s", diff)
		}
		p := w.properties("app/v1-linux-arm64/manifest.json")
		if !slices.Equal(p[layout.ManifestProperty], []string{"v1-linux-arm64"}) ||
			!slices.Equal(p[layout.ArchitectureProperty], []string{"arm64"}) ||
			!slices.Equal(p[layout.RepoNameProperty], []string{"app"}) {
			t.Fatalf("properties %v", p)
		}
		// The second attestation, listed last, holds the shared name.
		if got := w.tree("app/v1-unknown-unknown")["manifest.json"]; got != sum(ma.bodies[3]) {
			t.Fatalf("v1-unknown-unknown holds %s", got)
		}
		w.pushList(w.docker, "app", ma.list, ma.hash, layout.DigestFolder(ma.hash), "v2")
		for _, tag := range []string{"v2-linux-amd64", "v2-linux-arm64", "v2-unknown-unknown"} {
			if !slices.Contains(w.folders(w.docker, "app"), tag) {
				t.Errorf("%s missing", tag)
			}
		}
		if slices.ContainsFunc(w.folders(w.docker, "app"), func(f string) bool {
			return strings.HasPrefix(f, layout.DigestFolder(ma.hash)+"-")
		}) {
			t.Error("a digest folder got descriptive tags")
		}
	})
}

// A list pushed over a tag that alone held one of its platforms keeps that
// platform in its descriptive tag, as 7.x moved it there (RTFACT-26810), so
// the list never names a manifest the image lost.
func TestAccListOverwritesItsOwnPlatform(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(imageConfig, layerA)
		amd, amdDigest := manifestWith(imageConfig, layerA)
		w.pushTo(w.docker, "app", amd, amdDigest, "v1")
		list, hash := platformIndex(entry(amd, "linux/amd64"))
		w.pushList(w.docker, "app", list, hash, "v1")
		if diff := cmp.Diff(
			[]string{"v1", "v1-linux-amd64"},
			w.folders(w.docker, "app"),
		); diff != "" {
			t.Fatal(diff)
		}
		if got := w.tree("app/v1-linux-amd64")["manifest.json"]; got != amdDigest {
			t.Fatalf("v1-linux-amd64 holds %s", got)
		}
		if _, err := layout.FindManifest(context.Background(), w.db.Items(), "docker-local",
			"app", amdDigest); err != nil {
			t.Fatal(err)
		}
	})
}

// A list overwritten by an image takes its descriptive tags and the
// platform manifests nothing else names along, to the trash can, as 7.x
// removes "all of the sub-manifests related to the original revision".
// Under Tag Retention the previous revision is kept whole instead.
func TestAccListOverwrittenByImage(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ma := w.multiArch(w.docker, "app")
		w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
		// Another list names the arm64 platform, which so stays.
		other, otherHash := platformIndex(entry(ma.bodies[1], ""))
		w.pushList(w.docker, "app", other, otherHash, "other")
		image, imageDigest := manifestOf(layerB)
		w.pushTo(w.docker, "app", image, imageDigest, "v1")
		want := sorted(append([]string{"v1", "other"}, digestFolders(ma.bodies[1])...))
		if diff := cmp.Diff(want, w.folders(w.docker, "app")); diff != "" {
			t.Fatalf("after the overwrite:\n%s", diff)
		}
		for _, gone := range append([]string{"v1-linux-amd64", "v1-unknown-unknown"},
			digestFolders(ma.bodies[0], ma.bodies[2], ma.bodies[3])...) {
			if !w.trashed(w.docker, "app/"+gone) {
				t.Errorf("%s is not in the trash can", gone)
			}
		}

		kept := w.repoOf(&repo.Config{
			Key: "docker-kept", Class: repo.Local, PackageType: repo.Docker,
			DockerTagRetention: ptr(2),
		})
		ma = w.multiArch(kept, "app")
		w.pushList(kept, "app", ma.list, ma.hash, "v1")
		for _, b := range []string{imageConfig, layerB} {
			w.deploy(kept, layout.UploadPath("app", sum(b)), b, start)
		}
		w.pushTo(kept, "app", image, imageDigest, "v1")
		want = sorted(slices.Concat(
			[]string{"v1", "v1-linux-amd64", "v1-linux-arm64", "v1-unknown-unknown"},
			digestFolders(ma.bodies...), []string{layout.DigestFolder(ma.hash)},
		))
		if diff := cmp.Diff(want, w.folders(kept, "app")); diff != "" {
			t.Fatalf("under Tag Retention:\n%s", diff)
		}
	})
}

// A list overwritten by another list keeps the previous platform
// manifests, 7.x's default (DV-084); its descriptive tags follow the new
// list.
func TestAccListOverwrittenByList(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ma := w.multiArch(w.docker, "app")
		w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
		w.upload(armConfig, layerA)
		arm2, arm2Digest := manifestWith(armConfig, layerA)
		w.pushTo(w.docker, "app", arm2, arm2Digest, layout.DigestFolder(arm2Digest))
		next, nextHash := platformIndex(
			entry(ma.bodies[0], "linux/amd64"),
			entry(arm2, "linux/arm64"),
		)
		w.pushList(w.docker, "app", next, nextHash, "v1")
		if got := w.tree("app/v1-linux-arm64")["manifest.json"]; got != arm2Digest {
			t.Fatalf("v1-linux-arm64 holds %s", got)
		}
		for _, f := range digestFolders(ma.bodies...) {
			if !slices.Contains(w.folders(w.docker, "app"), f) {
				t.Errorf("%s went", f)
			}
		}
	})
}

// The multi-architecture delete takes the tag's descriptive tags along, but
// not one pushed over since; RemoveTag with Platforms does the same.
func TestAccDeleteMultiArchTakesPlatformTags(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		ma := w.multiArch(w.docker, "app")
		w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
		w.pushList(w.docker, "app", ma.list, ma.hash, "v2")
		image, imageDigest := manifestOf(layerB)
		w.pushTo(w.docker, "app", image, imageDigest, "v1-linux-amd64")
		removed, err := w.svc.DeleteMultiArch(ctx, w.owner("app"), "v1")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(
			[]string{"v1", "v1-linux-arm64", "v1-unknown-unknown"}, removed,
		); diff != "" {
			t.Fatalf("removed:\n%s", diff)
		}
		if err := w.svc.RemoveTag(ctx, w.owner("app"), "v2",
			registry.Removal{Platforms: true}); err != nil {
			t.Fatal(err)
		}
		want := sorted(append([]string{"v1-linux-amd64"}, digestFolders(ma.bodies[1:]...)...))
		// The amd64 platform is gone: the tag pushed over it holds another
		// manifest.
		want = slices.DeleteFunc(want, func(f string) bool {
			return slices.Contains(digestFolders(ma.bodies[1:]...), f)
		})
		if diff := cmp.Diff(want, w.folders(w.docker, "app")); diff != "" {
			t.Fatalf("after both:\n%s", diff)
		}
	})
}

// Max Unique Tags counts a list with its descriptive tags as one tag, and
// removes them with it.
func TestAccMaxUniqueTagsCountsAListOnce(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		c := &clock{}
		w.svc.Now = c.now
		limited := w.repoOf(&repo.Config{
			Key: "docker-limited", Class: repo.Local, PackageType: repo.Docker,
			MaxUniqueTags: ptr(1),
		})
		ma := w.multiArch(limited, "app")
		w.at(c, 1)
		w.pushList(limited, "app", ma.list, ma.hash, "v1")
		tags := []string{"v1", "v1-linux-amd64", "v1-linux-arm64", "v1-unknown-unknown"}
		if diff := cmp.Diff(tags, sorted(w.tags(limited, "app"))); diff != "" {
			t.Fatalf("after the list:\n%s", diff)
		}
		image, imageDigest := manifestOf(layerB)
		w.at(c, 2)
		w.pushTo(limited, "app", image, imageDigest, "v2")
		if diff := cmp.Diff([]string{"v2"}, w.folders(limited, "app")); diff != "" {
			t.Fatalf("after v2:\n%s", diff)
		}
	})
}

// A promotion copies a list with its platform manifests but writes no
// descriptive tags at the target, as 7.111.4 stopped doing (RTDEV-55450);
// a move takes the source's descriptive tags out with the list.
func TestAccPromoteListWithPlatformTags(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		prod := w.repoOf(
			&repo.Config{Key: "docker-prod", Class: repo.Local, PackageType: repo.Docker},
		)
		ma := w.multiArch(w.docker, "app")
		w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
		from := w.owner("app")
		to := registry.Owner{Repo: prod, Name: "app", By: "alice"}
		err := w.svc.Promote(ctx, registry.Promotion{
			From: from, To: to, Tags: []registry.Rename{{From: "v1", To: "v1"}}, Copy: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := sorted(append([]string{"v1"}, digestFolders(ma.bodies...)...))
		if diff := cmp.Diff(want, w.folders(prod, "app")); diff != "" {
			t.Fatalf("copied:\n%s", diff)
		}
		// Into a repository without the platforms, so that they move too.
		staging := w.repoOf(&repo.Config{
			Key: "docker-staging", Class: repo.Local, PackageType: repo.Docker,
		})
		err = w.svc.Promote(ctx, registry.Promotion{
			From: from, To: registry.Owner{Repo: staging, Name: "app", By: "alice"},
			Tags: []registry.Rename{{From: "v1", To: "moved"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := w.folders(w.docker, "app"); len(got) != 0 {
			t.Fatalf("left in the source: %v", got)
		}
	})
}

// getFailingAt is a store whose item reads fail for any path containing at.
type getFailingAt struct {
	store.Store
	at string
}

func (g getFailingAt) Items() store.ItemStore { return getFailingItems{g.Store.Items(), g.at} }

type getFailingItems struct {
	store.ItemStore
	at string
}

func (g getFailingItems) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	fullPath string,
) (types.Item, error) {
	if strings.Contains(fullPath, g.at) {
		return types.Item{}, errBoom
	}
	return g.ItemStore.Get(ctx, repoKey, cached, fullPath) //nolint:wrapcheck // test double
}

// A store that cannot read a descriptive tag fails the push that writes it,
// the delete and the move that would take it along, and an overwrite by an
// image, and changes nothing.
func TestAccPlatformTagStoreFailures(t *testing.T) {
	db := inmem.New()
	w := newWorld(t, db)
	ctx := context.Background()
	ma := w.multiArch(w.docker, "app")
	w.pushList(w.docker, "app", ma.list, ma.hash, "v1")
	prod := w.repoOf(&repo.Config{Key: "docker-prod", Class: repo.Local, PackageType: repo.Docker})
	svc := w.service(getFailingAt{db, "-linux-amd64"})
	m, err := layout.ParseManifest(layout.MediaTypeOCIIndex, []byte(ma.list), ma.hash)
	if err != nil {
		t.Fatal(err)
	}
	image, digest := manifestOf(layerB)
	im, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(image), digest)
	if err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"push": func() error {
			return svc.PutManifest(ctx, registry.Push{
				Owner: w.owner("app"), Manifest: m, Body: []byte(ma.list), Folders: []string{"v2"},
			})
		},
		"multi-architecture delete": func() error {
			_, err := svc.DeleteMultiArch(ctx, w.owner("app"), "v1")
			return err
		},
		"move": func() error {
			return svc.Promote(ctx, registry.Promotion{
				From: w.owner("app"), To: registry.Owner{Repo: prod, Name: "app", By: "alice"},
				Tags: []registry.Rename{{From: "v1", To: "v1"}},
			})
		},
		"overwrite by an image": func() error {
			return svc.PutManifest(ctx, registry.Push{
				Owner: w.owner("app"), Manifest: im, Body: []byte(image), Folders: []string{"v1"},
			})
		},
	} {
		if err := run(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := w.folders(w.docker, "app"); !slices.Contains(got, "v1-linux-amd64") ||
		slices.Contains(got, "v2") {
		t.Fatalf("a failure changed the image: %v", got)
	}
}
