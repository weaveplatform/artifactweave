package storetest

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runContent covers repositories, items with their properties and
// statistics, the trash can and the package index.
func runContent(t *testing.T, newStore Factory) {
	t.Run("Repos", func(t *testing.T) { testRepos(t, newStore(t).Repos()) })
	t.Run("Items", func(t *testing.T) { testItems(t, newStore(t).Items()) })
	t.Run("ItemCreate", func(t *testing.T) { testItemCreate(t, newStore(t)) })
	t.Run("ColonPaths", func(t *testing.T) { testColonPaths(t, newStore(t).Items()) })
	t.Run("ItemProperties", func(t *testing.T) { testItemProperties(t, newStore(t).Items()) })
	t.Run("RepositoryRoot", func(t *testing.T) { testRepositoryRoot(t, newStore(t).Items()) })
	t.Run("ItemStats", func(t *testing.T) { testItemStats(t, newStore(t).Items()) })
	t.Run("ItemSearch", func(t *testing.T) { testItemSearch(t, newStore(t)) })
	t.Run("LongPropertyValues", func(t *testing.T) { testLongPropertyValues(t, newStore(t)) })
	t.Run("LongPropertyKeys", func(t *testing.T) { testLongPropertyKeys(t, newStore(t)) })
	t.Run("Trash", func(t *testing.T) { testTrash(t, newStore(t).Trash()) })
	t.Run("Packages", func(t *testing.T) { testPackages(t, newStore(t)) })
}

// repoOpts compares repository configurations. FlexBool keeps how it was
// spelt on the wire in an unexported field that a stored copy need not
// reproduce; its value is what matters.
var repoOpts = cmp.Options{storekit.Opts, cmpopts.IgnoreUnexported(repo.FlexBool{})}

func contentPtr[V any](v V) *V { return &v }

func sameRepo(t *testing.T, what string, want, got repo.Config) {
	t.Helper()
	if diff := cmp.Diff(want, got, repoOpts); diff != "" {
		t.Fatalf("%s (-want +got):\n%s", what, diff)
	}
}

func repoKeys(cs []repo.Config) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}

func testRepos(t *testing.T, r store.RepoStore) {
	ctx := context.Background()
	local := repo.Config{
		Key:         "libs-local",
		Class:       repo.Local,
		PackageType: repo.Maven,
		Description: "releases",
		PriorityResolution: repo.Flex(
			true,
		),
		HandleReleases:    contentPtr(true),
		PrimaryKeyPairRef: "kp1",
		PropertySets:      []string{"artifactory"},
	}
	remote := repo.Config{
		Key:         "maven-remote",
		Class:       repo.Remote,
		PackageType: repo.Maven,
		URL:         "https://repo1.example",
		Username:    "svc",
		Password:    "s3cret",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Secret", Value: "header-secret", Sensitive: true},
			{Name: "X-Visible", Value: "visible"},
			{Name: "X-Empty", Sensitive: true},
		},
		ProjectKey:          "proj",
		SecondaryKeyPairRef: "kp1",
		SocketTimeoutMillis: contentPtr(15000),
	}
	virtual := repo.Config{
		Key:         "maven-virtual",
		Class:       repo.Virtual,
		PackageType: repo.Maven,
		Repositories: []string{
			"libs-local",
			"maven-remote",
			"libs-local",
		},
		DefaultDeploymentRepo: "libs-local",
	}
	all := repo.Config{
		Key:          "all-virtual",
		Class:        repo.Virtual,
		PackageType:  repo.Generic,
		Repositories: []string{"maven-remote"},
	}
	for _, c := range []repo.Config{virtual, remote, all, local} {
		storekit.Must(t, r.Create(ctx, c))
	}
	storekit.WantErr(t, r.Create(ctx, local), store.ErrConflict)
	storekit.WantErr(t, r.Create(ctx, repo.Config{Class: repo.Local}), store.ErrInvalid)

	got := storekit.Get[repo.Config](t)(r.Get(ctx, "maven-remote"))
	sameRepo(t, "Get remote", remote, got)
	sameRepo(t, "Get local", local, storekit.Get[repo.Config](t)(r.Get(ctx, "libs-local")))
	_, err := r.Get(ctx, "nope")
	storekit.WantErr(t, err, store.ErrNotFound)

	// Returned values are copies.
	v := storekit.Get[repo.Config](t)(r.Get(ctx, "maven-virtual"))
	v.Repositories[0] = "changed"
	sameRepo(
		t,
		"Get after mutating a copy",
		virtual,
		storekit.Get[repo.Config](t)(r.Get(ctx, "maven-virtual")),
	)

	list := storekit.Get[[]repo.Config](t)(r.List(ctx, store.RepoFilter{}))
	storekit.Same(
		t,
		"List keys",
		[]string{"all-virtual", "libs-local", "maven-remote", "maven-virtual"},
		repoKeys(list),
	)
	sameRepo(t, "List carries the password", remote, list[2])
	for _, tc := range []struct {
		f    store.RepoFilter
		want []string
	}{
		{store.RepoFilter{Class: repo.Virtual}, []string{"all-virtual", "maven-virtual"}},
		{store.RepoFilter{PackageType: repo.Maven}, []string{"libs-local", "maven-remote", "maven-virtual"}},
		{store.RepoFilter{ProjectKey: "proj"}, []string{"maven-remote"}},
		{store.RepoFilter{Class: repo.Local, PackageType: repo.Generic}, nil},
	} {
		storekit.Same(
			t,
			fmt.Sprintf("List %+v", tc.f),
			tc.want,
			repoKeys(storekit.Get[[]repo.Config](t)(r.List(ctx, tc.f))),
		)
	}

	keys := storekit.Get[[]string](t)
	storekit.Same(
		t,
		"Including maven-remote",
		[]string{"all-virtual", "maven-virtual"},
		keys(r.Including(ctx, "maven-remote")),
	)
	storekit.Same(
		t,
		"Including libs-local",
		[]string{"maven-virtual"},
		keys(r.Including(ctx, "libs-local")),
	)
	storekit.Same(t, "Including nothing", []string(nil), keys(r.Including(ctx, "nope")))
	storekit.Same(
		t,
		"UsingKeypair",
		[]string{"libs-local", "maven-remote"},
		keys(r.UsingKeypair(ctx, "kp1")),
	)
	storekit.Same(t, "UsingKeypair empty name", []string(nil), keys(r.UsingKeypair(ctx, "")))

	// Updates replace the document, the sealed password and the mirrors.
	remote.Password, remote.URL, remote.SecondaryKeyPairRef = "", "https://repo2.example", ""
	storekit.Must(t, r.Update(ctx, remote))
	sameRepo(
		t,
		"Get after update",
		remote,
		storekit.Get[repo.Config](t)(r.Get(ctx, "maven-remote")),
	)
	remote.Password = "rotated"
	storekit.Must(t, r.Update(ctx, remote))
	sameRepo(
		t,
		"Get after password change",
		remote,
		storekit.Get[repo.Config](t)(r.Get(ctx, "maven-remote")),
	)
	storekit.Same(
		t,
		"UsingKeypair after update",
		[]string{"libs-local"},
		keys(r.UsingKeypair(ctx, "kp1")),
	)
	virtual.Repositories = []string{"libs-local"}
	storekit.Must(t, r.Update(ctx, virtual))
	storekit.Same(
		t,
		"Including after update",
		[]string{"all-virtual"},
		keys(r.Including(ctx, "maven-remote")),
	)
	storekit.WantErr(
		t,
		r.Update(ctx, repo.Config{Key: "nope", Class: repo.Local}),
		store.ErrNotFound,
	)

	storekit.Must(t, r.Delete(ctx, "all-virtual"))
	storekit.WantErr(t, r.Delete(ctx, "all-virtual"), store.ErrNotFound)
	_, err = r.Get(ctx, "all-virtual")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Same(
		t,
		"Including after delete",
		[]string(nil),
		keys(r.Including(ctx, "maven-remote")),
	)
}

