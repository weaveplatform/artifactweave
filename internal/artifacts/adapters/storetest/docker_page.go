package storetest

import (
	"cmp"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunDockerPages runs the image layout page reader's contract
// (port.DockerPageStore). Each adapter's tests call it next to Run; the SQL
// adapter's on PostgreSQL too, since each engine folds and orders in its own
// way.
func RunDockerPages(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("DockerPages", func(t *testing.T) {
		t.Run("ManifestFolders", func(t *testing.T) { testManifestFolders(t, newStore(t)) })
		t.Run("ImageManifests", func(t *testing.T) { testImageManifests(t, newStore(t)) })
	})
}

// dockerTagFolders are folder names that differ only in case, or in _, .
// and -, which fold below, between and above digits and letters: their
// order is the registry's (layout.Compare), whatever order they are stored
// in.
var dockerTagFolders = []string{
	"v1", "V1", "beta", "Beta", "BETA", "a_b", "A_B", "a.b", "a-b", "a0", "aZ", "a_",
	"_z", "Zz", "zZ", "1.0", "sha256:" + "ab", "z", "Z_", "z_", "z-",
}

func putDockerItems(t *testing.T, s port.Store, repoKey string, cached bool, files []string) {
	t.Helper()
	for n, full := range files {
		it := fileItem(repoKey, full, n)
		it.Cached = cached
		storekit.Get[model.Item](t)(s.Items().Put(t.Context(), it))
	}
}

func testManifestFolders(t *testing.T, s port.Store) {
	ctx := t.Context()
	files := make([]string, 0, len(dockerTagFolders))
	for _, f := range dockerTagFolders {
		files = append(files, "team/app/"+f+"/manifest.json")
	}
	putDockerItems(t, s, "docker-local", false, append(files,
		// A folder holding both manifests is listed once.
		"team/app/v1/list.manifest.json",
		// None of these is a manifest folder of team/app: a blob, a deeper
		// image's manifest, a manifest deeper in a folder, images whose names
		// fold alike or begin alike, another repository's and a cache's.
		"team/app/_uploads/sha256__aa", "team/app/blobs-only/sha256__bb",
		"team/app/sub/v1/manifest.json", "team/app/v2/deeper/manifest.json",
		"team/App/v9/manifest.json", "Team/app/v8/manifest.json",
		"team/app-x/v7/manifest.json", "team/app.x/v6/manifest.json", "team/app_x/v5/manifest.json",
	))
	putDockerItems(t, s, "other-local", false, []string{"team/app/other/manifest.json"})
	putDockerItems(t, s, "docker-local", true, []string{"team/app/cached/manifest.json"})
	// A folder named like a manifest is no manifest.
	storekit.Get[model.Item](t)(s.Items().Put(ctx,
		folderItem("docker-local", "team/app/folder/manifest.json")))

	want := slices.Clone(dockerTagFolders)
	slices.SortFunc(want, layout.Compare)
	pages := s.Items().DockerPages()
	list := func(q port.DockerFolderQuery) []string {
		t.Helper()
		q.RepoKey, q.Image = "docker-local", "team/app"
		return storekit.Get[[]string](t)(pages.ManifestFolders(ctx, q))
	}
	storekit.Same(t, "every folder in the registry's order", want,
		list(port.DockerFolderQuery{}))
	storekit.Same(t, "a limit of zero lists all", want,
		list(port.DockerFolderQuery{Limit: 0}))
	// Pages of three, each continued after the last folder of the one before,
	// list each folder once.
	var paged []string
	for q := (port.DockerFolderQuery{Limit: 3}); ; {
		page := list(q)
		paged = append(paged, page...)
		if len(page) < 3 {
			break
		}
		last := page[len(page)-1]
		q.AfterFold, q.After = layout.Fold(last), last
	}
	storekit.Same(t, "paged folders", want, paged)
	// A cursor need not be a folder, and may differ from one in case only.
	for _, after := range []string{"BETA", "Beta", "beta", "bb", "B", "_", "~", "", "a-b"} {
		var rest []string
		for _, f := range want {
			if layout.Compare(f, after) > 0 {
				rest = append(rest, f)
			}
		}
		got := list(port.DockerFolderQuery{AfterFold: layout.Fold(after), After: after})
		storekit.Same(t, "folders after "+after, rest, got)
	}
	storekit.Same(t, "folders of an image with none", []string(nil),
		storekit.Get[[]string](t)(pages.ManifestFolders(ctx, port.DockerFolderQuery{
			RepoKey: "docker-local", Image: "team/none",
		})))
	testManifestFoldersNonASCII(t, s)
}

// dockerNonASCIIFolders are folder names no tag can be, which Go's
// strings.ToLower folds but the store does not: the store folds A to Z
// alone (port.DockerFold), so such a folder keeps its place, and a listing
// continued after it with that fold lists each folder once.
var dockerNonASCIIFolders = []string{
	"É1", "é1", "E1", "e1", "K", "K", "k", "İ", "I", "i", "ß", "SS", "ä-É",
}

func testManifestFoldersNonASCII(t *testing.T, s port.Store) {
	ctx := t.Context()
	files := make([]string, 0, len(dockerNonASCIIFolders)+1)
	for _, f := range dockerNonASCIIFolders {
		files = append(files, "intl/äpp/"+f+"/manifest.json")
	}
	putDockerItems(t, s, "docker-local", false, append(files, "intl/ÄPP/x/manifest.json"))
	want := slices.Clone(dockerNonASCIIFolders)
	slices.SortFunc(want, func(a, b string) int {
		return cmp.Or(
			strings.Compare(port.DockerFold(a), port.DockerFold(b)),
			strings.Compare(a, b),
		)
	})
	pages := s.Items().DockerPages()
	var paged []string
	for q := (port.DockerFolderQuery{RepoKey: "docker-local", Image: "intl/äpp", Limit: 2}); ; {
		page := storekit.Get[[]string](t)(pages.ManifestFolders(ctx, q))
		paged = append(paged, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		q.AfterFold, q.After = port.DockerFold(last), last
	}
	storekit.Same(t, "non-ASCII folders in the store's fold", want, paged)
}

func testImageManifests(t *testing.T, s port.Store) {
	ctx := t.Context()
	putDockerItems(t, s, "docker-local", false, []string{
		"a/v2/manifest.json", "a/v1/manifest.json", "a/v1/list.manifest.json",
		"a/b/v1/manifest.json", "a-b/v1/manifest.json", "a.b/v1/manifest.json",
		"a_b/v1/manifest.json", "b/_uploads/manifest.json", "Upper/v1/manifest.json",
		// Neither is an image's manifest: one in a top-level folder, and a
		// blob.
		"top/manifest.json", "c/v1/sha256__cc",
	})
	putDockerItems(t, s, "other-local", false, []string{"o/v1/manifest.json"})
	putDockerItems(t, s, "docker-local", true, []string{"cached/v1/manifest.json"})
	all := []port.DockerImageManifest{
		{Image: "Upper", Folder: "v1"},
		{Image: "a", Folder: "v1"},
		{Image: "a", Folder: "v2"},
		{Image: "a-b", Folder: "v1"},
		{Image: "a.b", Folder: "v1"},
		{Image: "a/b", Folder: "v1"},
		{Image: "a_b", Folder: "v1"},
		{Image: "b", Folder: "_uploads"},
	}
	pages := s.Items().DockerPages()
	list := func(q port.DockerImageQuery) []port.DockerImageManifest {
		t.Helper()
		q.RepoKey = "docker-local"
		return storekit.Get[[]port.DockerImageManifest](t)(pages.ImageManifests(ctx, q))
	}
	storekit.Same(t, "every image manifest by bytes", all, list(port.DockerImageQuery{}))
	storekit.Same(t, "after an image", all[3:], list(port.DockerImageQuery{After: "a"}))
	storekit.Same(
		t,
		"from an image",
		all[1:],
		list(port.DockerImageQuery{After: "a", Within: true}),
	)
	storekit.Same(t, "inside an image", all[2:],
		list(port.DockerImageQuery{After: "a", Within: true, AfterFolder: "v1"}))
	storekit.Same(
		t,
		"after a name no image has",
		all[5:],
		list(port.DockerImageQuery{After: "a.c"}),
	)
	storekit.Same(t, "limited", all[1:3],
		list(port.DockerImageQuery{After: "Upper", Limit: 2}))
	storekit.Same(t, "past the end", []port.DockerImageManifest(nil),
		list(port.DockerImageQuery{After: "z"}))
}
