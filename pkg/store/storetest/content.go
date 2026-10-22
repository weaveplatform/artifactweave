package storetest

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runContent covers repositories, items with their properties and
// statistics, upload sessions, the trash can and the package index.
func runContent(t *testing.T, newStore Factory) {
	t.Run("Repos", func(t *testing.T) { testRepos(t, newStore(t).Repos()) })
	t.Run("Items", func(t *testing.T) { testItems(t, newStore(t).Items()) })
	t.Run("ColonPaths", func(t *testing.T) { testColonPaths(t, newStore(t).Items()) })
	t.Run("ItemProperties", func(t *testing.T) { testItemProperties(t, newStore(t).Items()) })
	t.Run("RepositoryRoot", func(t *testing.T) { testRepositoryRoot(t, newStore(t).Items()) })
	t.Run("ItemStats", func(t *testing.T) { testItemStats(t, newStore(t).Items()) })
	t.Run("ItemSearch", func(t *testing.T) { testItemSearch(t, newStore(t)) })
	t.Run("LongPropertyValues", func(t *testing.T) { testLongPropertyValues(t, newStore(t)) })
	t.Run("LongPropertyKeys", func(t *testing.T) { testLongPropertyKeys(t, newStore(t)) })
	t.Run("Uploads", func(t *testing.T) { testUploads(t, newStore(t).Uploads()) })
	t.Run("Trash", func(t *testing.T) { testTrash(t, newStore(t).Trash()) })
	t.Run("Packages", func(t *testing.T) { testPackages(t, newStore(t)) })
}

// repoOpts compares repository configurations. FlexBool keeps how it was
// spelt on the wire in an unexported field that a stored copy need not
// reproduce; its value is what matters.
var repoOpts = cmp.Options{opts, cmpopts.IgnoreUnexported(repo.FlexBool{})}

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
		must(t, r.Create(ctx, c))
	}
	wantErr(t, r.Create(ctx, local), store.ErrConflict)
	wantErr(t, r.Create(ctx, repo.Config{Class: repo.Local}), store.ErrInvalid)

	got := get[repo.Config](t)(r.Get(ctx, "maven-remote"))
	sameRepo(t, "Get remote", remote, got)
	sameRepo(t, "Get local", local, get[repo.Config](t)(r.Get(ctx, "libs-local")))
	_, err := r.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// Returned values are copies.
	v := get[repo.Config](t)(r.Get(ctx, "maven-virtual"))
	v.Repositories[0] = "changed"
	sameRepo(
		t,
		"Get after mutating a copy",
		virtual,
		get[repo.Config](t)(r.Get(ctx, "maven-virtual")),
	)

	list := get[[]repo.Config](t)(r.List(ctx, store.RepoFilter{}))
	same(
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
		same(
			t,
			fmt.Sprintf("List %+v", tc.f),
			tc.want,
			repoKeys(get[[]repo.Config](t)(r.List(ctx, tc.f))),
		)
	}

	keys := get[[]string](t)
	same(
		t,
		"Including maven-remote",
		[]string{"all-virtual", "maven-virtual"},
		keys(r.Including(ctx, "maven-remote")),
	)
	same(t, "Including libs-local", []string{"maven-virtual"}, keys(r.Including(ctx, "libs-local")))
	same(t, "Including nothing", []string(nil), keys(r.Including(ctx, "nope")))
	same(
		t,
		"UsingKeypair",
		[]string{"libs-local", "maven-remote"},
		keys(r.UsingKeypair(ctx, "kp1")),
	)
	same(t, "UsingKeypair empty name", []string(nil), keys(r.UsingKeypair(ctx, "")))

	// Updates replace the document, the sealed password and the mirrors.
	remote.Password, remote.URL, remote.SecondaryKeyPairRef = "", "https://repo2.example", ""
	must(t, r.Update(ctx, remote))
	sameRepo(t, "Get after update", remote, get[repo.Config](t)(r.Get(ctx, "maven-remote")))
	remote.Password = "rotated"
	must(t, r.Update(ctx, remote))
	sameRepo(
		t,
		"Get after password change",
		remote,
		get[repo.Config](t)(r.Get(ctx, "maven-remote")),
	)
	same(t, "UsingKeypair after update", []string{"libs-local"}, keys(r.UsingKeypair(ctx, "kp1")))
	virtual.Repositories = []string{"libs-local"}
	must(t, r.Update(ctx, virtual))
	same(
		t,
		"Including after update",
		[]string{"all-virtual"},
		keys(r.Including(ctx, "maven-remote")),
	)
	wantErr(t, r.Update(ctx, repo.Config{Key: "nope", Class: repo.Local}), store.ErrNotFound)

	must(t, r.Delete(ctx, "all-virtual"))
	wantErr(t, r.Delete(ctx, "all-virtual"), store.ErrNotFound)
	_, err = r.Get(ctx, "all-virtual")
	wantErr(t, err, store.ErrNotFound)
	same(t, "Including after delete", []string(nil), keys(r.Including(ctx, "maven-remote")))
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
		Created: at(
			i,
		),
		CreatedBy:  "alice",
		Modified:   at(i + 1),
		ModifiedBy: "bob",
		Updated:    at(i + 2),
		RemoteURL:  "https://origin.example/" + full,
	}
}