// file builds a file item from fixture content i; full is the path.
func fileItem(repoKey, full string, i int) types.Item {
	b := blob(i)
	return types.Item{
		RepoKey:  repoKey,
		Name:     full,
		Size:     b.Size,
		Actual:   b.Checksums,
		Original: types.Checksums{SHA512: b.Checksums.SHA512, SHA1: b.Checksums.SHA1},
		MimeType: "application/java-archive",
		Created: storekit.At(
			i,
		),
		CreatedBy:  "alice",
		Modified:   storekit.At(i + 1),
		ModifiedBy: "bob",
		Updated:    storekit.At(i + 2),
		RemoteURL:  "https://origin.example/" + full,
	}
}

// folder builds a folder item.
func folderItem(repoKey, full string) types.Item {
	return types.Item{
		RepoKey:   repoKey,
		Name:      full,
		Folder:    true,
		Created:   storekit.At(0),
		CreatedBy: "alice",
	}
}

// stored is what Put returns for it with id: the canonical path split.
func stored(it types.Item, id int64) types.Item {
	parent, name, depth, err := types.SplitItemPath(it.FullPath())
	if err != nil {
		panic(err)
	}
	it.ID, it.Path, it.Name, it.Depth = id, parent, name, depth
	return it
}

func itemPaths(its []types.Item) []string {
	out := make([]string, 0, len(its))
	for _, it := range its {
		out = append(out, it.FullPath())
	}
	return out
}

