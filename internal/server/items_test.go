package server

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum here
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// itemWorld is a seeded store and blob store behind the router. Every user's
// password is "pw": admin; reader (in readers, so it reads everything);
// deployer (may deploy to libs-local but not delete); user (no rights).
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

// signingBlobs makes the fs store a presigner for the downloadRedirect test.
type signingBlobs struct{ blob.Store }

func (signingBlobs) PresignGet(_ context.Context, sha string, _ time.Duration) (string, error) {
	return "https://signed.example/" + sha, nil
}

func newItemWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	hash := cheapHash
	for _, u := range []types.User{
		{Name: "reader", PasswordHash: hash, Groups: []string{auth.ReadersGroup}},
		{Name: "deployer", PasswordHash: hash},
		{Name: "user", PasswordHash: hash},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-libs",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "other-local", Class: repo.Local},
		{Key: "server-local", Class: repo.Local, ChecksumPolicyType: repo.ServerGeneratedChecksums},
		{Key: "redirect-local", Class: repo.Local, DownloadRedirect: repo.Flex(true)},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
		{
			Key: "virt", Class: repo.Virtual, Repositories: []string{"missing", "other-local", "libs-local", "up"},
			DefaultDeploymentRepo: "libs-local",
		},
		{Key: "loop", Class: repo.Virtual, Repositories: []string{"loop"}},
		{Key: "virt-nodeploy", Class: repo.Virtual, Repositories: []string{"libs-local"}},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fsBlobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	w := &itemWorld{t: t, db: db, blobs: signingBlobs{fsBlobs}}
	w.h = New(Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: w.blobs, Auth: a,
	})
	return w
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *itemWorld) item(key string, cached bool, p string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(context.Background(), key, cached, p)
	if err != nil {
		w.t.Fatalf("item %s/%s: %v", key, p, err)
	}
	return it
}