// folder builds a folder item.
func folderItem(repoKey, full string) types.Item {
	return types.Item{
		RepoKey:   repoKey,
		Name:      full,
		Folder:    true,
		Created:   at(0),
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
		got := get[types.Item](t)(s.Put(ctx, it))
		if got.ID == 0 {
			t.Fatalf("Put(%s) assigned no ID", it.FullPath())
		}
		same(t, "Put "+it.FullPath(), stored(it, got.ID), got)
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
		wantErr(t, err, store.ErrInvalid)
	}

	got := get[types.Item](t)(s.Get(ctx, "r", false, "/a/b/c.jar"))
	same(t, "Get", stored(again, cjar.ID), got)
	same(t, "GetByID", got, get[types.Item](t)(s.GetByID(ctx, cjar.ID)))
	for _, p := range []string{"", "a/b/none", "a/../b"} {
		_, err := s.Get(ctx, "r", false, p)
		wantErr(t, err, store.ErrNotFound)
	}
	_, err := s.GetByID(ctx, 1<<40)
	wantErr(t, err, store.ErrNotFound)
	got.RemoteURL = "changed"
	if get[types.Item](t)(s.GetByID(ctx, cjar.ID)).RemoteURL == "changed" {
		t.Fatal("Get returned shared memory")
	}

	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(pages(t, 2, fn))
	}
	for _, root := range []string{"", ".", "/"} {
		same(
			t,
			"Children of root",
			[]string{"a", "ab", "z.txt"},
			list(
				func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, root, p) },
			),
		)
	}
	same(
		t,
		"Children of a/b",
		[]string{"a/b/c.jar", "a/b/d.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "a/b/", p) },
		),
	)
	same(
		t,
		"Children of the cache",
		[]string{"a/b/c.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", true, "a/b", p) },
		),
	)
	same(
		t,
		"Descendants of root",
		[]string{"a", "ab", "z.txt", "a/b", "a/x.txt", "a/b/c.jar", "a/b/d.jar", "ab/q.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "", p) },
		),
	)
	same(
		t,
		"Descendants of a",
		[]string{"a/b", "a/x.txt", "a/b/c.jar", "a/b/d.jar"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "a", p) },
		),
	)
	_, _, err = s.Children(ctx, "r", false, "", store.Page{After: store.Cursor("a", "b")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = s.Descendants(ctx, "r", false, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	// Checksum lookups return files only, ordered by ID.
	byChecksum := func(c types.Checksums) []string {
		t.Helper()
		return itemPaths(pages(t, 1, func(p store.Page) ([]types.Item, string, error) {
			return s.FindByChecksum(ctx, c, p)
		}))
	}
	one := blob(1).Checksums
	same(
		t,
		"FindByChecksum sha256",
		[]string{"a/b/c.jar", "a/b/c.jar"},
		byChecksum(types.Checksums{SHA256: one.SHA256}),
	)
	same(
		t,
		"FindByChecksum sha1",
		[]string{"a/b/c.jar", "a/b/c.jar"},
		byChecksum(types.Checksums{SHA1: one.SHA1}),
	)
	same(
		t,
		"FindByChecksum md5",
		[]string{"z.txt"},
		byChecksum(types.Checksums{MD5: blob(4).Checksums.MD5}),
	)
	same(
		t,
		"FindByChecksum none",
		[]string(nil),
		byChecksum(types.Checksums{SHA256: blob(9).Checksums.SHA256}),
	)
	hits := get[[]types.Item](t)(func() ([]types.Item, error) {
		its, _, err := s.FindByChecksum(ctx, types.Checksums{SHA256: one.SHA256}, store.Page{})
		return its, err
	}())
	if hits[0].ID != cached.ID || hits[1].RepoKey != "other" {
		t.Fatalf("FindByChecksum order = %+v", hits)
	}
	_, _, err = s.FindByChecksum(ctx, types.Checksums{}, store.Page{})
	wantErr(t, err, store.ErrInvalid)

	usage := get[[]store.RepoUsage](t)(s.Usage(ctx))
	same(t, "Usage", []store.RepoUsage{
		{RepoKey: "other", Files: 1, Bytes: 100},
		{RepoKey: "r", Files: 5, Folders: 3, Bytes: 600 + 200 + 300 + 400 + 500},
		{RepoKey: "r", Cached: true, Files: 1, Bytes: 100},
	}, usage)

	// Deleting a file, a folder subtree, a cache and an empty root.
	res := get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a/x.txt"))
	same(t, "Delete file", store.DeleteResult{
		Files: 1, Bytes: 300,
		BlobRefs: map[string]int64{blob(3).Checksums.SHA256: 1},
	}, res)
	_, err = s.Get(ctx, "r", false, "a/x.txt")
	wantErr(t, err, store.ErrNotFound)
	_, err = s.Delete(ctx, "r", false, "a/x.txt")
	wantErr(t, err, store.ErrNotFound)
	res = get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a"))
	same(t, "Delete folder", store.DeleteResult{
		Files: 2, Folders: 2, Bytes: 800,
		BlobRefs: map[string]int64{blob(6).Checksums.SHA256: 1, blob(2).Checksums.SHA256: 1},
	}, res)
	same(
		t,
		"Descendants after delete",
		[]string{"ab", "z.txt", "ab/q.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, ".", p) },
		),
	)
	res = get[store.DeleteResult](t)(s.Delete(ctx, "r", true, ""))
	same(t, "Delete cache", store.DeleteResult{
		Files: 1, Bytes: 100,
		BlobRefs: map[string]int64{one.SHA256: 1},
	}, res)
	same(t, "Delete empty root", store.DeleteResult{},
		get[store.DeleteResult](t)(s.Delete(ctx, "r", true, ".")))
	if got := get[types.Item](t)(s.Get(ctx, "r", false, "z.txt")); got.Name != "z.txt" {
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
		got := get[types.Item](t)(s.Put(ctx, it))
		same(t, fmt.Sprintf("Put %d", i), stored(it, got.ID), got)
	}
	got := get[types.Item](t)(s.Get(ctx, "r", false, "/img/sha256:ab/manifest.json"))
	if got.Path != "img/sha256:ab" || got.Name != "manifest.json" || got.Depth != 3 {
		t.Fatalf("Get = %q %q %d", got.Path, got.Name, got.Depth)
	}
	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(pages(t, 1, fn))
	}
	// Byte order puts ':' before '_'.
	same(t, "Children of the image", []string{"img/sha256:ab", "img/sha256__ab"},
		list(func(p store.Page) ([]types.Item, string, error) {
			return s.Children(ctx, "r", false, "img", p)
		}))
	same(t, "Descendants of the digest folder", []string{"img/sha256:ab/manifest.json"},
		list(func(p store.Page) ([]types.Item, string, error) {
			return s.Descendants(ctx, "r", false, "img/sha256:ab", p)
		}))
	search := func(query string) []string {
		t.Helper()
		q, err := aql.Parse(query)
		must(t, err)
		hits := get[[]aql.Hit](t)(s.Search(ctx, q, aql.Options{}))
		out := make([]string, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.Item.FullPath())
		}
		return out
	}
	same(t, "$eq on the path", []string{"img/sha256:ab/manifest.json"},
		search(`items.find({"repo":"r","path":"img/sha256:ab"})`))
	same(t, "$match on the path", []string{"img/sha256:ab/manifest.json"},
		search(`items.find({"repo":"r","path":{"$match":"img/sha256:*"}})`))
	same(t, "$match across both folders",
		[]string{"img/sha256:ab/manifest.json", "img/sha256__ab/manifest.json"},
		search(`items.find({"repo":"r","path":{"$match":"img/sha256*"}}).sort({"$asc":["path"]})`))
	res := get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "img/sha256:ab"))
	same(t, "Delete the digest folder", store.DeleteResult{
		Files: 1, Folders: 1, Bytes: blob(1).Size,
		BlobRefs: map[string]int64{blob(1).Checksums.SHA256: 1},
	}, res)
	if _, err := s.Get(ctx, "r", false, "img/sha256__ab/manifest.json"); err != nil {
		t.Fatalf("the tag folder went with the digest folder: %v", err)
	}
}