func testItems(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	put := func(it types.Item) types.Item {
		t.Helper()
		got := storekit.Get[types.Item](t)(s.Put(ctx, it))
		if got.ID == 0 {
			t.Fatalf("Put(%s) assigned no ID", it.FullPath())
		}
		storekit.Same(t, "Put "+it.FullPath(), stored(it, got.ID), got)
		return got
	}
	a := put(folderItem("r", "a"))
	if a.Path != types.RootPath || a.Depth != 1 {
		t.Fatalf("root folder = %q depth %d", a.Path, a.Depth)
	}
	put(folderItem("r", "a/b"))
	cjar := put(fileItem("r", "a/b/c.jar", 1))
	if cjar.Path != "a/b" || cjar.Name != "c.jar" || cjar.Depth != 3 {
		t.Fatalf("Put split = %q %q %d", cjar.Path, cjar.Name, cjar.Depth)
	}
	put(fileItem("r", "a/b/d.jar", 2))
	put(fileItem("r", "a/x.txt", 3))
	put(fileItem("r", "z.txt", 4))
	put(folderItem("r", "ab"))
	put(fileItem("r", "ab/q.txt", 5))
	cachedJar := fileItem("r", "a/b/c.jar", 1)
	cachedJar.Cached = true
	cached := put(cachedJar)
	if cached.ID == cjar.ID {
		t.Fatal("the cache and the repository share an item")
	}
	put(fileItem("other", "a/b/c.jar", 1))

	// Put on an existing natural key replaces it in place.
	again := fileItem("r", "a/b/c.jar", 6)
	again.Path, again.Name = "a/b", "c.jar"
	if got := put(again); got.ID != cjar.ID {
		t.Fatalf("replacing put changed the ID from %d to %d", cjar.ID, got.ID)
	}
	for _, bad := range []types.Item{
		{RepoKey: "r", Name: "a/../b"},
		{Name: "a"},
		{RepoKey: "r"},
	} {
		_, err := s.Put(ctx, bad)
		storekit.WantErr(t, err, store.ErrInvalid)
	}

	got := storekit.Get[types.Item](t)(s.Get(ctx, "r", false, "/a/b/c.jar"))
	storekit.Same(t, "Get", stored(again, cjar.ID), got)
	storekit.Same(t, "GetByID", got, storekit.Get[types.Item](t)(s.GetByID(ctx, cjar.ID)))
	for _, p := range []string{"", "a/b/none", "a/../b"} {
		_, err := s.Get(ctx, "r", false, p)
		storekit.WantErr(t, err, store.ErrNotFound)
	}
	_, err := s.GetByID(ctx, 1<<40)
	storekit.WantErr(t, err, store.ErrNotFound)
	got.RemoteURL = "changed"
	if storekit.Get[types.Item](t)(s.GetByID(ctx, cjar.ID)).RemoteURL == "changed" {
		t.Fatal("Get returned shared memory")
	}

	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(storekit.Pages(t, 2, fn))
	}
	for _, root := range []string{"", ".", "/"} {
		storekit.Same(
			t,
			"Children of root",
			[]string{"a", "ab", "z.txt"},
			list(
				func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, root, p) },
			),
		)
	}
	storekit.Same(
		t,
		"Children of a/b",
		[]string{"a/b/c.jar", "a/b/d.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "a/b/", p) },
		),
	)
	storekit.Same(
		t,
		"Children of the cache",
		[]string{"a/b/c.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", true, "a/b", p) },
		),
	)
	storekit.Same(
		t,
		"Descendants of root",
		[]string{"a", "ab", "z.txt", "a/b", "a/x.txt", "a/b/c.jar", "a/b/d.jar", "ab/q.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "", p) },
		),
	)
	storekit.Same(
		t,
		"Descendants of a",
		[]string{"a/b", "a/x.txt", "a/b/c.jar", "a/b/d.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "a", p) },
		),
	)
	_, _, err = s.Children(ctx, "r", false, "", store.Page{After: store.Cursor("a", "b")})
	storekit.WantErr(t, err, store.ErrInvalidCursor)
	_, _, err = s.Descendants(ctx, "r", false, "", store.Page{After: "%%%"})
	storekit.WantErr(t, err, store.ErrInvalidCursor)

	// Checksum lookups return files only, ordered by ID.
	byChecksum := func(c types.Checksums) []string {
		t.Helper()
		return itemPaths(storekit.Pages(t, 1, func(p store.Page) ([]types.Item, string, error) {
			return s.FindByChecksum(ctx, c, p)
		}))
	}
	one := blob(1).Checksums
	storekit.Same(
		t,
		"FindByChecksum sha256",
		[]string{"a/b/c.jar", "a/b/c.jar"},
		byChecksum(types.Checksums{SHA256: one.SHA256}),
	)
	storekit.Same(
		t,
		"FindByChecksum sha1",
		[]string{"a/b/c.jar", "a/b/c.jar"},
		byChecksum(types.Checksums{SHA1: one.SHA1}),
	)
	storekit.Same(
		t,
		"FindByChecksum md5",
		[]string{"z.txt"},
		byChecksum(types.Checksums{MD5: blob(4).Checksums.MD5}),
	)
	storekit.Same(
		t,
		"FindByChecksum none",
		[]string(nil),
		byChecksum(types.Checksums{SHA256: blob(9).Checksums.SHA256}),
	)
	hits := storekit.Get[[]types.Item](t)(func() ([]types.Item, error) {
		its, _, err := s.FindByChecksum(ctx, types.Checksums{SHA256: one.SHA256}, store.Page{})
		return its, err
	}())
	if hits[0].ID != cached.ID || hits[1].RepoKey != "other" {
		t.Fatalf("FindByChecksum order = %+v", hits)
	}
	_, _, err = s.FindByChecksum(ctx, types.Checksums{}, store.Page{})
	storekit.WantErr(t, err, store.ErrInvalid)

	usage := storekit.Get[[]store.RepoUsage](t)(s.Usage(ctx))
	storekit.Same(t, "Usage", []store.RepoUsage{
		{RepoKey: "other", Files: 1, Bytes: 100},
		{RepoKey: "r", Files: 5, Folders: 3, Bytes: 600 + 200 + 300 + 400 + 500},
		{RepoKey: "r", Cached: true, Files: 1, Bytes: 100},
	}, usage)

	// Deleting a file, a folder subtree, a cache and an empty root.
	res := storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a/x.txt"))
	storekit.Same(t, "Delete file", store.DeleteResult{
		Files: 1, Bytes: 300,
		BlobRefs: map[string]int64{blob(3).Checksums.SHA256: 1},
	}, res)
	_, err = s.Get(ctx, "r", false, "a/x.txt")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = s.Delete(ctx, "r", false, "a/x.txt")
	storekit.WantErr(t, err, store.ErrNotFound)
	res = storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a"))
	storekit.Same(t, "Delete folder", store.DeleteResult{
		Files: 2, Folders: 2, Bytes: 800,
		BlobRefs: map[string]int64{blob(6).Checksums.SHA256: 1, blob(2).Checksums.SHA256: 1},
	}, res)
	storekit.Same(
		t,
		"Descendants after delete",
		[]string{"ab", "z.txt", "ab/q.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, ".", p) },
		),
	)
	res = storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", true, ""))
	storekit.Same(t, "Delete cache", store.DeleteResult{
		Files: 1, Bytes: 100,
		BlobRefs: map[string]int64{one.SHA256: 1},
	}, res)
	storekit.Same(t, "Delete empty root", store.DeleteResult{},
		storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", true, ".")))
	if got := storekit.Get[types.Item](t)(s.Get(ctx, "r", false, "z.txt")); got.Name != "z.txt" {
		t.Fatalf("unrelated item lost: %+v", got)
	}
}