func sha1Hex(s string) string {
	b := sha1.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func TestDeployAndDownload(t *testing.T) {
	w := newItemWorld(t, false)
	rec := w.do(
		"PUT",
		"/artifactory/libs-local/org/app/app-1.0.jar;build.name=ci;tags=a,b%5C,c",
		"admin",
		"jar bytes",
		"X-Checksum-Sha1",
		sha1Hex("jar bytes"),
		"X-Checksum",
		sha256Hex("jar bytes"),
	)
	wantStatus(t, rec, http.StatusCreated)
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["repo"] != "libs-local" || created["path"] != "/org/app/app-1.0.jar" ||
		created["size"] != "9" || created["mimeType"] != "application/java-archive" ||
		created["downloadUri"] != "https://reg.example/artifactory/libs-local/org/app/app-1.0.jar" ||
		created["uri"] != "https://reg.example/artifactory/api/storage/libs-local/org/app/app-1.0.jar" ||
		created["createdBy"] != "admin" {
		t.Fatalf("body %v", created)
	}
	sums, _ := created["checksums"].(map[string]any)
	orig, _ := created["originalChecksums"].(map[string]any)
	if sums["sha256"] != sha256Hex("jar bytes") || orig["sha1"] != sha1Hex("jar bytes") ||
		orig["md5"] != nil {
		t.Fatalf("checksums %v original %v", sums, orig)
	}
	it := w.item("libs-local", false, "org/app/app-1.0.jar")
	props, _ := w.db.Items().Properties(context.Background(), it.ID)
	if props["build.name"][0] != "ci" || len(props["tags"]) != 2 || props["tags"][1] != "b,c" {
		t.Fatalf("matrix properties %v", props)
	}

	get := w.do("GET", "/artifactory/libs-local/org/app/app-1.0.jar", "reader", "")
	wantStatus(t, get, http.StatusOK)
	h := get.Header()
	if get.Body.String() != "jar bytes" || h.Get("X-Checksum-Sha1") != sha1Hex("jar bytes") ||
		h.Get("Accept-Ranges") != "bytes" || h.Get("X-Artifactory-Filename") != "app-1.0.jar" ||
		h.Get("Content-Type") != "application/java-archive" || h.Get("ETag") == "" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="app-1.0.jar"`) {
		t.Fatalf("download %q %v", get.Body, h)
	}
	if stats, _ := w.db.Items().Stats(context.Background(), it.ID); stats.DownloadCount != 1 {
		t.Fatalf("downloads %+v", stats)
	}
	head := w.do("HEAD", "/artifactory/libs-local/org/app/app-1.0.jar", "reader", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "9" {
		t.Fatalf("HEAD %d %q %v", head.Code, head.Body, head.Header())
	}
	part := w.do(
		"GET",
		"/artifactory/libs-local/org/app/app-1.0.jar",
		"reader",
		"",
		"Range",
		"bytes=4-",
	)
	if part.Code != http.StatusPartialContent || part.Body.String() != "bytes" {
		t.Fatalf("range %d %q", part.Code, part.Body)
	}
	cond := w.do(
		"GET",
		"/artifactory/libs-local/org/app/app-1.0.jar",
		"reader",
		"",
		"If-None-Match",
		h.Get("ETag"),
	)
	wantStatus(t, cond, http.StatusNotModified)
	w.do("GET", "/artifactory/libs-local/org/app/app-1.0.jar?skipUpdateStats=true", "reader", "")
	if stats, _ := w.db.Items().Stats(context.Background(), it.ID); stats.DownloadCount != 1 {
		t.Fatalf("a range, a 304 or skipUpdateStats was counted: %+v", stats)
	}
}

func TestMatrixResolution(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/a.txt;env=prod", "admin", "a"),
		http.StatusCreated,
	)
	for path, want := range map[string]int{
		"/artifactory/libs-local/a.txt;env=prod":     200,
		"/artifactory/libs-local/a.txt;env=dev,prod": 200,
		"/artifactory/libs-local/a.txt;env=dev":      404,
		"/artifactory/libs-local/a.txt;other=x":      200,
		"/artifactory/libs-local/a.txt;other+=x":     404,
		"/artifactory/libs-local/a.txt;env+=prod":    200,
	} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/b.txt;bad%3Dkey=v", "admin", "b"),
		http.StatusBadRequest,
	)
}

func TestChecksumPolicyAndSidecars(t *testing.T) {
	w := newItemWorld(t, false)
	wrong := strings.Repeat("0", 40)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin", "admin", "x", "X-Checksum-Sha1", wrong),
		http.StatusConflict,
	)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin", "admin", "x", "X-Checksum-Sha1", "zz"),
		http.StatusBadRequest,
	)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/server-local/x.bin", "admin", "x", "X-Checksum-Sha1", wrong),
		http.StatusCreated,
	)
	if it := w.item("server-local", false, "x.bin"); it.Original.SHA1 != wrong {
		t.Fatalf("server policy did not record the declared checksum: %+v", it)
	}
	// X-Checksum names its algorithm by length.
	wantStatus(
		t,
		w.do(
			"PUT",
			"/artifactory/libs-local/md5.bin",
			"admin",
			"x",
			"X-Checksum",
			strings.Repeat("1", 32),
		),
		http.StatusConflict,
	)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/x.bin", "admin", "x"), http.StatusCreated)

	// Sidecars are served from metadata and never stored.
	for ext, want := range map[string]string{".sha1": sha1Hex("x"), ".sha256": sha256Hex("x")} {
		rec := w.do("GET", "/artifactory/libs-local/x.bin"+ext, "admin", "")
		if rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("%s: %d %q", ext, rec.Code, rec.Body)
		}
	}
	if rec := w.do(
		"HEAD",
		"/artifactory/libs-local/x.bin.md5",
		"admin",
		"",
	); rec.Code != 200 ||
		rec.Body.Len() != 0 {
		t.Fatalf("HEAD sidecar %d %q", rec.Code, rec.Body)
	}
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/x.bin.sha512", "admin", ""),
		http.StatusNotFound,
	)
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/none.sha1", "admin", ""),
		http.StatusNotFound,
	)

	// A sidecar upload records the client's checksum, checked by policy.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin.sha1", "admin", sha1Hex("x")+"  x.bin\n"),
		http.StatusCreated,
	)
	if it := w.item("libs-local", false, "x.bin"); it.Original.SHA1 != sha1Hex("x") {
		t.Fatalf("sidecar not recorded: %+v", it)
	}
	if _, err := w.db.Items().
		Get(context.Background(), "libs-local", false, "x.bin.sha1"); err == nil {
		t.Fatal("the sidecar was stored as an item")
	}
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin.md5", "admin", strings.Repeat("2", 32)),
		http.StatusConflict,
	)
	// Without its file, a sidecar name is an ordinary file.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/lonely.md5", "admin", "data"),
		http.StatusCreated,
	)
}

func TestChecksumDeploy(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/first.bin", "admin", "shared"),
		http.StatusCreated,
	)
	rec := w.do("PUT", "/artifactory/other-local/copy.bin;p=v", "admin", "",
		"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex("shared"))
	wantStatus(t, rec, http.StatusCreated)
	if got := w.do(
		"GET",
		"/artifactory/other-local/copy.bin",
		"admin",
		"",
	); got.Body.String() != "shared" {
		t.Fatalf("checksum-deployed content %q", got.Body)
	}
	wantStatus(t, w.do(
		"PUT",
		"/artifactory/libs-local/miss.bin",
		"admin",
		"",
		"X-Checksum-Deploy",
		"true",
		"X-Checksum-Sha1",
		sha1Hex("never stored"),
	), http.StatusNotFound)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/none.bin", "admin", "", "X-Checksum-Deploy", "true"),
		http.StatusBadRequest,
	)
}

func TestFoldersAndListing(t *testing.T) {
	w := newItemWorld(t, false)
	rec := w.do("PUT", "/artifactory/libs-local/a/b/;owner=me", "admin", "")
	wantStatus(t, rec, http.StatusCreated)
	if strings.Contains(rec.Body.String(), "downloadUri") ||
		!w.item("libs-local", false, "a/b").Folder {
		t.Fatalf("folder create %s", rec.Body)
	}
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/a/b/f%20one.txt", "admin", "f"),
		http.StatusCreated,
	)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a", "admin", "x"), http.StatusConflict)
	list := w.do("GET", "/artifactory/libs-local/a/b/", "reader", "")
	if list.Code != 200 ||
		!strings.Contains(list.Body.String(), `<a href="f%20one.txt">f one.txt</a>`) {
		t.Fatalf("listing %d %s", list.Code, list.Body)
	}
	root := w.do("GET", "/artifactory/libs-local/", "reader", "")
	if root.Code != 200 || !strings.Contains(root.Body.String(), `<a href="a/">a/</a>`) {
		t.Fatalf("root listing %d %s", root.Code, root.Body)
	}
	if head := w.do(
		"HEAD",
		"/artifactory/libs-local/a",
		"reader",
		"",
	); head.Code != 200 ||
		head.Body.Len() != 0 {
		t.Fatalf("HEAD folder %d", head.Code)
	}
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/", "admin", "x"), http.StatusBadRequest)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/a/../b", "admin", "x"),
		http.StatusBadRequest,
	)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.zip", "admin", "zip", "X-Explode-Archive", "true"),
		http.StatusBadRequest,
	)
}

func TestRepositoryClasses(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	wantStatus(t, w.do("PUT", "/artifactory/up/x", "admin", "x"), http.StatusMethodNotAllowed)
	wantStatus(t, w.do("PUT", "/artifactory/up-cache/x", "admin", "x"), http.StatusMethodNotAllowed)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/virt-nodeploy/x", "admin", "x"),
		http.StatusMethodNotAllowed,
	)
	wantStatus(t, w.do("PUT", "/artifactory/missing/x", "admin", "x"), http.StatusNotFound)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local-cache/x", "admin", "x"), http.StatusNotFound)

	// A virtual repository deploys into its default deployment repository.
	rec := w.do("PUT", "/artifactory/virt/via-virtual.txt", "admin", "v")
	wantStatus(t, rec, http.StatusCreated)
	if !strings.Contains(rec.Body.String(), `"repo":"libs-local"`) {
		t.Fatalf("virtual deploy reported %s", rec.Body)
	}

	// A remote repository serves its cache; nothing is fetched upstream yet.
	if _, err := w.db.Items().Put(ctx, types.Item{
		RepoKey: "up",
		Cached:  true,
		Name:    "cached.txt",
		Size:    1,
		Actual: types.Checksums{
			SHA256: sha256Hex("c"),
			SHA1:   sha1Hex("c"),
			MD5:    "4a8a08f09d37b73795649038408b5f33",
		},
	}); err != nil {
		t.Fatal(err)
	}
	u, err := w.blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = u.Append(ctx, strings.NewReader("c"))
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Blobs().Ref(ctx, types.Blob{Checksums: types.Checksums{
		SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}, Size: 1}, 1); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/artifactory/up/cached.txt", "/artifactory/up-cache/cached.txt", "/artifactory/virt/cached.txt"} {
		if rec := w.do("GET", p, "admin", ""); rec.Code != 200 || rec.Body.String() != "c" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
	// Members resolve in order; the virtual's own deploy is found too.
	if rec := w.do(
		"GET",
		"/artifactory/virt/via-virtual.txt",
		"reader",
		"",
	); rec.Body.String() != "v" {
		t.Fatalf("virtual resolution %d %q", rec.Code, rec.Body)
	}
	wantStatus(t, w.do("GET", "/artifactory/virt/absent", "admin", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", "/artifactory/loop/absent", "admin", ""), http.StatusNotFound)
	wantStatus(
		t,
		w.do("DELETE", "/artifactory/virt/via-virtual.txt", "admin", ""),
		http.StatusMethodNotAllowed,
	)
	wantStatus(t, w.do("DELETE", "/artifactory/up/cached.txt", "admin", ""), http.StatusNoContent)
}

func TestDownloadRedirect(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/redirect-local/r.bin", "admin", "redirected"),
		http.StatusCreated,
	)
	rec := w.do("GET", "/artifactory/redirect-local/r.bin", "admin", "")
	if rec.Code != http.StatusFound ||
		rec.Header().Get("Location") != "https://signed.example/"+sha256Hex("redirected") {
		t.Fatalf("redirect %d %v", rec.Code, rec.Header())
	}
}

func TestDeleteItem(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/d/one.txt", "admin", "1"),
		http.StatusCreated,
	)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/d", "deployer", ""), http.StatusForbidden)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/d", "admin", ""), http.StatusNoContent)
	if entries, _, _ := w.db.Trash().List(ctx, "libs-local", "", store.Page{}); len(entries) != 2 {
		t.Fatalf("trash %v", entries)
	}
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/d/one.txt", "admin", ""),
		http.StatusNotFound,
	)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/d", "admin", ""), http.StatusNotFound)
	wantStatus(t, w.do("DELETE", "/artifactory/missing/x", "admin", ""), http.StatusNotFound)
}

func TestItemPermissions(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/p.txt", "", "x"), http.StatusUnauthorized)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/p.txt", "user", "x"), http.StatusForbidden)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/p.txt", "reader", "x"), http.StatusForbidden)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/other-local/p.txt", "deployer", "x"),
		http.StatusForbidden,
	)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/p.txt", "deployer", "x"), http.StatusCreated)
	// Overwriting needs delete as well as deploy.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/p.txt", "deployer", "y"),
		http.StatusForbidden,
	)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/p.txt", "user", ""), http.StatusForbidden)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/p.txt", "deployer", ""), http.StatusOK)
	wantStatus(t, w.do("DELETE", "/artifactory/libs-local/p.txt", "user", ""), http.StatusForbidden)

	anon := newItemWorld(t, true)
	wantStatus(t, anon.do("PUT", "/artifactory/libs-local/a.txt", "admin", "x"), http.StatusCreated)
	wantStatus(t, anon.do("GET", "/artifactory/libs-local/a.txt", "", ""), http.StatusUnauthorized)
	wantStatus(t, anon.do("PUT", "/artifactory/libs-local/b.txt", "", "x"), http.StatusUnauthorized)
	// Any Remote lets anonymous read remote repositories.
	wantStatus(t, anon.do("GET", "/artifactory/up/absent", "", ""), http.StatusNotFound)
}

func TestSearchAQL(t *testing.T) {
	w := newItemWorld(t, false)
	for _, p := range []string{"libs-local/a/x.jar", "libs-local/a/y.jar", "other-local/z.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	query := `items.find({"name":{"$match":"*.jar"}}).include("repo","path","name").sort({"$asc":["name"]})`
	rec := w.do("POST", "/artifactory/api/search/aql", "admin", query, "Content-Type", "text/plain")
	wantStatus(t, rec, http.StatusOK)
	var res struct {
		Results []map[string]any `json:"results"`
		Range   map[string]any   `json:"range"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 3 || res.Results[0]["name"] != "x.jar" ||
		res.Range["total"] != float64(3) {
		t.Fatalf("admin search %s", rec.Body)
	}
	// deployer reads libs-local only.
	rec = w.do("POST", "/artifactory/api/search/aql", "deployer", query)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.Results) != 2 {
		t.Fatalf("filtered search %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/search/aql", "deployer",
		`items.find({"name":"x.jar"}).include("name")`), http.StatusBadRequest)
	wantStatus(
		t,
		w.do("POST", "/artifactory/api/search/aql", "admin", `items.find(`),
		http.StatusBadRequest,
	)
	tooLong := w.do("POST", "/artifactory/api/search/aql", "admin",
		`items.find({"name":"`+strings.Repeat("x", 6001)+`"})`)
	if tooLong.Code != 400 || !strings.Contains(tooLong.Body.String(), "AQL query is too long") {
		t.Fatalf("too long %d %s", tooLong.Code, tooLong.Body)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/search/aql", "", query), http.StatusUnauthorized)

	orig := aqlHardLimit
	aqlHardLimit = 2
	t.Cleanup(func() { aqlHardLimit = orig })
	rec = w.do("POST", "/artifactory/api/search/aql", "admin", query)
	if !strings.Contains(rec.Body.String(), `"notification":`) {
		t.Fatalf("a capped search gave no notification: %s", rec.Body)
	}
}

func TestUploadsConfig(t *testing.T) {
	w := newItemWorld(t, false)
	rec := w.do("GET", "/artifactory/api/v1/uploads/config", "admin", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"supported":false}` {
		t.Fatalf("uploads config %d %s", rec.Code, rec.Body)
	}
}