func testItemProperties(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	it := get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.jar", 1)))
	props := get[types.Properties](t)
	same(t, "no properties", types.Properties{}, props(s.Properties(ctx, it.ID)))
	// stamped checks when the properties of the item with id last changed,
	// zero for never or for none left.
	stamped := func(what string, id int64, when time.Time) {
		t.Helper()
		same(t, what, when, get[types.Item](t)(s.GetByID(ctx, id)).PropertiesModified)
	}
	stamped("a new item", it.ID, time.Time{})

	must(
		t,
		s.SetProperties(
			ctx,
			it.ID,
			types.Properties{"b": {"2", "1", "1"}, "a": {"x"}, "empty": nil},
			at(10),
		),
	)
	same(
		t,
		"SetProperties",
		types.Properties{"a": {"x"}, "b": {"1", "2"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("SetProperties", it.ID, at(10))
	// The same properties again, spelt differently, change nothing.
	must(t, s.SetProperties(ctx, it.ID, types.Properties{"b": {"1", "2", "2"}, "a": {"x"}}, at(11)))
	stamped("SetProperties without a change", it.ID, at(10))

	must(
		t,
		s.PatchProperties(
			ctx,
			it.ID,
			types.Properties{"c": {"3"}, "a": {"z", "y"}},
			[]string{"b", "absent"},
			at(12),
		),
	)
	same(
		t,
		"PatchProperties",
		types.Properties{"a": {"y", "z"}, "c": {"3"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("PatchProperties", it.ID, at(12))
	must(t, s.PatchProperties(ctx, it.ID, types.Properties{"c": {"3"}}, []string{"absent"}, at(13)))
	stamped("PatchProperties without a change", it.ID, at(12))
	// A key both removed and set ends with the set values.
	must(t, s.PatchProperties(ctx, it.ID, types.Properties{"c": {"4"}}, []string{"c"}, at(14)))
	same(
		t,
		"remove then set",
		types.Properties{"a": {"y", "z"}, "c": {"4"}},
		props(s.Properties(ctx, it.ID)),
	)
	stamped("remove then set", it.ID, at(14))

	got := props(s.Properties(ctx, it.ID))
	got["a"][0] = "changed"
	same(
		t,
		"copies",
		types.Properties{"a": {"y", "z"}, "c": {"4"}},
		props(s.Properties(ctx, it.ID)),
	)

	// Only the property methods stamp: Put keeps the stored time over the
	// one it is given, and a new item starts without one.
	replace := fileItem("r", "a/b.jar", 3)
	replace.PropertiesModified = at(90)
	same(t, "Put over an item", at(14), get[types.Item](t)(s.Put(ctx, replace)).PropertiesModified)
	fresh := fileItem("r", "a/c.jar", 2)
	fresh.PropertiesModified = at(91)
	other := get[types.Item](t)(s.Put(ctx, fresh))
	stamped("Put of a new item", other.ID, time.Time{})

	must(t, s.SetProperties(ctx, it.ID, types.Properties{"d": {""}}, at(15)))
	same(t, "SetProperties replaces", types.Properties{"d": {""}}, props(s.Properties(ctx, it.ID)))
	stamped("SetProperties replaces", it.ID, at(15))

	must(t, s.PatchProperties(ctx, other.ID, types.Properties{"k": {"v"}}, []string{"x"}, at(16)))
	same(
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
	same(t, "Children", []time.Time{at(15), at(16)}, when(pages(
		t,
		1,
		func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "a", p) },
	)))
	same(t, "Descendants", []time.Time{at(15), at(16)}, when(pages(
		t,
		1,
		func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, "", p) },
	)))
	same(t, "Get", at(16), get[types.Item](t)(s.Get(ctx, "r", false, "a/c.jar")).PropertiesModified)

	// Removing the last property clears the time, by either method.
	must(t, s.SetProperties(ctx, it.ID, nil, at(17)))
	stamped("SetProperties of nothing", it.ID, time.Time{})
	must(t, s.PatchProperties(ctx, other.ID, nil, []string{"k"}, at(18)))
	stamped("PatchProperties removing the last key", other.ID, time.Time{})

	missing := it.ID + 1000
	_, err := s.Properties(ctx, missing)
	wantErr(t, err, store.ErrNotFound)
	wantErr(
		t,
		s.SetProperties(ctx, missing, types.Properties{"a": {"1"}}, at(19)),
		store.ErrNotFound,
	)
	wantErr(t, s.PatchProperties(ctx, missing, nil, []string{"a"}, at(19)), store.ErrNotFound)

	// Properties, and their time, go with the item.
	must(t, s.SetProperties(ctx, it.ID, types.Properties{"e": {"1"}}, at(20)))
	get[store.DeleteResult](t)(s.Delete(ctx, "r", false, "a/b.jar"))
	again := get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.jar", 1)))
	same(t, "properties after re-deploy", types.Properties{}, props(s.Properties(ctx, again.ID)))
	stamped("re-deployed", again.ID, time.Time{})
}

// testRepositoryRoot covers the record a repository root gets for its own
// properties: found by Root, ID and AQL, and left out of every listing,
// usage total and content delete, which describe what is inside the root.
func testRepositoryRoot(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		_, err := s.Root(ctx, "r", cached)
		wantErr(t, err, store.ErrNotFound)
	}
	get[types.Item](t)(s.Put(ctx, folderItem("r", "a")))
	get[types.Item](t)(s.Put(ctx, fileItem("r", "a/b.txt", 1)))
	get[types.Item](t)(s.Put(ctx, fileItem("r", "z.txt", 2)))

	item := get[types.Item](t)
	root := item(s.EnsureRoot(ctx, types.Item{
		RepoKey: "r", Path: "ignored", Name: "ignored", Folder: false, Size: 9,
		Created: at(1), CreatedBy: "alice", Modified: at(2), ModifiedBy: "bob", Updated: at(3),
	}))
	want := types.RootItem("r", false)
	want.ID = root.ID
	want.Created, want.CreatedBy = at(1), "alice"
	want.Modified, want.ModifiedBy, want.Updated = at(2), "bob", at(3)
	same(t, "EnsureRoot", want, root)
	if root.FullPath() != "" || root.Depth != 0 {
		t.Fatalf("root full path %q depth %d", root.FullPath(), root.Depth)
	}
	same(t, "EnsureRoot again", want, item(s.EnsureRoot(ctx, types.Item{
		RepoKey: "r", Created: at(9), CreatedBy: "carol",
	})))
	same(t, "Root", want, item(s.Root(ctx, "r", false)))
	same(t, "GetByID", want, item(s.GetByID(ctx, root.ID)))
	cachedRoot := item(s.EnsureRoot(ctx, types.Item{RepoKey: "r", Cached: true}))
	if cachedRoot.ID == root.ID || !cachedRoot.Cached {
		t.Fatalf("the cache shares the repository's root: %+v", cachedRoot)
	}
	_, err := s.EnsureRoot(ctx, types.Item{})
	wantErr(t, err, store.ErrInvalid)
	for _, p := range []string{"", ".", "/"} {
		_, err := s.Get(ctx, "r", false, p)
		wantErr(t, err, store.ErrNotFound)
	}

	props := get[types.Properties](t)
	must(
		t,
		s.PatchProperties(
			ctx,
			root.ID,
			types.Properties{"k1": {"v2", "v1"}, "k2": {"x"}},
			nil,
			at(4),
		),
	)
	same(t, "root properties", types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}},
		props(s.Properties(ctx, root.ID)))
	want.PropertiesModified = at(4)
	same(t, "Root after its properties changed", want, item(s.Root(ctx, "r", false)))

	list := func(fn func(store.Page) ([]types.Item, string, error)) []string {
		t.Helper()
		return itemPaths(pages(t, 1, fn))
	}
	same(
		t,
		"Children of root",
		[]string{"a", "z.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", false, "", p) },
		),
	)
	same(
		t,
		"Descendants of root",
		[]string{"a", "z.txt", "a/b.txt"},
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Descendants(ctx, "r", false, ".", p) },
		),
	)
	same(
		t,
		"Children of the empty cache",
		[]string(nil),
		list(
			func(p store.Page) ([]types.Item, string, error) { return s.Children(ctx, "r", true, "", p) },
		),
	)
	same(t, "Usage", []store.RepoUsage{{RepoKey: "r", Files: 2, Folders: 1, Bytes: 300}},
		get[[]store.RepoUsage](t)(s.Usage(ctx)))

	// AQL reports the root as Artifactory does: a folder whose path and name
	// are both ".", found by its properties once folders are asked for.
	find := func(query string) []aql.Hit {
		t.Helper()
		q, err := aql.Parse(query)
		must(t, err)
		return get[[]aql.Hit](t)(s.Search(ctx, q, aql.Options{}))
	}
	hits := find(`items.find({"repo":"r","type":"folder","@k1":"v1"}).include("*","property")`)
	if len(hits) != 1 {
		t.Fatalf("root by property: %+v", hits)
	}
	same(t, "root hit", want, hits[0].Item)
	same(t, "root hit properties", types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}},
		hits[0].Properties)
	if hits := find(`items.find({"repo":"r","@k1":"v1"})`); len(hits) != 0 {
		t.Fatalf("a files query found the root: %+v", hits)
	}
	var names []string
	for _, h := range find(`items.find({"repo":"r","path":".","type":"any"})`) {
		names = append(names, h.Item.Name)
	}
	slices.Sort(names)
	same(t, "items at the root", []string{".", "a", "z.txt"}, names)

	// Deleting the repository's content keeps the root and its properties,
	// and does not count it.
	res := get[store.DeleteResult](t)(s.Delete(ctx, "r", false, ""))
	if res.Files != 2 || res.Folders != 1 {
		t.Fatalf("Delete root = %+v", res)
	}
	same(t, "root after a content delete", want, item(s.Root(ctx, "r", false)))
	same(t, "root properties after a content delete",
		types.Properties{"k1": {"v1", "v2"}, "k2": {"x"}}, props(s.Properties(ctx, root.ID)))
	same(t, "Delete of an empty root", store.DeleteResult{},
		get[store.DeleteResult](t)(s.Delete(ctx, "r", true, "")))

	must(t, s.DeleteRoot(ctx, "r", false))
	_, err = s.Root(ctx, "r", false)
	wantErr(t, err, store.ErrNotFound)
	_, err = s.Properties(ctx, root.ID)
	wantErr(t, err, store.ErrNotFound)
	must(t, s.DeleteRoot(ctx, "r", false))
	same(t, "the cache's root stays", cachedRoot, item(s.Root(ctx, "r", true)))

	// A new root after removal starts without properties.
	again := item(s.EnsureRoot(ctx, types.Item{RepoKey: "r"}))
	same(t, "properties of a new root", types.Properties{}, props(s.Properties(ctx, again.ID)))
}