// testColonPaths stores a Docker digest folder, whose name keeps the
// digest's colon as a 7.x local repository does, beside the sha256__ tag
// folder that differs from it in that character alone: reads, listings,
// searches and deletes treat the colon as an ordinary character.
func testColonPaths(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	for i, it := range []types.Item{
		folderItem("r", "img"),
		folderItem("r", "img/sha256:ab"),
		fileItem("r", "img/sha256:ab/manifest.json", 1),
		folderItem("r", "img/sha256__ab"),
		fileItem("r", "img/sha256__ab/manifest.json", 2),
	} {
		got := storekit.Get[types.Item](t)(s.Put(ctx, it))
		storekit.Same(t, fmt.Sprintf("Put %d", i), stored(it, got.ID), got)
	}
	got := storekit.Get[types.Item](t)(s.Get(ctx, "r", false, "/img/sha256:ab/manifest.json"))
	if got.Path != "img/sha256:ab" || got.Name != "manifest.json" || got.Depth != 3 {
		t.Fatalf("Get = %q %q %d", got.Path, got.Name, got.Depth)
	}
	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(storekit.Pages(t, 1, fn))
	}
	// Byte order puts ':' before '_'.
	storekit.Same(t, "Children of the image", []string{"img/sha256:ab", "img/sha256__ab"},
		list(func(p store.Page) ([]types.Item, string, error) {
			return s.Children(ctx, "r", false, "img", p)
		}))
	storekit.Same(t, "Descendants of the digest folder", []string{"img/sha256:ab/manifest.json"},
		list(func(p store.Page) ([]types.Item, string, error) {
			return s.Descendants(ctx, "r", false, "img/sha256:ab", p)
		}))
	search := func(query string) []string {
		t.Helper()
		q, err := aql.Parse(query)
		storekit.Must(t, err)
		hits := storekit.Get[[]aql.Hit](t)(s.Search(ctx, q, aql.Options{}))
		out := make([]string, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.Item.FullPath())
		}
		return out
	}
	storekit.Same(t, "$eq on the path", []string{"img/sha256:ab/manifest.json"},
		search(`items.find({"repo":"r","path":"img/sha256:ab"})`))
	storekit.Same(t, "$match on the path", []string{"img/sha256:ab/manifest.json"},
		search(`items.find({"repo":"r","path":{"$match":"img/sha256:*"}})`))
	storekit.Same(t, "$match across both folders",
		[]string{"img/sha256:ab/manifest.json", "img/sha256__ab/manifest.json"},
		search(`items.find({"repo":"r","path":{"$match":"img/sha256*"}}).sort({"$asc":["path"]})`))
	res := storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "img/sha256:ab"))
	storekit.Same(t, "Delete the digest folder", store.DeleteResult{
		Files: 1, Folders: 1, Bytes: blob(1).Size,
		BlobRefs: map[string]int64{blob(1).Checksums.SHA256: 1},
	}, res)
	if _, err := s.Get(ctx, "r", false, "img/sha256__ab/manifest.json"); err != nil {
		t.Fatalf("the tag folder went with the digest folder: %v", err)
	}
}

