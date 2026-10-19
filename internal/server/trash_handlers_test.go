package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestTrashListingAndRestore(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	if rec := do(
		t,
		h,
		http.MethodDelete,
		"/artifactory/libs-local/fixture/del.txt",
		admin,
		"",
	); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	tc := "/artifactory/api/storage/auto-trashcan"
	root := decodeObject(t, do(t, h, http.MethodGet, tc, admin, "").Body.String())
	if kids := root["children"].([]any); len(kids) != 1 || //nolint:forcetypeassert // fixture
		kids[0].(map[string]any)["uri"] != "/libs-local" { //nolint:forcetypeassert // fixture
		t.Fatalf("root %v", root)
	}
	folder := decodeObject(
		t,
		do(t, h, http.MethodGet, tc+"/libs-local/fixture", admin, "").Body.String(),
	)
	if folder["path"] != "/libs-local/fixture" ||
		len(folder["children"].([]any)) != 1 { //nolint:forcetypeassert // fixture
		t.Fatalf("folder %v", folder)
	}
	file := decodeObject(
		t,
		do(t, h, http.MethodGet, tc+"/libs-local/fixture/del.txt", admin, "").Body.String(),
	)
	props := file["properties"].(map[string]any) //nolint:forcetypeassert // fixture
	if file["size"] != "7" ||
		props["trash.deletedBy"].([]any)[0] != "admin" || //nolint:forcetypeassert // fixture
		props["trash.originalPath"].([]any)[0] != "fixture/del.txt" { //nolint:forcetypeassert // fixture
		t.Fatalf("file %v", file)
	}
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/trash/restore/libs-local/fixture/del.txt?to=libs-local",
		admin,
		"",
	)
	if rec.Code != http.StatusAccepted || rec.Body.String() != restoredMessage ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("restore: %d %q %s", rec.Code, rec.Body, rec.Header())
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/libs-local/fixture/del.txt",
		admin,
		"",
	); rec.Body.String() != "fixture" {
		t.Fatalf("restored content: %d %q", rec.Code, rec.Body)
	}
	// The generated one-segment restore route answers the same way.
	do(t, h, http.MethodDelete, "/artifactory/libs-local/fixture/del.txt", admin, "")
	if rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/trash/restore/libs-local?to=libs-local",
		admin,
		"",
	); rec.Code != http.StatusAccepted ||
		rec.Body.String() != restoredMessage {
		t.Fatalf("one-segment restore: %d %q", rec.Code, rec.Body)
	}
}