func testItemStats(t *testing.T, s store.ItemStore) {
	ctx := context.Background()
	it := get[types.Item](t)(s.Put(ctx, fileItem("r", "a.jar", 1)))
	stats := get[types.ItemStats](t)
	same(t, "no statistics", types.ItemStats{}, stats(s.Stats(ctx, it.ID)))

	must(t, s.RecordDownload(ctx, it.ID, store.Download{By: "alice", At: at(1)}))
	must(t, s.RecordDownload(ctx, it.ID, store.Download{By: "bob", At: at(2)}))
	must(t, s.RecordDownload(ctx, it.ID, store.Download{
		By: "edge", At: at(3), Remote: true, Origin: "https://edge.example", Path: "r/a.jar",
	}))
	same(t, "after downloads", types.ItemStats{
		DownloadCount: 2, LastDownloaded: at(2), LastDownloadedBy: "bob",
		RemoteDownloadCount: 1, RemoteLastDownloaded: at(3), RemoteLastDownloadedBy: "edge",
		RemoteOrigin: "https://edge.example", RemotePath: "r/a.jar",
	}, stats(s.Stats(ctx, it.ID)))

	imported := types.ItemStats{DownloadCount: 42, LastDownloaded: at(9), LastDownloadedBy: "carol"}
	must(t, s.PutStats(ctx, it.ID, imported))
	same(t, "PutStats", imported, stats(s.Stats(ctx, it.ID)))

	missing := it.ID + 1000
	_, err := s.Stats(ctx, missing)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, s.RecordDownload(ctx, missing, store.Download{At: at(1)}), store.ErrNotFound)
	wantErr(t, s.PutStats(ctx, missing, imported), store.ErrNotFound)
}