func testItemProperties(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	it := storekit.Get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.jar", 1)))
	props := storekit.Get[types.Properties](t)
	storekit.Same(t, "no properties", types.Properties{}, props(s.Properties(ctx, it.ID)))
	// stamped checks when the properties of the item with id last changed,
	// zero for never or for none left.
	stamped := func(what string, id int64, when time.Time) {
		t.Helper()
		storekit.Same(
			t,
			what,
			when,
			storekit.Get[types.Item](t)(s.GetByID(ctx, id)).PropertiesModified,
		)
	}
	stamped("a new item", it.ID, time.Time{})

	storekit.Must(
		t,
		s.SetProperties(
			ctx,
			it.ID,
			types.Properties{"b": {"2", "1", "1"}, "a": {"x"}, "empty": nil},
			storekit.At(10),
		),
	)
	storekit.Same(
		t,
		"SetProperties",
		types.Properties{"a": {"x"}, "b": {"1", "2"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("SetProperties", it.ID, storekit.At(10))
	// The same properties again, spelt differently, change nothing.
	storekit.Must(
		t,
		s.SetProperties(
			ctx,
			it.ID,
			types.Properties{"b": {"1", "2", "2"}, "a": {"x"}},
			storekit.At(11),
		),
	)
	stamped("SetProperties without a change", it.ID, storekit.At(10))

	storekit.Must(
		t,
		s.PatchProperties(
			ctx,
			it.ID,
			types.Properties{"c": {"3"}, "a": {"z", "y"}},
			[]string{"b", "absent"},
			storekit.At(12),
		),
	)
	storekit.Same(
		t,
		"PatchProperties",
		types.Properties{"a": {"y", "z"}, "c": {"3"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("PatchProperties", it.ID, storekit.At(12))
	storekit.Must(
		t,
		s.PatchProperties(
			ctx,
			it.ID,
			types.Properties{"c": {"3"}},
			[]string{"absent"},
			storekit.At(13),
		),
	)
	stamped("PatchProperties without a change", it.ID, storekit.At(12))
	// A key both removed and set ends with the set values.
	storekit.Must(
		t,
		s.PatchProperties(ctx, it.ID, types.Properties{"c": {"4"}}, []string{"c"}, storekit.At(14)),
	)
	storekit.Same(
		t,
		"remove then set",
		types.Properties{"a": {"y", "z"}, "c": {"4"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("remove then set", it.ID, storekit.At(14))

	got := props(s.Properties(ctx, it.ID))
	got["a"][0] = "changed"
	storekit.Same(
		t,
		"copies",
		types.Properties{"a": {"y", "z"}, "c": {"4"}},
		props(s.Properties(ctx, it.ID)),
	)

	// Only the property methods stamp: Put keeps the stored time over the
	// one it is given, and a new item starts without one.
	replace := fileItem("r", "a/b.jar", 3)
	replace.PropertiesModified = storekit.At(90)
	storekit.Same(
		t,
		"Put over an item",
		storekit.At(14),
		storekit.Get[types.Item](t)(s.Put(ctx, replace)).PropertiesModified,
	)
	fresh := fileItem("r", "a/c.jar", 2)
	fresh.PropertiesModified = storekit.At(91)
	other := storekit.Get[types.Item](t)(s.Put(ctx, fresh))
	stamped("Put of a new item", other.ID, time.Time{})

	storekit.Must(t, s.SetProperties(ctx, it.ID, types.Properties{"d": {""}}, storekit.At(15)))
	storekit.Same(
		t,
		"SetProperties replaces",
		types.Properties{"d": {""}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("SetProperties replaces", it.ID, storekit.At(15))

	storekit.Must(
		t,
		s.PatchProperties(
			ctx,
			other.ID,
			types.Properties{"k": {"v"}},
			[]string{"x"},
			storekit.At(16),
		),
	)
	storekit.Same(
		t,
		"PatchProperties on a bare item",
		types.Properties{"k": {"v"}},
		props(s.Properties(ctx, other.ID)),
	)
	// Every read of an item carries the time.
	when := func(items []types.Item) []time.Time {
		out := make([]time.Time, 0, len(items))
		for _, i := range items {
			out = append(out, i.PropertiesModified)
		}
		return out
	}
	storekit.Same(t, "Children", []time.Time{storekit.At(15), storekit.At(16)}, when(storekit.Pages(
		t,
		1,
		func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "a", p) },
	)))
	storekit.Same(
		t,
		"Descendants",
		[]time.Time{storekit.At(15), storekit.At(16)},
		when(storekit.Pages(
			t,
			1,
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "", p) },
		)),
	)
	storekit.Same(
		t,
		"Get",
		storekit.At(16),
		storekit.Get[types.Item](t)(s.Get(ctx, "r", false, "a/c.jar")).PropertiesModified,
	)

	// Removing the last property clears the time, by either method.
	storekit.Must(t, s.SetProperties(ctx, it.ID, nil, storekit.At(17)))
	stamped("SetProperties of nothing", it.ID, time.Time{})
	storekit.Must(t, s.PatchProperties(ctx, other.ID, nil, []string{"k"}, storekit.At(18)))
	stamped("PatchProperties removing the last key", other.ID, time.Time{})

	missing := it.ID + 1000
	_, err := s.Properties(ctx, missing)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(
		t,
		s.SetProperties(ctx, missing, types.Properties{"a": {"1"}}, storekit.At(19)),
		store.ErrNotFound,
	)
	storekit.WantErr(
		t,
		s.PatchProperties(ctx, missing, nil, []string{"a"}, storekit.At(19)),
		store.ErrNotFound,
	)

	// Properties, and their time, go with the item.
	storekit.Must(t, s.SetProperties(ctx, it.ID, types.Properties{"e": {"1"}}, storekit.At(20)))
	storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a/b.jar"))
	again := storekit.Get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.jar", 1)))
	storekit.Same(
		t,
		"properties after re-deploy",
		types.Properties{},
		props(s.Properties(ctx, again.ID)),
	)
	stamped("re-deployed", again.ID, time.Time{})
}

// testRepositoryRoot covers the record a repository root gets for its own
// properties: found by Root, ID and AQL, and left out of every listing,
// usage total and content delete, which describe what is inside the root.
func testRepositoryRoot(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		_, err := s.Root(ctx, "r", cached)
		storekit.WantErr(t, err, store.ErrNotFound)
	}
	storekit.Get[types.Item](t)(s.Put(ctx, folderItem("r", "a")))
	storekit.Get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.txt", 1)))
	storekit.Get[types.Item](t)(s.Put(ctx, fileItem("r", "z.txt", 2)))

	item := storekit.Get[types.Item](t)
	root := item(s.EnsureRoot(ctx, types.Item{
		RepoKey: "r",
		Path:    "ignored",
		Name:    "ignored",
		Folder:  false,
		Size:    9,
		Created: storekit.At(
			1,
		),
		CreatedBy:  "alice",
		Modified:   storekit.At(2),
		ModifiedBy: "bob",
		Updated:    storekit.At(3),
	}))
	want := types.RootItem("r", false)
	want.ID = root.ID
	want.Created, want.CreatedBy = storekit.At(1), "alice"
	want.Modified, want.ModifiedBy, want.Updated = storekit.At(2), "bob", storekit.At(3)
	storekit.Same(t, "EnsureRoot", want, root)
	if root.FullPath() != "" || root.Depth != 0 {
		t.Fatalf("root full path %q depth %d", root.FullPath(), root.Depth)
	}
	storekit.Same(t, "EnsureRoot again", want, item(s.EnsureRoot(ctx, types.Item{
		RepoKey: "r", Created: storekit.At(9), CreatedBy: "carol",
	})))
	storekit.Same(t, "Root", want, item(s.Root(ctx, "r", false)))
	storekit.Same(t, "GetByID", want, item(s.GetByID(ctx, root.ID)))
	cachedRoot := item(s.EnsureRoot(ctx, types.Item{RepoKey: "r", Cached: true}))
	if cachedRoot.ID == root.ID || !cachedRoot.Cached {
		t.Fatalf("the cache shares the repository's root: %+v", cachedRoot)
	}
	_, err := s.EnsureRoot(ctx, types.Item{})
	storekit.WantErr(t, err, store.ErrInvalid)
	for _, p := range []string{"", ".", "/"} {
		_, err := s.Get(ctx, "r", false, p)
		storekit.WantErr(t, err, store.ErrNotFound)
	}

	props := storekit.Get[types.Properties](t)
	storekit.Must(
		t,
		s.PatchProperties(
			ctx,
			root.ID,
			types.Properties{"k1": {"v2", "v1"}, "k2": {"x"}},
			nil,
			storekit.At(4),
		),
	)
	storekit.Same(t, "root properties", types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}},
		props(s.Properties(ctx, root.ID)))
	want.PropertiesModified = storekit.At(4)
	storekit.Same(t, "Root after its properties changed", want, item(s.Root(ctx, "r", false)))

	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(storekit.Pages(t, 1, fn))
	}
	storekit.Same(
		t,
		"Children of root",
		[]string{"a", "z.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "", p) },
		),
	)
	storekit.Same(
		t,
		"Descendants of root",
		[]string{"a", "z.txt", "a/b.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, ".", p) },
		),
	)
	storekit.Same(
		t,
		"Children of the empty cache",
		[]string(nil),
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", true, "", p) },
		),
	)
	storekit.Same(t, "Usage", []store.RepoUsage{{RepoKey: "r", Files: 2, Folders: 1, Bytes: 300}},
		storekit.Get[[]store.RepoUsage](t)(s.Usage(ctx)))

	// AQL reports the root as Artifactory does: a folder whose path and name
	// are both ".", found by its properties once folders are asked for.
	find := func(query string) []aql.Hit {
		t.Helper()
		q, err := aql.Parse(query)
		storekit.Must(t, err)
		return storekit.Get[[]aql.Hit](t)(s.Search(ctx, q, aql.Options{}))
	}
	hits := find(`items.find({"repo":"r","type":"folder","@k1":"v1"}).include("*","property")`)
	if len(hits) != 1 {
		t.Fatalf("root by property: %+v", hits)
	}
	storekit.Same(t, "root hit", want, hits[0].Item)
	storekit.Same(t, "root hit properties", types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}},
		hits[0].Properties)
	if hits := find(`items.find({"repo":"r","@k1":"v1"})`); len(hits) != 0 {
		t.Fatalf("a files query found the root: %+v", hits)
	}
	var names []string
	for _, h := range find(`items.find({"repo":"r","path":".","type":"any"})`) {
		names = append(names, h.Item.Name)
	}
	slices.Sort(names)
	storekit.Same(t, "items at the root", []string{".", "a", "z.txt"}, names)

	// Deleting the repository's content keeps the root and its properties,
	// and does not count it.
	res := storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", false, ""))
	if res.Files != 2 || res.Folders != 1 {
		t.Fatalf("Delete root = %+v", res)
	}
	storekit.Same(t, "root after a content delete", want, item(s.Root(ctx, "r", false)))
	storekit.Same(t, "root properties after a content delete",
		types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}}, props(s.Properties(ctx, root.ID)))
	storekit.Same(t, "Delete of an empty root", store.DeleteResult{},
		storekit.Get[store.DeleteResult](t)(s.Delete(ctx, "r", true, "")))

	storekit.Must(t, s.DeleteRoot(ctx, "r", false))
	_, err = s.Root(ctx, "r", false)
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = s.Properties(ctx, root.ID)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Must(t, s.DeleteRoot(ctx, "r", false))
	storekit.Same(t, "the cache's root stays", cachedRoot, item(s.Root(ctx, "r", true)))

	// A new root after removal starts without properties.
	again := item(s.EnsureRoot(ctx, types.Item{RepoKey: "r"}))
	storekit.Same(
		t,
		"properties of a new root",
		types.Properties{},
		props(s.Properties(ctx, again.ID)),
	)
}

