package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

// TestAccTrashStorageLastModified: a trashed file reports its own time and a
// folder the newest item below it.
func TestAccTrashStorageLastModified(t *testing.T) {
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
	if got := last(
		"admin",
		"/libs-local/a/old.txt",
	); got["lastModified"] != httpkit.FormatTime(
		t0,
	) {
		t.Fatalf("file %v", got)
	}
	if got := last("admin", "/libs-local"); got["uri"] !=
		"https://reg.example"+trashURL+"/libs-local/a/b/new.txt" {
		t.Fatalf("folder %v", got)
	}
	if got := last("admin", ""); got["lastModified"] != httpkit.FormatTime(t0.Add(2*time.Hour)) {
		t.Fatalf("root %v", got)
	}
	// What the caller does not see does not count, though it is newer.
	if got := last("deployer", ""); got["lastModified"] != httpkit.FormatTime(t0.Add(time.Hour)) {
		t.Fatalf("deployer's root %v", got)
	}
}

// TestAccTrashStorageViewsFiltered: in every view, a caller that is not an
// administrator sees only what was trashed from repositories where it holds
// deploy or manage, at each item's original path (#455, #439).
func TestAccTrashStorageViewsFiltered(t *testing.T) {
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
	if got := decodeObject(t, rec.Body.String()); got["lastModified"] != httpkit.FormatTime(t0) {
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