// TestTrashListingFiltered: a caller that is not an administrator sees what
// was trashed from the repositories it holds deploy or manage on, judged at
// each item's original path, and nothing else (#439).
func TestTrashListingFiltered(t *testing.T) {
	w := newItemWorld(t, true)
	ctx := context.Background()
	grant := func(name, user, repoKey string, a types.Actions, include, exclude []string) {
		t.Helper()
		if err := w.db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: a}},
				Targets: map[string]types.Target{
					repoKey: {Include: include, Exclude: exclude},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		"libs-local/a/one.txt", "libs-local/a/secret/two.txt", "libs-local/b/three.txt",
		"other-local/o.txt", "server-local/s.txt",
	} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	for _, p := range []string{
		"libs-local/a", "libs-local/b", "other-local/o.txt", "server-local/s.txt",
	} {
		wantStatus(t, w.do("DELETE", "/artifactory/"+p, "admin", ""), http.StatusNoContent)
	}
	const tc = "/artifactory/api/storage/auto-trashcan"
	list := func(as, path string, want ...string) {
		t.Helper()
		rec := w.do("GET", tc+path, as, "")
		wantStatus(t, rec, http.StatusOK)
		kids := decodeObject(t, rec.Body.String())["children"].([]any) //nolint:forcetypeassert // fixture
		var got []string
		for _, c := range kids {
			child := c.(map[string]any)              //nolint:forcetypeassert // fixture
			got = append(got, child["uri"].(string)) //nolint:forcetypeassert // fixture
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s lists %q as %v, want %v", as, tc+path, got, want)
		}
	}
	refused := func(as string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			wantStatus(t, w.do("GET", tc+p, as, ""), http.StatusForbidden)
		}
	}

	// Without deploy or manage anywhere the trash can is refused; read is
	// not enough.
	refused("user", "", "/libs-local")
	grant("read-all", "user", types.TargetAnyLocal, types.ActionRead, []string{"**"}, nil)
	refused("user", "", "/libs-local/a/one.txt")

	// Deploy on a/ in libs-local, but not a/secret/.
	grant("deploy-a", "user", "libs-local", types.ActionWrite,
		[]string{"a/**"}, []string{"a/secret/**"})
	list("user", "", "/libs-local")
	list("user", "/libs-local", "/a")
	list("user", "/libs-local/a", "/one.txt")
	file := decodeObject(t, w.do("GET", tc+"/libs-local/a/one.txt", "user", "").Body.String())
	if file["path"] != "/libs-local/a/one.txt" {
		t.Fatalf("file info %v", file)
	}
	// Elsewhere it is refused before the trash is read, so a refusal does
	// not tell whether anything is trashed there.
	refused("user", "/libs-local/a/secret", "/libs-local/a/secret/two.txt",
		"/libs-local/b", "/libs-local/b/absent", "/other-local", "/other-local/o.txt",
		"/gone-repo", "/gone-repo/x")
	// Where it does see, nothing trashed is not found.
	wantStatus(t, w.do("GET", tc+"/libs-local/a/absent", "user", ""), http.StatusNotFound)

	// Manage without deploy is enough too.
	grant("manage-other", "user", "other-local", types.ActionManage, []string{"**"}, nil)
	list("user", "", "/libs-local", "/other-local")
	list("user", "/other-local", "/o.txt")

	// A pattern naming a path below s.txt lets the folder check through,
	// but the trashed file needs the exact right, and its repository shows
	// only for an item the caller sees.
	grant("below-s", "user", "server-local", types.ActionWrite, []string{"s.txt/x"}, nil)
	refused("user", "/server-local/s.txt")
	wantStatus(t, w.do("GET", tc+"/server-local", "user", ""), http.StatusNotFound)
	list("user", "", "/libs-local", "/other-local")

	// Each caller sees its own share; an administrator sees it all.
	list("deployer", "", "/libs-local")
	list("deployer", "/libs-local", "/a", "/b")
	list("admin", "", "/libs-local", "/other-local", "/server-local")

	// Anonymous is told to sign in, even holding deploy.
	grant("anon-deploy", auth.AnonymousUser, "libs-local", types.ActionWrite, []string{"**"}, nil)
	for _, p := range []string{"", "/libs-local", "/libs-local/b/three.txt"} {
		wantStatus(t, w.do("GET", tc+p, "", ""), http.StatusUnauthorized)
	}

	// Once a repository is gone, only an administrator sees its trash.
	if err := w.db.Repos().Delete(ctx, "other-local"); err != nil {
		t.Fatal(err)
	}
	list("user", "", "/libs-local")
	refused("user", "/other-local", "/other-local/o.txt")
	list("admin", "/other-local", "/o.txt")
}

// A failure reading the repositories a listing is judged by is a server
// error, not a refusal or an empty listing.
func TestTrashListingReadFailure(t *testing.T) {
	fail := map[string]bool{}
	w := newItemWorld(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = faultyStore{Store: db, fail: fail}
	})
	const tc = "/artifactory/api/storage/auto-trashcan"
	fail["list"] = true
	wantStatus(t, w.do("GET", tc, "deployer", ""), http.StatusInternalServerError)
	// An administrator needs no repository to see the trash can.
	wantStatus(t, w.do("GET", tc, "admin", ""), http.StatusOK)
	fail["get:libs-local"] = true
	wantStatus(t, w.do("GET", tc+"/libs-local", "deployer", ""), http.StatusInternalServerError)
}