func testItemStats(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	it := storekit.Get[types.Item](t)(s.Put(ctx, fileItem("r", "a.jar", 1)))
	stats := storekit.Get[types.ItemStats](t)
	storekit.Same(t, "no statistics", types.ItemStats{}, stats(s.Stats(ctx, it.ID)))

	storekit.Must(t, s.RecordDownload(ctx, it.ID, store.Download{By: "alice", At: storekit.At(1)}))
	storekit.Must(t, s.RecordDownload(ctx, it.ID, store.Download{By: "bob", At: storekit.At(2)}))
	storekit.Must(t, s.RecordDownload(ctx, it.ID, store.Download{
		By:     "edge",
		At:     storekit.At(3),
		Remote: true,
		Origin: "https://edge.example",
		Path:   "r/a.jar",
	}))
	storekit.Same(t, "after downloads", types.ItemStats{
		DownloadCount:          2,
		LastDownloaded:         storekit.At(2),
		LastDownloadedBy:       "bob",
		RemoteDownloadCount:    1,
		RemoteLastDownloaded:   storekit.At(3),
		RemoteLastDownloadedBy: "edge",
		RemoteOrigin:           "https://edge.example",
		RemotePath:             "r/a.jar",
	}, stats(s.Stats(ctx, it.ID)))

	imported := types.ItemStats{
		DownloadCount:    42,
		LastDownloaded:   storekit.At(9),
		LastDownloadedBy: "carol",
	}
	storekit.Must(t, s.PutStats(ctx, it.ID, imported))
	storekit.Same(t, "PutStats", imported, stats(s.Stats(ctx, it.ID)))

	missing := it.ID + 1000
	_, err := s.Stats(ctx, missing)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(
		t,
		s.RecordDownload(ctx, missing, store.Download{At: storekit.At(1)}),
		store.ErrNotFound,
	)
	storekit.WantErr(t, s.PutStats(ctx, missing, imported), store.ErrNotFound)
}

func trashEntry(repoKey, full string, i int, deleted int) types.TrashEntry {
	e := types.TrashEntry{
		Record: types.TrashRecord{
			Item:       fileItem(repoKey, full, i),
			Properties: types.Properties{"build.number": {fmt.Sprint(i)}},
		},
		DeletedBy: "alice",
	}
	if deleted != 0 {
		e.DeletedAt = storekit.At(deleted)
	}
	return e
}

func trashPaths(es []types.TrashEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(
			out,
			fmt.Sprintf("%s:%s@%d", e.Record.Item.RepoKey, e.Record.Item.FullPath(), e.ID),
		)
	}
	return out
}

