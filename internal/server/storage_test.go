package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// storageWorld is an item world with a small tree in libs-local and
// other-local (both members of virt):
//
//	libs-local/dir/a.txt, libs-local/dir/sub/b.txt, libs-local/top.txt
//	other-local/dir/c.txt, other-local/dir/a.txt (shadowed by libs-local's)
func storageWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	for path, body := range map[string]string{
		"/artifactory/libs-local/dir/a.txt":     "aaa",
		"/artifactory/libs-local/dir/sub/b.txt": "bbbb",
		"/artifactory/libs-local/top.txt":       "t",
		"/artifactory/other-local/dir/c.txt":    "cc",
		"/artifactory/other-local/dir/a.txt":    "other",
	} {
		wantStatus(t, w.do(http.MethodPut, path, "admin", body), http.StatusCreated)
	}
	return w
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

func TestStorageInfo(t *testing.T) {
	w := storageWorld(t, false)
	const api = "/artifactory/api/storage/"

	rec := w.do(http.MethodGet, api+"libs-local/dir/a.txt", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != mediaFileInfo {
		t.Fatalf("content type %q", ct)
	}
	file := decodeObject(t, rec.Body.String())
	for k, want := range map[string]any{
		"uri":         "https://reg.example/artifactory/api/storage/libs-local/dir/a.txt",
		"downloadUri": "https://reg.example/artifactory/libs-local/dir/a.txt",
		"repo":        "libs-local",
		"path":        "/dir/a.txt",
		"size":        "3",
		"createdBy":   "admin",
	} {
		if file[k] != want {
			t.Errorf("file %s = %v, want %v", k, file[k], want)
		}
	}
	if file["checksums"].(map[string]any)["sha1"] != sha1Hex("aaa") || file["lastModified"] == nil {
		t.Errorf("file info %v", file)
	}

	rec = w.do(http.MethodGet, api+"libs-local/dir", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	folder := decodeObject(t, rec.Body.String())
	want := []any{
		map[string]any{"uri": "/a.txt", "folder": false},
		map[string]any{"uri": "/sub", "folder": true},
	}
	if rec.Header().Get("Content-Type") != mediaFolderInfo ||
		!reflect.DeepEqual(folder["children"], want) || folder["path"] != "/dir" {
		t.Fatalf("folder info %v", folder)
	}

	// The root, with and without a trailing slash, has no stored item: no
	// times, path "/".
	for _, p := range []string{api + "libs-local", api + "libs-local/"} {
		rec = w.do(http.MethodGet, p, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		root := decodeObject(t, rec.Body.String())
		if root["path"] != "/" || root["created"] != nil ||
			root["uri"] != "https://reg.example/artifactory/api/storage/libs-local" ||
			len(root["children"].([]any)) != 2 {
			t.Fatalf("root info %s: %v", p, root)
		}
	}

	// A virtual repository reports under its own key and merges its members'
	// children, the first member that holds a name winning.
	rec = w.do(http.MethodGet, api+"virt/dir/a.txt", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if v := decodeObject(t, rec.Body.String()); v["repo"] != "virt" || v["size"] != "5" {
		t.Fatalf("virtual file %v", v)
	}
	rec = w.do(http.MethodGet, api+"virt/dir", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var names []string
	for _, c := range decodeObject(t, rec.Body.String())["children"].([]any) {
		names = append(names, c.(map[string]any)["uri"].(string))
	}
	if strings.Join(names, " ") != "/a.txt /c.txt /sub" {
		t.Fatalf("virtual children %v", names)
	}

	wantStatus(t, w.do(http.MethodGet, api+"libs-local/absent", "admin", ""), http.StatusNotFound)
	wantStatus(t, w.do(http.MethodGet, api+"missing/x", "admin", ""), http.StatusNotFound)
	wantStatus(
		t,
		w.do(http.MethodGet, api+"libs-local/dir/a.txt", "user", ""),
		http.StatusForbidden,
	)
	wantStatus(t, w.do(http.MethodGet, api+"libs-local/dir/a.txt", "", ""), http.StatusUnauthorized)
}

func TestStorageList(t *testing.T) {
	w := storageWorld(t, true)
	const list = "/artifactory/api/storage/libs-local?list"
	files := func(query string) []string {
		t.Helper()
		rec := w.do(http.MethodGet, list+query, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		if rec.Header().Get("Content-Type") != mediaFileList {
			t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
		}
		var out []string
		for _, f := range decodeObject(t, rec.Body.String())["files"].([]any) {
			e := f.(map[string]any)
			s := e["uri"].(string) + ":" + e["size"].(string)
			if _, ok := e["mdTimestamps"]; ok {
				s += ":md"
			}
			out = append(out, s)
		}
		return out
	}
	for query, want := range map[string]string{
		"":                               "/top.txt:1",
		"&deep=1":                        "/dir/a.txt:3 /dir/sub/b.txt:4 /top.txt:1",
		"&deep=1&listFolders=1":          "/dir:-1 /dir/a.txt:3 /dir/sub:-1 /dir/sub/b.txt:4 /top.txt:1",
		"&deep=1&listFolders=1&depth=1":  "/dir:-1 /top.txt:1",
		"&deep=1&depth=2":                "/dir/a.txt:3 /top.txt:1",
		"&deep=1&depth=0":                "/dir/a.txt:3 /dir/sub/b.txt:4 /top.txt:1",
		"&mdTimestamps=1":                "/top.txt:1:md",
		"&deep=1&listFolders=":           "/dir/a.txt:3 /dir/sub/b.txt:4 /top.txt:1",
		"&deep=true&includeRootPath=1":   "/dir/a.txt:3 /dir/sub/b.txt:4 /top.txt:1",
		"&listFolders=1&includeRootPath": "/dir:-1 /top.txt:1",
	} {
		if got := strings.Join(files(query), " "); got != want {
			t.Errorf("list%s = %q, want %q", query, got, want)
		}
	}
	// Below a folder, uris are relative to it unless includeRootPath asks
	// for the full path.
	rec := w.do(
		http.MethodGet,
		"/artifactory/api/storage/libs-local/dir?list&deep=1&includeRootPath=1",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	first := decodeObject(t, rec.Body.String())["files"].([]any)[0].(map[string]any)
	if first["uri"] != "/dir/a.txt" || first["sha1"] != sha1Hex("aaa") {
		t.Fatalf("includeRootPath entry %v", first)
	}
	rec = w.do(http.MethodGet, "/artifactory/api/storage/libs-local/dir?list", "admin", "")
	if got := decodeObject(t, rec.Body.String())["files"].([]any)[0].(map[string]any)["uri"]; got != "/a.txt" {
		t.Fatalf("relative entry %v", got)
	}

	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/api/storage/libs-local/top.txt?list", "admin", ""),
		http.StatusBadRequest,
	)
	for _, bad := range []string{"&deep=2", "&depth=-1", "&depth=x"} {
		wantStatus(t, w.do(http.MethodGet, list+bad, "admin", ""), http.StatusBadRequest)
	}
	// Listing refuses anonymous callers even where they may read.
	grant(t, w, "anon-read", "anonymous", "libs-local", types.ActionRead)
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/api/storage/libs-local/top.txt", "", ""),
		http.StatusOK,
	)
	wantStatus(t, w.do(http.MethodGet, list, "", ""), http.StatusUnauthorized)
}

// grant gives user actions on a repository.
func grant(t *testing.T, w *itemWorld, name, user, repoKey string, a types.Actions) {
	t.Helper()
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: name,
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{user: a}},
			Targets: map[string]types.Target{repoKey: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageModes(t *testing.T) {
	w := storageWorld(t, false)
	const file = "/artifactory/api/storage/libs-local/top.txt"
	for _, q := range []string{"?properties&stats", "?list&lastModified", "?deep=1", "?includeRootPath=1"} {
		wantStatus(t, w.do(http.MethodGet, file+q, "admin", ""), http.StatusBadRequest)
	}
	wantStatus(t, w.do(http.MethodGet, file+"?%zz", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do(http.MethodGet, file+"?a=%zz", "admin", ""), http.StatusBadRequest)
}

func TestStorageProperties(t *testing.T) {
	w := storageWorld(t, false)
	const api = "/artifactory/api/storage/"
	props := func(path string) map[string]any {
		t.Helper()
		rec := w.do(http.MethodGet, api+path+"?properties", "admin", "")
		if rec.Code == http.StatusNotFound {
			return nil
		}
		wantStatus(t, rec, http.StatusOK)
		return decodeObject(t, rec.Body.String())["properties"].(map[string]any)
	}

	// ";" and "|" separate properties, "," values, and %5C escapes.
	wantStatus(
		t,
		w.do(http.MethodPut, api+"libs-local/dir/a.txt?properties=a=1,2;b=x|c=y%5C,z&recursive=0",
			"admin", ""),
		http.StatusNoContent,
	)
	got := props("libs-local/dir/a.txt")
	want := map[string]any{"a": []any{"1", "2"}, "b": []any{"x"}, "c": []any{"y,z"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("properties %v, want %v", got, want)
	}
	rec := w.do(http.MethodGet, api+"libs-local/dir/a.txt?properties=a,zz", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if p := decodeObject(
		t,
		rec.Body.String(),
	); rec.Header().
		Get("Content-Type") !=
		mediaProperties ||
		len(p["properties"].(map[string]any)) != 1 {
		t.Fatalf("named properties %v", p)
	}
	wantStatus(t, w.do(http.MethodGet, api+"libs-local/dir/a.txt?properties=zz", "admin", ""),
		http.StatusNotFound)
	wantStatus(
		t,
		w.do(http.MethodGet, api+"libs-local?properties", "admin", ""),
		http.StatusNotFound,
	)

	// recursive (the default) reaches everything below a folder; 0 only the
	// folder.
	wantStatus(t, w.do(http.MethodPut, api+"libs-local/dir?properties=team=core", "admin", ""),
		http.StatusNoContent)
	if props("libs-local/dir/sub/b.txt")["team"] == nil || props("libs-local/dir")["team"] == nil {
		t.Fatal("recursive set missed an item")
	}
	wantStatus(
		t,
		w.do(http.MethodPut, api+"libs-local/dir?properties=only=folder&recursive=0", "admin", ""),
		http.StatusNoContent,
	)
	if props("libs-local/dir/a.txt")["only"] != nil || props("libs-local/dir")["only"] == nil {
		t.Fatal("recursive=0 reached below the folder")
	}
	// The root holds properties of its own, and recursive reaches every
	// item below it too.
	wantStatus(
		t,
		w.do(http.MethodPut, api+"libs-local?properties=all=yes", "admin", ""),
		http.StatusNoContent,
	)
	if props("libs-local/top.txt")["all"] == nil || props("libs-local")["all"] == nil {
		t.Fatal("root set missed the root or top.txt")
	}
	wantStatus(t, w.do(http.MethodPut, api+"libs-local?properties=none=1&recursive=0", "admin", ""),
		http.StatusNoContent)
	if props("libs-local/top.txt")["none"] != nil || props("libs-local")["none"] == nil {
		t.Fatal("recursive=0 on the root reached below it, or missed the root")
	}

	wantStatus(
		t,
		w.do(http.MethodDelete, api+"libs-local/dir/a.txt?properties=a,b,absent&recursive=0",
			"admin", ""),
		http.StatusNoContent,
	)
	if got := props("libs-local/dir/a.txt"); got["a"] != nil || got["b"] != nil || got["c"] == nil {
		t.Fatalf("after delete %v", got)
	}
	wantStatus(t, w.do(http.MethodDelete, api+"libs-local/dir?properties=team", "admin", ""),
		http.StatusNoContent)
	if props("libs-local/dir/sub/b.txt")["team"] != nil {
		t.Fatal("recursive delete missed an item")
	}

	for _, bad := range []string{
		"?properties=",
		"?properties=k",
		"?properties=bad%2Fkey=1",
		"?properties=k=v&recursive=2",
	} {
		wantStatus(
			t,
			w.do(http.MethodPut, api+"libs-local/top.txt"+bad, "admin", ""),
			http.StatusBadRequest,
		)
	}
	wantStatus(t, w.do(http.MethodDelete, api+"libs-local/top.txt?properties=,", "admin", ""),
		http.StatusBadRequest)
	wantStatus(
		t,
		w.do(http.MethodPut, api+"virt/top.txt?properties=k=v", "admin", ""),
		http.StatusBadRequest,
	)
	wantStatus(
		t,
		w.do(http.MethodPut, api+"up/x?properties=k=v", "admin", ""),
		http.StatusBadRequest,
	)
	wantStatus(
		t,
		w.do(http.MethodPut, api+"libs-local/absent?properties=k=v", "admin", ""),
		http.StatusNotFound,
	)
	// deployer may write but not annotate.
	wantStatus(t, w.do(http.MethodPut, api+"libs-local/top.txt?properties=k=v", "deployer", ""),
		http.StatusForbidden)
	wantStatus(t, w.do(http.MethodPut, api+"libs-local/top.txt?properties=k=v", "", ""),
		http.StatusUnauthorized)
}

// TestStorageRootProperties follows the Terraform provider's
// TestAccItemProperties_repo_only: properties set on a repository itself,
// with no item path, read back, patched and deleted like an item's.
func TestStorageRootProperties(t *testing.T) {
	ctx := context.Background()
	w := storageWorld(t, false)
	const (
		api  = "/artifactory/api/storage/"
		meta = "/artifactory/api/metadata/"
	)
	props := func(path string) map[string]any {
		t.Helper()
		rec := w.do(http.MethodGet, api+path+"?properties=", "admin", "")
		if rec.Code == http.StatusNotFound {
			return nil
		}
		wantStatus(t, rec, http.StatusOK)
		return decodeObject(t, rec.Body.String())["properties"].(map[string]any)
	}
	if props("libs-local") != nil {
		t.Fatal("a new repository's root has properties")
	}

	// The provider's create: recursive=1 sets the root and everything below.
	wantStatus(t, w.do(http.MethodPut,
		api+"libs-local?properties=key1=value1,value2;key2=value3,value4&recursive=1", "admin", ""),
		http.StatusNoContent)
	want := map[string]any{"key1": []any{"value1", "value2"}, "key2": []any{"value3", "value4"}}
	if got := props("libs-local"); !reflect.DeepEqual(got, want) {
		t.Fatalf("root properties %v, want %v", got, want)
	}
	if !reflect.DeepEqual(props("libs-local/dir/sub/b.txt"), want) {
		t.Fatal("recursive=1 on the root missed an item below it")
	}
	rec := w.do(http.MethodGet, api+"libs-local?properties=key2", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	body := decodeObject(t, rec.Body.String())
	if body["uri"] != "https://reg.example"+api+"libs-local" ||
		len(body["properties"].(map[string]any)) != 1 {
		t.Fatalf("named root properties %v", body)
	}
	// The other root views describe the repository as before.
	rec = w.do(http.MethodGet, api+"libs-local", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if info := decodeObject(t, rec.Body.String()); info["path"] != "/" || info["created"] != nil {
		t.Fatalf("root info %v", info)
	}

	// The provider's update: a PATCH that replaces, adds and removes keys
	// on the root alone.
	wantStatus(t, w.do(http.MethodPatch, meta+"libs-local?recursiveProperties=0", "admin",
		`{"props":{"key1":"value1","key3":"value5","key2":null}}`), http.StatusNoContent)
	want = map[string]any{"key1": []any{"value1"}, "key3": []any{"value5"}}
	if got := props("libs-local"); !reflect.DeepEqual(got, want) {
		t.Fatalf("patched root properties %v, want %v", got, want)
	}
	if props("libs-local/top.txt")["key2"] == nil {
		t.Fatal("recursiveProperties=0 on the root reached top.txt")
	}

	// AQL finds the root as Artifactory reports it, and the property search
	// as the repository's storage URI.
	rows, _ := aqlRows(t, w, "admin",
		`items.find({"repo":"libs-local","type":"folder","@key3":"value5"})`)
	if !slices.Equal(rows, []string{"libs-local/./."}) {
		t.Fatalf("AQL rows %v", rows)
	}
	wantURIs(t, w.do(http.MethodGet, "/artifactory/api/search/prop?key3=value5", "admin", ""),
		"libs-local")
	// items.delete passes the root over: it would empty the repository.
	rows, _ = aqlRows(t, w, "admin",
		`items.delete({"repo":"libs-local","type":"folder","@key3":"value5"}).dryRun("false")`)
	if len(rows) != 0 {
		t.Fatalf("items.delete acted on the root: %v", rows)
	}

	// Deleting the repository's content keeps the root's properties.
	wantStatus(t, w.do(http.MethodDelete, "/artifactory/libs-local/", "admin", ""),
		http.StatusNoContent)
	if got := props("libs-local"); !reflect.DeepEqual(got, want) {
		t.Fatalf("root properties after a content delete %v", got)
	}

	// The provider's delete; the last key gone, the view is a 404 again.
	wantStatus(t, w.do(http.MethodDelete, api+"libs-local?properties=key1,key3&recursive=0",
		"admin", ""), http.StatusNoContent)
	if got := props("libs-local"); got != nil {
		t.Fatalf("root properties after delete %v", got)
	}
	// Removing keys from a root that never had any creates nothing.
	wantStatus(t, w.do(http.MethodDelete, api+"other-local?properties=k&recursive=0", "admin", ""),
		http.StatusNoContent)
	if _, err := w.db.Items().Root(ctx, "other-local", false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a delete created a root record: %v", err)
	}

	// A remote repository's root properties are its cache's, read through
	// either key; a virtual repository's root holds none.
	wantStatus(t, w.do(http.MethodPut, api+"up-cache?properties=k=v", "admin", ""),
		http.StatusNoContent)
	for _, key := range []string{"up-cache", "up"} {
		if props(key)["k"] == nil {
			t.Fatalf("%s: root properties missing", key)
		}
	}
	wantStatus(t, w.do(http.MethodPut, api+"up?properties=k=v", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do(http.MethodPut, api+"virt?properties=k=v", "admin", ""),
		http.StatusBadRequest)
	if props("virt") != nil {
		t.Fatal("a virtual repository's root has properties")
	}

	// Annotating the root needs the action on the whole repository; reading
	// or deploying there is not enough.
	grant(t, w, "read-libs", "user", "libs-local", types.ActionRead)
	wantStatus(t, w.do(http.MethodPut, api+"libs-local?properties=k=v", "user", ""),
		http.StatusForbidden)
	wantStatus(t, w.do(http.MethodPatch, meta+"libs-local", "deployer", `{"props":{"k":"v"}}`),
		http.StatusForbidden)
}

// failingRoots fails every read and creation of a repository root's record.
type failingRoots struct{ store.ItemStore }

func (failingRoots) Root(context.Context, string, bool) (types.Item, error) {
	return types.Item{}, errBoom
}

func (failingRoots) EnsureRoot(context.Context, types.Item) (types.Item, error) {
	return types.Item{}, errBoom
}

type failingRootStore struct{ store.Store }

func (s failingRootStore) Items() store.ItemStore { return failingRoots{s.Store.Items()} }

// A store that cannot read or create a root's record answers 500.
func TestStorageRootStoreErrors(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.Store = failingRootStore{c.Store} })
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/artifactory/api/storage/libs-local?properties", ""},
		{http.MethodGet, "/artifactory/api/storage/libs-local", ""},
		{http.MethodPut, "/artifactory/api/storage/libs-local?properties=k=v", ""},
		{http.MethodDelete, "/artifactory/api/storage/libs-local?properties=k", ""},
		{http.MethodPatch, "/artifactory/api/metadata/libs-local", `{"props":{"k":"v"}}`},
	} {
		rec := w.do(r.method, r.path, "admin", r.body)
		if rec.Code != http.StatusInternalServerError ||
			strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

func TestStoragePropertiesOnRemoteCache(t *testing.T) {
	w := storageWorld(t, false)
	if _, err := w.db.Items().Put(context.Background(), types.Item{
		RepoKey:  "up",
		Cached:   true,
		Name:     "cached.txt",
		Size:     1,
		Created:  time.Now(),
		Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/api/storage/up-cache/cached.txt?properties=k=v",
			"admin", ""),
		http.StatusNoContent,
	)
	rec := w.do(
		http.MethodGet,
		"/artifactory/api/storage/up-cache/cached.txt?properties",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	if p := decodeObject(
		t,
		rec.Body.String(),
	); !strings.HasSuffix(
		p["uri"].(string),
		"/up-cache/cached.txt",
	) {
		t.Fatalf("cache uri %v", p["uri"])
	}
}

func TestMetadataPatch(t *testing.T) {
	w := storageWorld(t, false)
	const meta = "/artifactory/api/metadata/"
	patch := func(path, body string) int {
		t.Helper()
		return w.do(
			http.MethodPatch,
			meta+path,
			"admin",
			body,
			"Content-Type",
			"application/json",
		).Code
	}
	if c := patch(
		"libs-local/dir/a.txt",
		`{"props":{"a":"1","b":["x","y"],"c":"z"}}`,
	); c != http.StatusNoContent {
		t.Fatalf("set: %d", c)
	}
	if c := patch(
		"libs-local/dir/a.txt",
		`{"props":{"a":null,"b":[],"c":"new"}}`,
	); c != http.StatusNoContent {
		t.Fatalf("patch: %d", c)
	}
	p, err := w.db.Items().
		Properties(context.Background(), w.item("libs-local", false, "dir/a.txt").ID)
	if err != nil || !reflect.DeepEqual(p, types.Properties{"c": {"new"}}) {
		t.Fatalf("properties %v, %v", p, err)
	}
	if c := patch(
		"libs-local/dir?recursiveProperties=0",
		`{"props":{"f":"1"}}`,
	); c != http.StatusNoContent {
		t.Fatalf("non-recursive: %d", c)
	}
	sub, _ := w.db.Items().
		Properties(context.Background(), w.item("libs-local", false, "dir/sub/b.txt").ID)
	if len(sub) != 0 {
		t.Fatalf("recursiveProperties=0 reached %v", sub)
	}
	for body, want := range map[string]int{
		`{"props":{}}`:           http.StatusBadRequest,
		`{}`:                     http.StatusBadRequest,
		`not json`:               http.StatusBadRequest,
		`{"props":{"k":5}}`:      http.StatusBadRequest,
		`{"props":{"a/b":"1"}}`:  http.StatusBadRequest,
		`{"props":{"k":[1,2]}}`:  http.StatusBadRequest,
		`{"props":{"k":"fine"}}`: http.StatusNoContent,
	} {
		if c := patch("libs-local/top.txt", body); c != want {
			t.Errorf("%s: %d, want %d", body, c, want)
		}
	}
	if c := patch("virt/top.txt", `{"props":{"k":"v"}}`); c != http.StatusBadRequest {
		t.Fatalf("virtual: %d", c)
	}
	rec := w.do(http.MethodPatch, meta+"libs-local/top.txt", "reader", `{"props":{"k":"v"}}`)
	wantStatus(t, rec, http.StatusForbidden)
}

func TestStorageStatsAndLastModified(t *testing.T) {
	w := storageWorld(t, false)
	const api = "/artifactory/api/storage/"
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/libs-local/dir/a.txt", "admin", ""),
		http.StatusOK,
	)

	rec := w.do(http.MethodGet, api+"libs-local/dir/a.txt?stats", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	st := decodeObject(t, rec.Body.String())
	if rec.Header().Get("Content-Type") != mediaStats || st["downloadCount"] != 1.0 ||
		st["lastDownloadedBy"] != "admin" || st["lastDownloaded"].(float64) <= 0 {
		t.Fatalf("stats %v", st)
	}
	rec = w.do(http.MethodGet, api+"libs-local/top.txt?stats", "admin", "")
	if st := decodeObject(
		t,
		rec.Body.String(),
	); st["lastDownloaded"] != 0.0 ||
		st["lastDownloadedBy"] != nil {
		t.Fatalf("stats of an undownloaded file %v", st)
	}
	wantStatus(
		t,
		w.do(http.MethodGet, api+"libs-local/dir?stats", "admin", ""),
		http.StatusBadRequest,
	)

	rec = w.do(http.MethodGet, api+"libs-local/dir?lastModified", "deployer", "")
	wantStatus(t, rec, http.StatusOK)
	lm := decodeObject(t, rec.Body.String())
	if rec.Header().Get("Content-Type") != mediaLastModified ||
		!strings.HasPrefix(
			lm["uri"].(string),
			"https://reg.example/artifactory/api/storage/libs-local/dir/",
		) {
		t.Fatalf("lastModified %v", lm)
	}
	rec = w.do(http.MethodGet, api+"libs-local/top.txt?lastModified", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if decodeObject(t, rec.Body.String())["uri"] != "https://reg.example/artifactory/api/storage/libs-local/top.txt" {
		t.Fatal("lastModified of a file names another item")
	}
	// lastModified needs deploy rights; reader only reads.
	wantStatus(
		t,
		w.do(http.MethodGet, api+"libs-local/dir?lastModified", "reader", ""),
		http.StatusForbidden,
	)
	wantStatus(
		t,
		w.do(http.MethodGet, api+"server-local?lastModified", "admin", ""),
		http.StatusNotFound,
	)
}

func TestStoragePermissions(t *testing.T) {
	w := storageWorld(t, false)
	rec := w.do(
		http.MethodGet,
		"/artifactory/api/storage/libs-local/top.txt?permissions",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	p := decodeObject(t, rec.Body.String())["principals"].(map[string]any)
	if !reflect.DeepEqual(p["users"], map[string]any{"deployer": []any{"r", "w"}}) ||
		!reflect.DeepEqual(p["groups"], map[string]any{"readers": []any{"r"}}) {
		t.Fatalf("principals %v", p)
	}
	rec = w.do(http.MethodGet, "/artifactory/api/storage/up-cache?permissions", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	p = decodeObject(t, rec.Body.String())["principals"].(map[string]any)
	if p["users"].(map[string]any)["anonymous"] == nil {
		t.Fatalf("Any Remote does not show on a remote: %v", p)
	}
	wantStatus(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/storage/libs-local/top.txt?permissions",
			"reader",
			"",
		),
		http.StatusForbidden,
	)
}

func TestParseProperties(t *testing.T) {
	for in, want := range map[string]types.Properties{
		`a=1`:          {"a": {"1"}},
		`a=1,2|b=`:     {"a": {"1", "2"}, "b": {""}},
		`a=x=y;a=z`:    {"a": {"x=y", "z"}},
		`a=p\;q\|r`:    {"a": {"p;q|r"}},
		`a\b=1`:        {"ab": {"1"}},
		`;;a=1;`:       {"a": {"1"}},
		`a=trailing\`:  {"a": {`trailing\`}},
		`=1`:           nil,
		`a=1;missingv`: nil,
	} {
		got, err := parseProperties(in)
		if want == nil {
			if err == nil {
				t.Errorf("%q: parsed %v, want an error", in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, %v, want %v", in, got, err, want)
		}
	}
}