// TestTrashRestoreNeedsOrigin: manage on the destination is not enough to
// restore; the caller also needs delete or manage where the items were
// trashed from, at every restored path (#380).
func TestTrashRestoreNeedsOrigin(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	grant := func(name, repoKey string, a types.Actions, include, exclude []string) {
		t.Helper()
		if err := w.db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": a}},
				Targets: map[string]types.Target{
					repoKey: {Include: include, Exclude: exclude},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"a/one.txt", "a/secret/two.txt", "b/three.txt"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	for _, p := range []string{"a", "b"} {
		wantStatus(t, w.do("DELETE", "/artifactory/libs-local/"+p, "admin", ""),
			http.StatusNoContent)
	}
	trashed := func() int {
		t.Helper()
		entries, _, err := w.db.Trash().List(ctx, "libs-local", "", store.Page{})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	restore := func(from string) *httptest.ResponseRecorder {
		t.Helper()
		return w.do("POST", "/artifactory/api/trash/restore/"+from+"?to=other-local", "user", "")
	}
	grant("restore-dest", "other-local", types.ActionManage, []string{"**"}, nil)

	// Manage on the destination alone: refused, and nothing moves.
	wantStatus(t, restore("libs-local/a"), http.StatusBadRequest)
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/one.txt", "admin", ""),
		http.StatusNotFound)
	if n := trashed(); n != 6 {
		t.Fatalf("a refused restore keeps the trash: %d entries", n)
	}
	// Refused before the trash is read: no hint whether anything is there.
	wantStatus(t, restore("libs-local/absent"), http.StatusBadRequest)

	// Delete on a/ but not a/secret/: a/ as a whole is refused, with
	// nothing restored, while the permitted file alone is restored.
	grant("restore-a", "libs-local", types.ActionDelete,
		[]string{"a/**"}, []string{"a/secret/**"})
	rec := restore("libs-local/a")
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "everything trashed at 'libs-local/a'") {
		t.Fatalf("refusal: %s", rec.Body)
	}
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/one.txt", "admin", ""),
		http.StatusNotFound)
	wantStatus(t, restore("libs-local/a/one.txt"), http.StatusAccepted)
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/one.txt", "admin", ""), http.StatusOK)
	// Permitted, with nothing trashed there: now the caller may know.
	wantStatus(t, restore("libs-local/a/absent"), http.StatusNotFound)
	// b/ is outside the grant.
	wantStatus(t, restore("libs-local/b"), http.StatusBadRequest)

	// Manage without delete on the origin is enough too.
	grant("restore-secret", "libs-local", types.ActionManage, []string{"a/secret/**"}, nil)
	wantStatus(t, restore("libs-local/a/secret"), http.StatusAccepted)
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/secret/two.txt", "admin", ""),
		http.StatusOK)

	// Once the original repository is gone, only an admin restores from it.
	if err := w.db.Repos().Delete(ctx, "libs-local"); err != nil {
		t.Fatal(err)
	}
	grant("restore-any", "ANY LOCAL", types.ActionDelete|types.ActionManage, []string{"**"}, nil)
	wantStatus(t, restore("libs-local/b"), http.StatusBadRequest)
	wantStatus(t, w.do("POST", "/artifactory/api/trash/restore/libs-local/b?to=other-local",
		"admin", ""), http.StatusAccepted)
	wantStatus(t, w.do("GET", "/artifactory/other-local/b/three.txt", "admin", ""), http.StatusOK)
}

// A failure reading the original repository is a server error, not a
// refusal or a restore.
func TestTrashOriginReadFailure(t *testing.T) {
	fail := map[string]bool{}
	w := newItemWorld(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = faultyStore{Store: db, fail: fail}
	})
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/f.txt", "admin", "f"), http.StatusCreated)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/f.txt", "admin", ""),
		http.StatusNoContent)
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "restore-dest",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionManage}},
			Targets: map[string]types.Target{"other-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	fail["get:libs-local"] = true
	wantStatus(t, w.do("POST", "/artifactory/api/trash/restore/libs-local/f.txt?to=other-local",
		"user", ""), http.StatusInternalServerError)
	wantStatus(t, w.do("DELETE", "/artifactory/api/trash/clean/libs-local/f.txt", "user", ""),
		http.StatusInternalServerError)
}