func testTrash(t *testing.T, s store.TrashStore) {
	ctx := context.Background()
	add := func(e types.TrashEntry) types.TrashEntry {
		t.Helper()
		got := storekit.Get[types.TrashEntry](t)(s.Add(ctx, e))
		want := e
		want.ID = got.ID
		want.Record.Item = stored(e.Record.Item, 0)
		storekit.Same(t, "Add", want, got)
		return got
	}
	c1 := add(trashEntry("r1", "a/b/c.jar", 1, 3))
	c2 := add(trashEntry("r1", "a/b/c.jar", 2, 5))
	d := add(trashEntry("r1", "a/b/d.jar", 3, 1))
	z := add(trashEntry("r1", "z.txt", 4, 2))
	q := add(trashEntry("r1", "ab/q.txt", 5, 0))
	o := add(trashEntry("r2", "a/b/c.jar", 6, 4))
	if c1.ID == c2.ID || c1.ID == 0 {
		t.Fatalf("IDs %d and %d", c1.ID, c2.ID)
	}
	_, err := s.Add(ctx, trashEntry("r1", "a/../b", 1, 1))
	storekit.WantErr(t, err, store.ErrInvalid)

	got := storekit.Get[types.TrashEntry](t)(s.Get(ctx, c1.ID))
	storekit.Same(t, "Get", c1, got)
	got.Record.Properties["build.number"][0] = "changed"
	storekit.Same(t, "copies", c1, storekit.Get[types.TrashEntry](t)(s.Get(ctx, c1.ID)))
	_, err = s.Get(ctx, 1<<40)
	storekit.WantErr(t, err, store.ErrNotFound)

	entries := storekit.Get[[]types.TrashEntry](t)
	storekit.Same(
		t,
		"Find newest first",
		trashPaths([]types.TrashEntry{c2, c1}),
		trashPaths(entries(s.Find(ctx, "r1", "/a/b/c.jar"))),
	)
	storekit.Same(t, "Find nothing", []string{}, trashPaths(entries(s.Find(ctx, "r1", "a/../c"))))
	q2 := add(trashEntry("r1", "ab/q.txt", 7, 6))
	storekit.Same(t, "Find puts an undated entry last", trashPaths([]types.TrashEntry{q2, q}),
		trashPaths(entries(s.Find(ctx, "r1", "ab/q.txt"))))

	list := func(repoKey, prefix string) []string {
		t.Helper()
		return trashPaths(
			storekit.Pages(t, 2, func(p store.Page) ([]types.TrashEntry, string, error) {
				return s.List(ctx, repoKey, prefix, p)
			}),
		)
	}
	storekit.Same(
		t,
		"List all",
		trashPaths([]types.TrashEntry{z, c1, c2, d, q, q2, o}),
		list("", ""),
	)
	storekit.Same(
		t,
		"List r1 under a",
		trashPaths([]types.TrashEntry{c1, c2, d}),
		list("r1", "/a/"),
	)
	storekit.Same(
		t,
		"List an exact path",
		trashPaths([]types.TrashEntry{c1, c2}),
		list("r1", "a/b/c.jar"),
	)
	storekit.Same(t, "List a root file", trashPaths([]types.TrashEntry{z}), list("r1", "z.txt"))
	storekit.Same(t, "List an invalid prefix", []string{}, list("r1", "a/.."))
	_, _, err = s.List(ctx, "", "", store.Page{After: store.Cursor("r1")})
	storekit.WantErr(t, err, store.ErrInvalidCursor)
	_, _, err = s.List(ctx, "", "", store.Page{After: "%%%"})
	storekit.WantErr(t, err, store.ErrInvalidCursor)

	storekit.Same(
		t,
		"Expired",
		trashPaths([]types.TrashEntry{d, z, c1}),
		trashPaths(entries(s.Expired(ctx, storekit.At(4), 0))),
	)
	storekit.Same(
		t,
		"Expired limited",
		trashPaths([]types.TrashEntry{d}),
		trashPaths(entries(s.Expired(ctx, storekit.At(4), 1))),
	)

	storekit.Must(t, s.Delete(ctx, c1.ID))
	storekit.WantErr(t, s.Delete(ctx, c1.ID), store.ErrNotFound)
	storekit.Same(
		t,
		"Find after delete",
		trashPaths([]types.TrashEntry{c2}),
		trashPaths(entries(s.Find(ctx, "r1", "a/b/c.jar"))),
	)
}

func versionNames(vs []types.PackageVersion) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s#%d", v.Version, v.OrderKey))
	}
	return out
}