func testUploads(t *testing.T, s store.UploadStore) {
	ctx := context.Background()
	u1 := store.Upload{
		ID: "u1", Backend: "s3", RepoKey: "docker-local", Path: "img/blobs", State: []byte("abc"),
		Bytes: 3, Started: at(1), Updated: at(5),
	}
	u2 := store.Upload{ID: "u2", Backend: "fs", Started: at(2)}
	u3 := store.Upload{ID: "u3"}
	u4 := store.Upload{ID: "u4", Started: at(1), Updated: at(10)}
	for _, u := range []store.Upload{u4, u3, u2, u1} {
		must(t, s.Put(ctx, u))
	}
	same(t, "Get", u1, get[store.Upload](t)(s.Get(ctx, "u1")))
	wantErr(t, s.Put(ctx, store.Upload{Bytes: 1}), store.ErrInvalid)

	u1.Bytes, u1.State = 6, []byte("abcdef")
	must(t, s.Put(ctx, u1))
	got := get[store.Upload](t)(s.Get(ctx, "u1"))
	same(t, "Get after replace", u1, got)
	got.State[0] = 'X'
	same(t, "copies", u1, get[store.Upload](t)(s.Get(ctx, "u1")))

	ids := func(us []store.Upload, err error) []string {
		t.Helper()
		must(t, err)
		out := make([]string, 0, len(us))
		for _, u := range us {
			out = append(out, u.ID)
		}
		return out
	}
	same(t, "Stale", []string{"u3", "u2", "u1"}, ids(s.Stale(ctx, at(6), 0)))
	same(t, "Stale limited", []string{"u3", "u2"}, ids(s.Stale(ctx, at(6), 2)))
	same(t, "Stale none", []string{"u3"}, ids(s.Stale(ctx, at(-100000), 0)))

	must(t, s.Delete(ctx, "u1"))
	_, err := s.Get(ctx, "u1")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, s.Delete(ctx, "u1"), store.ErrNotFound)
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
		e.DeletedAt = at(deleted)
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
		got := get[types.TrashEntry](t)(s.Add(ctx, e))
		want := e
		want.ID = got.ID
		want.Record.Item = stored(e.Record.Item, 0)
		same(t, "Add", want, got)
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
	wantErr(t, err, store.ErrInvalid)

	got := get[types.TrashEntry](t)(s.Get(ctx, c1.ID))
	same(t, "Get", c1, got)
	got.Record.Properties["build.number"][0] = "changed"
	same(t, "copies", c1, get[types.TrashEntry](t)(s.Get(ctx, c1.ID)))
	_, err = s.Get(ctx, 1<<40)
	wantErr(t, err, store.ErrNotFound)

	entries := get[[]types.TrashEntry](t)
	same(
		t,
		"Find newest first",
		trashPaths([]types.TrashEntry{c2, c1}),
		trashPaths(entries(s.Find(ctx, "r1", "/a/b/c.jar"))),
	)
	same(t, "Find nothing", []string{}, trashPaths(entries(s.Find(ctx, "r1", "a/../c"))))
	q2 := add(trashEntry("r1", "ab/q.txt", 7, 6))
	same(t, "Find puts an undated entry last", trashPaths([]types.TrashEntry{q2, q}),
		trashPaths(entries(s.Find(ctx, "r1", "ab/q.txt"))))

	list := func(repoKey, prefix string) []string {
		t.Helper()
		return trashPaths(pages(t, 2, func(p store.Page) ([]types.TrashEntry, string, error) {
			return s.List(ctx, repoKey, prefix, p)
		}))
	}
	same(t, "List all", trashPaths([]types.TrashEntry{z, c1, c2, d, q, q2, o}), list("", ""))
	same(t, "List r1 under a", trashPaths([]types.TrashEntry{c1, c2, d}), list("r1", "/a/"))
	same(t, "List an exact path", trashPaths([]types.TrashEntry{c1, c2}), list("r1", "a/b/c.jar"))
	same(t, "List a root file", trashPaths([]types.TrashEntry{z}), list("r1", "z.txt"))
	same(t, "List an invalid prefix", []string{}, list("r1", "a/.."))
	_, _, err = s.List(ctx, "", "", store.Page{After: store.Cursor("r1")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = s.List(ctx, "", "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	same(
		t,
		"Expired",
		trashPaths([]types.TrashEntry{d, z, c1}),
		trashPaths(entries(s.Expired(ctx, at(4), 0))),
	)
	same(
		t,
		"Expired limited",
		trashPaths([]types.TrashEntry{d}),
		trashPaths(entries(s.Expired(ctx, at(4), 1))),
	)

	must(t, s.Delete(ctx, c1.ID))
	wantErr(t, s.Delete(ctx, c1.ID), store.ErrNotFound)
	same(
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
	pkg := get[types.Package](t)(s.Ensure(ctx, types.Package{
		RepoKey: "npm-local", Type: repo.NPM, Name: "left-pad", Created: at(1),
	}))
	if pkg.ID == 0 {
		t.Fatal("Ensure assigned no ID")
	}
	same(
		t,
		"Ensure",
		types.Package{
			ID:      pkg.ID,
			RepoKey: "npm-local",
			Type:    repo.NPM,
			Name:    "left-pad",
			Created: at(1),
		},
		pkg,
	)
	same(t, "Ensure existing", pkg, get[types.Package](t)(s.Ensure(ctx, types.Package{
		RepoKey: "npm-local", Type: repo.NPM, Name: "left-pad", Created: at(9),
	})))
	_, err := s.Ensure(ctx, types.Package{RepoKey: "npm-local"})
	wantErr(t, err, store.ErrInvalid)
	same(t, "Get", pkg, get[types.Package](t)(s.Get(ctx, pkg.ID)))
	same(t, "Find", pkg, get[types.Package](t)(s.Find(ctx, "npm-local", "left-pad")))
	_, err = s.Get(ctx, pkg.ID+1000)
	wantErr(t, err, store.ErrNotFound)
	_, err = s.Find(ctx, "npm-local", "nope")
	wantErr(t, err, store.ErrNotFound)

	for _, name := range []string{"b-pkg", "a-pkg", "@scope/z"} {
		get[types.Package](
			t,
		)(
			s.Ensure(ctx, types.Package{RepoKey: "npm-local", Type: repo.NPM, Name: name}),
		)
	}
	get[types.Package](
		t,
	)(
		s.Ensure(ctx, types.Package{RepoKey: "npm-remote", Type: repo.NPM, Name: "left-pad"}),
	)
	var names []string
	for _, p := range pages(t, 2, func(p store.Page) ([]types.Package, string, error) {
		return s.List(ctx, "npm-local", p)
	}) {
		names = append(names, p.Name)
	}
	same(t, "List", []string{"@scope/z", "a-pkg", "b-pkg", "left-pad"}, names)
	for _, bad := range []string{"%%%", store.Cursor("a", "b")} {
		_, _, err = s.List(ctx, "npm-local", store.Page{After: bad})
		wantErr(t, err, store.ErrInvalidCursor)
	}

	put := func(version string, created int, meta string) types.PackageVersion {
		t.Helper()
		v := types.PackageVersion{PackageID: pkg.ID, Version: version, Created: at(created)}
		if meta != "" {
			v.Metadata = []byte(meta)
		}
		return get[types.PackageVersion](t)(s.PutVersion(ctx, v))
	}
	put("1.10.0", 1, `{"dist":"a"}`)
	put("1.2.0", 2, "")
	put("1.9.0-beta", 3, "")
	top := put("2.0.0", 4, "")
	if top.OrderKey != 4 || top.ID == 0 {
		t.Fatalf("PutVersion = %+v", top)
	}
	versions := get[[]types.PackageVersion](t)
	same(t, "Versions in semver order", []string{"1.2.0#1", "1.9.0-beta#2", "1.10.0#3", "2.0.0#4"},
		versionNames(versions(s.Versions(ctx, pkg.ID))))
	put("1.0.0", 5, "")
	same(
		t,
		"Versions after an older one",
		[]string{"1.0.0#1", "1.2.0#2", "1.9.0-beta#3", "1.10.0#4", "2.0.0#5"},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)

	v110 := get[types.PackageVersion](t)(s.Version(ctx, pkg.ID, "1.10.0"))
	same(t, "Version", types.PackageVersion{
		ID:        v110.ID,
		PackageID: pkg.ID,
		Version:   "1.10.0",
		OrderKey:  4,
		Created:   at(1),
		Metadata:  []byte(`{"dist":"a"}`),
	}, v110)
	replaced := put("1.10.0", 7, `{"dist":"b"}`)
	if replaced.ID != v110.ID || string(replaced.Metadata) != `{"dist":"b"}` ||
		replaced.OrderKey != 4 {
		t.Fatalf("replacing PutVersion = %+v", replaced)
	}
	replaced.Metadata[0] = 'X'
	if string(
		get[types.PackageVersion](t)(s.Version(ctx, pkg.ID, "1.10.0")).Metadata,
	) != `{"dist":"b"}` {
		t.Fatal("Version returned shared memory")
	}
	_, err = s.Version(ctx, pkg.ID, "9.9.9")
	wantErr(t, err, store.ErrNotFound)
	_, err = s.PutVersion(ctx, types.PackageVersion{PackageID: pkg.ID + 1000, Version: "1"})
	wantErr(t, err, store.ErrNotFound)
	_, err = s.PutVersion(ctx, types.PackageVersion{PackageID: pkg.ID})
	wantErr(t, err, store.ErrInvalid)

	// File links, cascading from both sides.
	itemsStore := st.Items()
	jar := get[types.Item](
		t,
	)(
		itemsStore.Put(ctx, fileItem("npm-local", "left-pad/-/left-pad-1.10.0.tgz", 1)),
	)
	pom := get[types.Item](
		t,
	)(
		itemsStore.Put(ctx, fileItem("npm-local", "left-pad/package.json", 2)),
	)
	for _, f := range []types.VersionFile{{VersionID: v110.ID, ItemID: pom.ID}, {VersionID: v110.ID, ItemID: jar.ID}, {VersionID: v110.ID, ItemID: jar.ID}} {
		must(t, s.LinkFile(ctx, f))
	}
	ids := get[[]int64](t)
	same(t, "Files", []int64{min(jar.ID, pom.ID), max(jar.ID, pom.ID)}, ids(s.Files(ctx, v110.ID)))
	same(t, "VersionOf", v110.ID, get[types.PackageVersion](t)(s.VersionOf(ctx, jar.ID)).ID)
	wantErr(
		t,
		s.LinkFile(ctx, types.VersionFile{VersionID: v110.ID + 1000, ItemID: jar.ID}),
		store.ErrNotFound,
	)
	wantErr(
		t,
		s.LinkFile(ctx, types.VersionFile{VersionID: v110.ID, ItemID: jar.ID + 1000}),
		store.ErrNotFound,
	)
	_, err = s.VersionOf(ctx, jar.ID+1000)
	wantErr(t, err, store.ErrNotFound)
	get[store.DeleteResult](t)(itemsStore.Delete(ctx, "npm-local", false, pom.FullPath()))
	same(t, "Files after the item went", []int64{jar.ID}, ids(s.Files(ctx, v110.ID)))

	must(t, s.RecordVersionDownload(ctx, v110.ID, at(9)))
	if got := get[types.PackageVersion](
		t,
	)(
		s.Version(ctx, pkg.ID, "1.10.0"),
	); !got.LastDownloaded.Equal(
		want(at(9)),
	) {
		t.Fatalf("LastDownloaded = %v", got.LastDownloaded)
	}
	wantErr(t, s.RecordVersionDownload(ctx, v110.ID+1000, at(9)), store.ErrNotFound)

	must(t, s.DeleteVersion(ctx, v110.ID))
	wantErr(t, s.DeleteVersion(ctx, v110.ID), store.ErrNotFound)
	same(t, "Versions after delete", []string{"1.0.0#1", "1.2.0#2", "1.9.0-beta#3", "2.0.0#4"},
		versionNames(versions(s.Versions(ctx, pkg.ID))))
	same(t, "Files of a deleted version", []int64(nil), ids(s.Files(ctx, v110.ID)))
	_, err = s.VersionOf(ctx, jar.ID)
	wantErr(t, err, store.ErrNotFound)

	must(t, s.Delete(ctx, pkg.ID))
	wantErr(t, s.Delete(ctx, pkg.ID), store.ErrNotFound)
	_, err = s.Version(ctx, pkg.ID, "2.0.0")
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"Versions of a deleted package",
		[]string{},
		versionNames(versions(s.Versions(ctx, pkg.ID))),
	)
}