func TestStorageInfoBody(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("admin", "pw"), "")
	info := decodeObject(t, rec.Body.String())
	bin := info["binariesSummary"].(map[string]any) //nolint:forcetypeassert // fixture
	rows := info["repositoriesSummaryList"].([]any) //nolint:forcetypeassert // fixture
	last := rows[len(rows)-1].(map[string]any)      //nolint:forcetypeassert // fixture
	// Relations rather than counts, so files added to the shared fixture by
	// other features do not break this test: the TOTAL row agrees with the
	// binaries summary, and the fixture's identical contents are stored once.
	artifacts, _ := strconv.Atoi(bin["artifactsCount"].(string)) //nolint:forcetypeassert // fixture
	binaries, _ := strconv.Atoi(bin["binariesCount"].(string))   //nolint:forcetypeassert // fixture
	if last["repoKey"] != "TOTAL" || last["filesCount"] != float64(artifacts) ||
		binaries < 1 || binaries >= artifacts ||
		!strings.HasSuffix(bin["optimization"].(string), "%") { //nolint:forcetypeassert // fixture
		t.Fatalf("summary %s", rec.Body)
	}
	found := false
	for _, r := range rows {
		if r.(map[string]any)["repoKey"] == "remote-private-cache" { //nolint:forcetypeassert // fixture
			found = true
		}
	}
	if !found {
		t.Fatalf("a remote repository is listed under its cache alias: %s", rec.Body)
	}
}

func TestZapMessage(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/zap/remote-private-cache",
		basic("admin", "pw"),
		"",
	)
	if decodeObject(t, rec.Body.String())["Message"] != "Completed zapping cache in path remote-private-cache" {
		t.Fatalf("zap: %d %s", rec.Code, rec.Body)
	}
}

func TestConfiguredCollector(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	c, err := gc.New(db, nil, gc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := artifactoryService{cfg: Config{Store: db, GC: c}}
	if got, err := s.collector(); err != nil || got != c {
		t.Fatalf("collector %p, %v", got, err)
	}
	if _, err := (artifactoryService{}).collector(); err == nil {
		t.Fatal("no store, no collector")
	}
}

func TestStorageFormatting(t *testing.T) {
	// 7.x keeps two decimals ("10.00 MB", "47.10 KB" in the captures of
	// research 15) and groups thousands ("1,004.64 KB" in jfrog-cli-core's
	// parser test); TB is its largest unit.
	for n, want := range map[int64]string{
		0: "0 bytes", 101: "101 bytes", 1023: "1023 bytes", 1024: "1.00 KB", 1536: "1.50 KB",
		48230: "47.10 KB", 1028751: "1,004.64 KB", 10 << 20: "10.00 MB",
		3_736_621_547: "3.48 GB", 5 << 40: "5.00 TB", 5 << 50: "5,120.00 TB",
		3 << 60: "3,145,728.00 TB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 123: "123", 125726: "125,726", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
	for want, got := range map[string]string{
		"33.33%": percent(1, 3), "N/A": percent(1, 0), "100%": percent(4, 4),
		"0.1%": percent(1, 1000), "0%": percent(1, 100000), "20.46%": percent(2046, 10000),
	} {
		if got != want {
			t.Errorf("percent %q, want %q", got, want)
		}
	}
	// A volume that is not measured has no space fields, as 7.x's object
	// stores have none.
	fs := storageSummary(usage.Summary{Location: usage.Location{Type: "file-system"}}).
		FileStoreSummary
	if fs.TotalSpace != nil || fs.UsedSpace != nil || fs.FreeSpace != nil {
		t.Fatalf("file store %+v", fs)
	}
}
