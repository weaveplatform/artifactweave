package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const trashURL = "/artifactory/api/storage/auto-trashcan"

// seedTrash adds an entry trashed from repoKey at full, modified and deleted
// at the times given, so the views' times are known.
func seedTrash(
	t *testing.T,
	db store.Store,
	repoKey, full string,
	folder bool,
	modified, deleted time.Time,
) {
	t.Helper()
	parent, name, depth, err := types.SplitItemPath(full)
	if err != nil {
		t.Fatal(err)
	}
	it := types.Item{
		RepoKey: repoKey, Path: parent, Name: name, Depth: depth, Folder: folder,
		Created: modified, Modified: modified, Updated: modified,
	}
	if !folder {
		it.Size = int64(len(full))
		it.Actual = types.Checksums{SHA1: sha1Hex(full), SHA256: sha256Hex(full)}
	}
	if _, err := db.Trash().Add(context.Background(), types.TrashEntry{
		Record:    types.TrashRecord{Item: it, PackageType: "generic"},
		DeletedAt: deleted, DeletedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
}

// fileURIs are the uris of a FileList body's entries, in order.
func fileURIs(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	for _, f := range body["files"].([]any) { //nolint:forcetypeassert // fixture
		out = append(out, f.(map[string]any)["uri"].(string)) //nolint:forcetypeassert // fixture
	}
	return out
}

// TestTrashStorageViews: the trash can serves the storage API's views as
// any repository does (#455).
func TestTrashStorageViews(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/one.txt;k=v", "admin", "one"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/b/two.txt", "admin", "two"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/other-local/o.txt", "admin", "o"), http.StatusCreated)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/a", "admin", ""), http.StatusNoContent)
	wantStatus(t, w.do("DELETE", "/artifactory/other-local/o.txt", "admin", ""),
		http.StatusNoContent)
	get := func(path string, want int) map[string]any {
		t.Helper()
		rec := w.do("GET", trashURL+path, "admin", "")
		wantStatus(t, rec, want)
		if want != http.StatusOK {
			return nil
		}
		return decodeObject(t, rec.Body.String())
	}
	list := func(path string, want ...string) map[string]any {
		t.Helper()
		body := get(path, http.StatusOK)
		if got := fileURIs(t, body); !slices.Equal(got, want) {
			t.Fatalf("%s lists %v, want %v", path, got, want)
		}
		return body
	}

	// The root holds a folder per repository; a direct listing of files
	// finds none there.
	list("?list")
	list("?list&listFolders=1", "/libs-local", "/other-local")
	list("?list&deep=1", "/libs-local/a/b/two.txt", "/libs-local/a/one.txt", "/other-local/o.txt")
	list("?list&deep=1&listFolders=1&depth=2",
		"/libs-local", "/libs-local/a", "/other-local", "/other-local/o.txt")
	body := list("/libs-local?list&deep=1&listFolders=1&includeRootPath=1&mdTimestamps=1",
		"/", "/a", "/a/b", "/a/b/two.txt", "/a/one.txt")
	if body["uri"] != "https://reg.example"+trashURL+"/libs-local" {
		t.Fatalf("list uri %v", body["uri"])
	}
	files := body["files"].([]any)      //nolint:forcetypeassert // fixture
	self := files[0].(map[string]any)   //nolint:forcetypeassert // fixture
	folder := files[1].(map[string]any) //nolint:forcetypeassert // fixture
	one := files[4].(map[string]any)    //nolint:forcetypeassert // fixture
	// The repository's folder holds only what was trashed below it, so it
	// has no record and no properties; what was trashed has both.
	if self["size"] != float64(-1) || self["mdTimestamps"] != nil ||
		folder["folder"] != true || folder["mdTimestamps"] == nil ||
		one["size"] != float64(3) || one["sha1"] != sha1Hex("one") ||
		one["sha2"] != sha256Hex("one") || one["mdTimestamps"] == nil {
		t.Fatalf("entries %v", files)
	}
	get("/libs-local/a/one.txt?list", http.StatusBadRequest)
	get("/libs-local/absent?list", http.StatusNotFound)
	get("?list&deep=2", http.StatusBadRequest)

	// The properties of a trashed item are its own and the trash.* ones.
	rec := w.do("GET", trashURL+"/libs-local/a/one.txt?properties", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Content-Type") != mediaProperties {
		t.Fatalf("properties type %q", rec.Header().Get("Content-Type"))
	}
	props := decodeObject(t, rec.Body.String())["properties"].(map[string]any) //nolint:forcetypeassert // fixture
	millis, err := strconv.ParseInt(
		props["trash.time"].([]any)[0].(string), 10, 64) //nolint:forcetypeassert // fixture
	if err != nil || time.Since(time.UnixMilli(millis)) > time.Minute ||
		props["k"].([]any)[0] != "v" || //nolint:forcetypeassert // fixture
		props["trash.deletedBy"].([]any)[0] != "admin" || //nolint:forcetypeassert // fixture
		props["trash.originalRepository"].([]any)[0] != "libs-local" || //nolint:forcetypeassert // fixture
		props["trash.originalRepositoryType"].([]any)[0] != "generic" || //nolint:forcetypeassert // fixture
		props["trash.originalPath"].([]any)[0] != "a/one.txt" { //nolint:forcetypeassert // fixture
		t.Fatalf("properties %v", props)
	}
	named := get("/libs-local/a/one.txt?properties=k,trash.deletedBy", http.StatusOK)
	if len(named["properties"].(map[string]any)) != 2 { //nolint:forcetypeassert // fixture
		t.Fatalf("named properties %v", named)
	}
	// A trashed folder carries the trash.* properties too.
	get("/libs-local/a?properties=trash.originalPath", http.StatusOK)
	get("/libs-local/a/one.txt?properties=absent", http.StatusNotFound)
	get("/libs-local?properties", http.StatusNotFound)
	get("?properties", http.StatusNotFound)

	// Statistics are kept for files, and the trash can's copy has none.
	stats := get("/libs-local/a/one.txt?stats", http.StatusOK)
	if stats["downloadCount"] != float64(0) || stats["lastDownloaded"] != float64(0) {
		t.Fatalf("stats %v", stats)
	}
	get("/libs-local/a?stats", http.StatusBadRequest)

	// The file info names when and by whom the file was trashed.
	info := w.do("GET", trashURL+"/libs-local/a/one.txt", "admin", "")
	wantStatus(t, info, http.StatusOK)
	if info.Header().Get("Content-Type") != mediaFileInfo {
		t.Fatalf("file info type %q", info.Header().Get("Content-Type"))
	}
	infoProps := decodeObject(t, info.Body.String())["properties"].(map[string]any) //nolint:forcetypeassert // fixture
	infoTime := infoProps["trash.time"].([]any)[0]                                  //nolint:forcetypeassert // fixture
	if infoTime != props["trash.time"].([]any)[0] ||                                //nolint:forcetypeassert // fixture
		infoProps["trash.originalRepositoryType"] == nil {
		t.Fatalf("file info properties %v", infoProps)
	}

	// The permissions view does not apply; it is still for administrators.
	get("/libs-local/a/one.txt?permissions", http.StatusBadRequest)
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt?permissions", "deployer", ""),
		http.StatusForbidden)

	// The modes combine as on any repository.
	get("?list&stats", http.StatusBadRequest)
	get("?deep=1", http.StatusBadRequest)
	get("?properties=%zz", http.StatusBadRequest)
}