func testPackages(t *testing.T, st store.Store) {
	ctx := context.Background()
	s := st.Packages()
	pkg := storekit.Get[types.Package](t)(s.Ensure(ctx, types.Package{
		RepoKey: "npm-local", Type: repo.NPM, Name: "left-pad", Created: storekit.At(1),
	}))
	if pkg.ID == 0 {
		t.Fatal("Ensure assigned no ID")
	}
	storekit.Same(
		t,
		"Ensure",
		types.Package{
			ID:      pkg.ID,
			RepoKey: "npm-local",
			Type:    repo.NPM,
			Name:    "left-pad",
			Created: storekit.At(1),
		},
		pkg,
	)
	storekit.Same(
		t,
		"Ensure existing",
		pkg,
		storekit.Get[types.Package](t)(s.Ensure(ctx, types.Package{
			RepoKey: "npm-local", Type: repo.NPM, Name: "left-pad", Created: storekit.At(9),
		})),
	)
	_, err := s.Ensure(ctx, types.Package{RepoKey: "npm-local"})
	storekit.WantErr(t, err, store.ErrInvalid)
	storekit.Same(t, "Get", pkg, storekit.Get[types.Package](t)(s.Get(ctx, pkg.ID)))
	storekit.Same(
		t,
		"Find",
		pkg,
		storekit.Get[types.Package](t)(s.Find(ctx, "npm-local", "left-pad")),
	)
	_, err = s.Get(ctx, pkg.ID+1000)
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = s.Find(ctx, "npm-local", "nope")
	storekit.WantErr(t, err, store.ErrNotFound)

	for _, name := range []string{"b-pkg", "a-pkg", "@scope/z"} {
		storekit.Get[types.Package](
			t,
		)(
			s.Ensure(ctx, types.Package{RepoKey: "npm-local", Type: repo.NPM, Name: name}),
		)
	}
	storekit.Get[types.Package](
		t,
	)(
		s.Ensure(ctx, types.Package{RepoKey: "npm-remote", Type: repo.NPM, Name: "left-pad"}),
	)
	var names []string
	for _, p := range storekit.Pages(t, 2, func(p store.Page) ([]types.Package, string, error) {
		return s.List(ctx, "npm-local", p)
	}) {
		names = append(names, p.Name)
	}
	storekit.Same(t, "List", []string{"@scope/z", "a-pkg", "b-pkg", "left-pad"}, names)
	for _, bad := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err = s.List(ctx, "npm-local", store.Page{After: bad})
		storekit.WantErr(t, err, store.ErrInvalidCursor)
	}

	put := func(version string, created int, meta string) types.PackageVersion {
		t.Helper()
		v := types.PackageVersion{
			PackageID: pkg.ID,
			Version:   version,
			Created:   storekit.At(created),
		}
		if meta != "" {
			v.Metadata = []byte(meta)
		}
		return storekit.Get[types.PackageVersion](t)(s.PutVersion(ctx, v))
	}
	put("1.10.0", 1, `{"dist":"a"}`)
	put("1.2.0", 2, "")
	put("1.9.0-beta", 3, "")
	top := put("2.0.0", 4, "")
	if top.OrderKey != 4 || top.ID == 0 {
		t.Fatalf("PutVersion = %+v", top)
	}
	versions := storekit.Get[[]types.PackageVersion](t)
	storekit.Same(
		t,
		"Versions in semver order",
		[]string{"1.2.0#1", "1.9.0-beta#2", "1.10.0#3", "2.0.0#4"},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)
	put("1.0.0", 5, "")
	storekit.Same(
		t,
		"Versions after an older one",
		[]string{"1.0.0#1", "1.2.0#2", "1.9.0-beta#3", "1.10.0#4", "2.0.0#5"},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)

	v110 := storekit.Get[types.PackageVersion](t)(s.Version(ctx, pkg.ID, "1.10.0"))
	storekit.Same(t, "Version", types.PackageVersion{
		ID:        v110.ID,
		PackageID: pkg.ID,
		Version:   "1.10.0",
		OrderKey:  4,
		Created:   storekit.At(1),
		Metadata:  []byte(`{"dist":"a"}`),
	}, v110)
	replaced := put("1.10.0", 7, `{"dist":"b"}`)
	if replaced.ID != v110.ID || string(replaced.Metadata) != `{"dist":"b"}` ||
		replaced.OrderKey != 4 {
		t.Fatalf("replacing PutVersion = %+v", replaced)
	}
	replaced.Metadata[0] = 'X'
	if string(
		storekit.Get[types.PackageVersion](t)(s.Version(ctx, pkg.ID, "1.10.0")).Metadata,
	) != `{"dist":"b"}` {
		t.Fatal("Version returned shared memory")
	}
	_, err = s.Version(ctx, pkg.ID, "9.9.9")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = s.PutVersion(ctx, types.PackageVersion{PackageID: pkg.ID + 1000, Version: "1"})
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = s.PutVersion(ctx, types.PackageVersion{PackageID: pkg.ID})
	storekit.WantErr(t, err, store.ErrInvalid)

	// File links, cascading from both sides.
	itemsStore := st.Items()
	jar := storekit.Get[types.Item](
		t,
	)(
		itemsStore.Put(ctx, fileItem("npm-local", "left-pad/-/left-pad-1.10.0.tgz", 1)),
	)
	pom := storekit.Get[types.Item](
		t,
	)(
		itemsStore.Put(ctx, fileItem("npm-local", "left-pad/package.json", 2)),
	)
	for _, f := range []types.VersionFile{{VersionID: v110.ID, ItemID: pom.ID}, {VersionID: v110.ID, ItemID: jar.ID}, {VersionID: v110.ID, ItemID: jar.ID}} {
		storekit.Must(t, s.LinkFile(ctx, f))
	}
	ids := storekit.Get[[]int64](t)
	storekit.Same(
		t,
		"Files",
		[]int64{min(jar.ID, pom.ID), max(jar.ID, pom.ID)},
		ids(s.Files(ctx, v110.ID)),
	)
	storekit.Same(
		t,
		"VersionOf",
		v110.ID,
		storekit.Get[types.PackageVersion](t)(s.VersionOf(ctx, jar.ID)).ID,
	)
	storekit.WantErr(
		t,
		s.LinkFile(ctx, types.VersionFile{VersionID: v110.ID + 1000, ItemID: jar.ID}),
		store.ErrNotFound,
	)
	storekit.WantErr(
		t,
		s.LinkFile(ctx, types.VersionFile{VersionID: v110.ID, ItemID: jar.ID + 1000}),
		store.ErrNotFound,
	)
	_, err = s.VersionOf(ctx, jar.ID+1000)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Get[store.DeleteResult](t)(itemsStore.Delete(ctx, "npm-local", false, pom.FullPath()))
	storekit.Same(t, "Files after the item went", []int64{jar.ID}, ids(s.Files(ctx, v110.ID)))

	storekit.Must(t, s.RecordVersionDownload(ctx, v110.ID, storekit.At(9)))
	if got := storekit.Get[types.PackageVersion](
		t,
	)(
		s.Version(ctx, pkg.ID, "1.10.0"),
	); !got.LastDownloaded.Equal(
		storekit.Want(storekit.At(9)),
	) {
		t.Fatalf("LastDownloaded = %v", got.LastDownloaded)
	}
	storekit.WantErr(
		t,
		s.RecordVersionDownload(ctx, v110.ID+1000, storekit.At(9)),
		store.ErrNotFound,
	)

	storekit.Must(t, s.DeleteVersion(ctx, v110.ID))
	storekit.WantErr(t, s.DeleteVersion(ctx, v110.ID), store.ErrNotFound)
	storekit.Same(
		t,
		"Versions after delete",
		[]string{"1.0.0#1", "1.2.0#2", "1.9.0-beta#3", "2.0.0#4"},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)
	storekit.Same(t, "Files of a deleted version", []int64(nil), ids(s.Files(ctx, v110.ID)))
	_, err = s.VersionOf(ctx, jar.ID)
	storekit.WantErr(t, err, store.ErrNotFound)

	storekit.Must(t, s.Delete(ctx, pkg.ID))
	storekit.WantErr(t, s.Delete(ctx, pkg.ID), store.ErrNotFound)
	_, err = s.Version(ctx, pkg.ID, "2.0.0")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Same(
		t,
		"Versions of a deleted package",
		[]string{},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)
}
