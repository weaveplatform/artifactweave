package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccTrashStorageViews: the trash can serves the storage API's views as
// any repository does (#455).
func TestAccTrashStorageViews(t *testing.T) {
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
func TestAccTrashStorageReadFailure(t *testing.T) {
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

func (f trashFaults) Settings() store.SettingsStore {
	return faultySettings{f.Store.Settings(), f.fail}
}

// faultySettings fails reads of the trash can setting while fail holds
// "settings"; the other settings, which every request's authentication
// reads, still answer.
type faultySettings struct {
	store.SettingsStore
	fail map[string]int
}

func (f faultySettings) Get(ctx context.Context, key string, into any) error {
	if key == types.SettingTrashcan && f.fail["settings"] > 0 {
		return errBoom
	}
	return f.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

// TestAccTrashStorageWhileOff: while the trash can is off, its views answer as
// for a path that is not there, to an administrator too, and what it held
// stays, so restore still finds it and turning it on shows it again (#490).
func TestAccTrashStorageWhileOff(t *testing.T) {
	w := newItemWorld(t, true)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/one.txt", "admin", "one"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/two.txt", "admin", "two"),
		http.StatusCreated)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/a", "admin", ""), http.StatusNoContent)
	setTrashcan(t, w.db, false)
	// "" is anonymous, whose access is on here.
	for _, as := range []string{"admin", "deployer", "user", ""} {
		for _, path := range []string{
			"", "?list", "?list&deep=1&listFolders=1", "/libs-local", "/libs-local/a/one.txt",
			"/libs-local/a/one.txt?properties", "/libs-local/a/one.txt?stats",
			"/libs-local/a?lastModified", "/libs-local/a/one.txt?permissions",
			"/libs-local/absent", "?list&stats",
		} {
			rec := w.do("GET", trashURL+path, as, "")
			wantStatus(t, rec, http.StatusNotFound)
			if !strings.Contains(rec.Body.String(), "Could not find resource") {
				t.Fatalf("%s as %q: %s", path, as, rec.Body)
			}
		}
	}
	// A query that does not parse is still a bad request.
	wantStatus(t, w.do("GET", trashURL+"?properties=%zz", "admin", ""), http.StatusBadRequest)

	// The trashed files are kept: restore brings one back while the trash
	// can is off, and the other shows again once it is on.
	wantStatus(t, w.do("POST", "/artifactory/api/trash/restore/libs-local/a/one.txt?to=libs-local",
		"admin", ""), http.StatusAccepted)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a/one.txt", "admin", ""), http.StatusOK)
	setTrashcan(t, w.db, true)
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/two.txt", "admin", ""), http.StatusOK)
	wantStatus(t, w.do("GET", trashURL+"/libs-local/a/one.txt", "admin", ""),
		http.StatusNotFound)
}

// A failure reading the trash can setting is a server error, not a 404.
func TestAccTrashStorageSettingsFailure(t *testing.T) {
	fail := map[string]int{"settings": 1}
	w := newItemWorld(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = trashFaults{Store: db, fail: fail}
	})
	wantStatus(t, w.do("GET", trashURL, "admin", ""), http.StatusInternalServerError)
	// Without credentials, while anonymous access is off, the caller is told
	// to sign in before the setting is read.
	wantStatus(t, w.do("GET", trashURL, "", ""), http.StatusUnauthorized)
}