// TestTrashStorageLastModified: a trashed file reports its own time and a
// folder the newest item below it.
func TestTrashStorageLastModified(t *testing.T) {
	w := newItemWorld(t, false)
	// An empty trash can has nothing to report.
	wantStatus(t, w.do("GET", trashURL+"?lastModified", "admin", ""), http.StatusNotFound)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedTrash(t, w.db, "libs-local", "a/old.txt", false, t0, t0)
	seedTrash(t, w.db, "libs-local", "a/b/new.txt", false, t0.Add(time.Hour), t0)
	seedTrash(t, w.db, "other-local", "o.txt", false, t0.Add(2*time.Hour), t0)
	last := func(as, path string) map[string]any {
		t.Helper()
		rec := w.do("GET", trashURL+path+"?lastModified", as, "")
		wantStatus(t, rec, http.StatusOK)
		return decodeObject(t, rec.Body.String())
	}
	if got := last("admin", "/libs-local/a/old.txt"); got["lastModified"] != formatTime(t0) {
		t.Fatalf("file %v", got)
	}
	if got := last("admin", "/libs-local"); got["uri"] !=
		"https://reg.example"+trashURL+"/libs-local/a/b/new.txt" {
		t.Fatalf("folder %v", got)
	}
	if got := last("admin", ""); got["lastModified"] != formatTime(t0.Add(2*time.Hour)) {
		t.Fatalf("root %v", got)
	}
	// What the caller does not see does not count, though it is newer.
	if got := last("deployer", ""); got["lastModified"] != formatTime(t0.Add(time.Hour)) {
		t.Fatalf("deployer's root %v", got)
	}
}

// TestTrashStorageViewsFiltered: in every view, a caller that is not an
// administrator sees only what was trashed from repositories where it holds
// deploy or manage, at each item's original path (#455, #439).
func TestTrashStorageViewsFiltered(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedTrash(t, w.db, "libs-local", "a", true, t0, t0)
	seedTrash(t, w.db, "libs-local", "a/one.txt", false, t0, t0)
	seedTrash(t, w.db, "libs-local", "a/secret/two.txt", false, t0.Add(time.Hour), t0)
	seedTrash(t, w.db, "other-local", "o/three.txt", false, t0.Add(2*time.Hour), t0)
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-a",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionWrite}},
			Targets: map[string]types.Target{
				"libs-local": {Include: []string{"a/**"}, Exclude: []string{"a/secret/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	list := func(path string, want ...string) {
		t.Helper()
		rec := w.do("GET", trashURL+path, "user", "")
		wantStatus(t, rec, http.StatusOK)
		if got := fileURIs(t, decodeObject(t, rec.Body.String())); !slices.Equal(got, want) {
			t.Fatalf("%s lists %v, want %v", path, got, want)
		}
	}
	list("?list&deep=1&listFolders=1", "/libs-local", "/libs-local/a", "/libs-local/a/one.txt")
	list("/libs-local?list&deep=1", "/a/one.txt")
	list("/libs-local/a?list&listFolders=1&includeRootPath=1", "/", "/one.txt")
	rec := w.do("GET", trashURL+"/libs-local/a?lastModified", "user", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeObject(t, rec.Body.String()); got["lastModified"] != formatTime(t0) {
		t.Fatalf("lastModified %v", got)
	}
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt?properties", "user", ""),
		http.StatusOK)
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt?stats", "user", ""), http.StatusOK)
	// Every view of what it does not see is refused before the trash is
	// read, so the refusal does not tell whether anything is there.
	for _, path := range []string{
		"/other-local", "/other-local/o", "/other-local/o/three.txt", "/other-local/absent",
		"/libs-local/a/secret", "/libs-local/a/secret/two.txt",
	} {
		for _, view := range []string{
			"", "?list", "?list&deep=1&listFolders=1", "?properties", "?stats",
			"?lastModified", "?permissions",
		} {
			wantStatus(t, w.do("GET", trashURL+path+view, "user", ""), http.StatusForbidden)
		}
	}
	// Anonymous is told to sign in, in every view.
	for _, view := range []string{"?list", "?properties", "?stats", "?lastModified"} {
		wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt"+view, "", ""),
			http.StatusUnauthorized)
	}
}

// trashFaults is an in-memory store whose trash fails the reads named in
// fail: a count of 1 fails the next call and every one after it, and a
// higher count lets that many calls less one through first.
type trashFaults struct {
	*inmem.Store
	fail map[string]int
}

func (f trashFaults) Trash() store.TrashStore { return faultyTrash{f.Store.Trash(), f.fail} }

type faultyTrash struct {
	store.TrashStore
	fail map[string]int
}

func (f faultyTrash) fails(op string) bool {
	switch n := f.fail[op]; {
	case n == 1:
		return true
	case n > 1:
		f.fail[op] = n - 1
	}
	return false
}

func (f faultyTrash) List(
	ctx context.Context,
	repoKey, prefix string,
	p store.Page,
) ([]types.TrashEntry, string, error) {
	if f.fails("list") {
		return nil, "", errBoom
	}
	return f.TrashStore.List(ctx, repoKey, prefix, p) //nolint:wrapcheck // test double
}

func (f faultyTrash) Find(ctx context.Context, repoKey, full string) ([]types.TrashEntry, error) {
	if f.fails("find") {
		return nil, errBoom
	}
	return f.TrashStore.Find(ctx, repoKey, full) //nolint:wrapcheck // test double
}

// A failure reading the trash is a server error in every view, not an empty
// view or a 404.
func TestTrashStorageReadFailure(t *testing.T) {
	fail := map[string]int{}
	w := newItemWorld(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = trashFaults{Store: db, fail: fail}
	})
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedTrash(t, w.db, "libs-local", "a/one.txt", false, t0, t0)
	fail["find"] = 1
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt", "admin", ""),
		http.StatusInternalServerError)
	fail["find"] = 0
	// Finding the folder reads the trash once; a list or lastModified view
	// reads it again.
	for view, reads := range map[string]int{"": 1, "?list": 2, "?lastModified": 2} {
		for n := 1; n <= reads; n++ {
			fail["list"] = n
			wantStatus(t, w.do("GET", trashURL+view, "admin", ""), http.StatusInternalServerError)
		}
	}
}
