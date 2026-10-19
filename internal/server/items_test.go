package server

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum here
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

// signingBlobs makes a blob store a signer of the routes it lists, with
// the signing options readable back from the URL. err fails every signature.
type signingBlobs struct {
	blob.Store
	routes []blob.Via
	err    error
}

// storageSigner signs storage URLs only, as an object store without a CDN.
func storageSigner(st blob.Store) signingBlobs {
	return signingBlobs{Store: st, routes: []blob.Via{blob.ViaStorage}}
}

func (b signingBlobs) Signs(via blob.Via) bool { return slices.Contains(b.routes, via) }

func (b signingBlobs) SignURL(_ context.Context, sha string, o blob.SignOptions) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	q := url.Values{"ttl": {o.TTL.String()}, "name": {o.Filename}, "type": {o.ContentType}}
	return "https://" + o.Via.String() + ".example/" + sha + "?" + q.Encode(), nil
}

// anonymousRemotes is the permission newItemWorld gives anonymous when
// anonymous access is on: read on every remote repository.
const anonymousRemotes = "anonymous-remotes"

// newItemWorld builds the world; tune adjusts the router's configuration,
// for tests that need another blob store or a logger they can read.
func newItemWorld(t *testing.T, anonymous bool, tune ...func(*Config)) *itemWorld {
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
		// The seeded Any Remote grants anonymous nothing (#383), so anonymous
		// reads remotes through a permission of its own, as JFrog advises.
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: anonymousRemotes,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					auth.AnonymousUser: types.ActionRead,
				}},
				Targets: map[string]types.Target{types.TargetAnyRemote: {Include: []string{"**"}}},
			}},
		}); err != nil {
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
	cfg := Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: storageSigner(fsBlobs), Auth: a,
	}
	for _, f := range tune {
		f(&cfg)
	}
	w := &itemWorld{t: t, db: db, blobs: cfg.Blobs}
	w.h = New(cfg)
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

func sha512Hex(s string) string {
	b := sha512.Sum512([]byte(s))
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
		created["uri"] != created["downloadUri"] ||
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

	// Sidecars are served from metadata and never stored. Under
	// client-checksums each serves the client's checksum, and the client
	// sent none but the SHA-256, which every deploy records as its own.
	sidecars := func(repoKey string, want map[string]string) {
		t.Helper()
		for ext, value := range want {
			rec := w.do("GET", "/artifactory/"+repoKey+"/x.bin"+ext, "admin", "")
			switch {
			case value == "" && (rec.Code != http.StatusNotFound ||
				!strings.Contains(rec.Body.String(), "Checksum not found for x.bin")):
				t.Errorf("%s %s: %d %q, want 404", repoKey, ext, rec.Code, rec.Body)
			case value != "" && (rec.Code != http.StatusOK || rec.Body.String() != value):
				t.Errorf("%s %s: %d %q, want %q", repoKey, ext, rec.Code, rec.Body, value)
			}
		}
	}
	sidecars("libs-local", map[string]string{
		".sha1": "", ".md5": "", ".sha512": "", ".sha256": sha256Hex("x"),
	})
	wantStatus(t, w.do("HEAD", "/artifactory/libs-local/x.bin.md5", "admin", ""),
		http.StatusNotFound)
	// Under server-generated-checksums the computed checksum is served, not
	// the declared one.
	sidecars("server-local", map[string]string{
		".sha1": sha1Hex("x"), ".md5": md5Hex("x"), ".sha256": sha256Hex("x"),
		".sha512": sha512Hex("x"),
	})
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/none.sha1", "admin", ""),
		http.StatusNotFound,
	)

	// A sidecar upload records the client's checksum, checked by policy,
	// and fixes the missing checksum: the sidecar is served from then on.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin.sha1", "admin", sha1Hex("x")+"  x.bin\n"),
		http.StatusCreated,
	)
	if it := w.item("libs-local", false, "x.bin"); it.Original.SHA1 != sha1Hex("x") {
		t.Fatalf("sidecar not recorded: %+v", it)
	}
	sidecars("libs-local", map[string]string{".sha1": sha1Hex("x"), ".md5": ""})
	if rec := w.do(
		"HEAD",
		"/artifactory/libs-local/x.bin.sha1",
		"admin",
		"",
	); rec.Code != 200 ||
		rec.Body.Len() != 0 {
		t.Fatalf("HEAD sidecar %d %q", rec.Code, rec.Body)
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
	// SHA-512 files, which Gradle uploads, are checked like the others.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin.sha512", "admin", sha512Hex("x")),
		http.StatusCreated,
	)
	if it := w.item("libs-local", false, "x.bin"); it.Original.SHA512 != sha512Hex("x") {
		t.Fatalf("sha512 sidecar not recorded: %+v", it)
	}
	sidecars("libs-local", map[string]string{".sha512": sha512Hex("x")})
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/x.bin.sha512", "admin", strings.Repeat("3", 128)),
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
	// The found content's checksums become the client's, so every sidecar
	// is served under client-checksums, though the deploy sent only a SHA-1
	// and the first deploy sent none.
	for ext, want := range map[string]string{
		".sha1": sha1Hex("shared"), ".md5": md5Hex("shared"), ".sha256": sha256Hex("shared"),
		".sha512": sha512Hex("shared"),
	} {
		got := w.do("GET", "/artifactory/other-local/copy.bin"+ext, "admin", "")
		if got.Code != http.StatusOK || got.Body.String() != want {
			t.Errorf("%s: %d %q", ext, got.Code, got.Body)
		}
	}
	rec = w.do("GET", "/artifactory/api/storage/other-local/copy.bin", "admin", "")
	var info struct {
		Original map[string]string `json:"originalChecksums"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil ||
		info.Original["md5"] != md5Hex("shared") || info.Original["sha256"] != sha256Hex("shared") {
		t.Errorf("storage info %s %v", rec.Body, err)
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

// TestChecksumDeployNeedsRead (#379): a checksum deploy reuses stored
// content only when the caller may read a file that holds it. Anything else
// is the 404 of a miss, body and all, so a checksum learned elsewhere
// neither copies the content nor confirms that the server holds it.
func TestChecksumDeployNeedsRead(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	byChecksum := func(p, as, content string) *httptest.ResponseRecorder {
		return w.do("PUT", "/artifactory/"+p, as, "",
			"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex(content))
	}
	wantStatus(
		t,
		w.do("PUT", "/artifactory/other-local/secret.bin", "admin", "secret"),
		http.StatusCreated,
	)
	// deployer may deploy to libs-local, and read there, but not in
	// other-local.
	stolen := byChecksum("libs-local/stolen.bin", "deployer", "secret")
	wantStatus(t, stolen, http.StatusNotFound)
	miss := byChecksum("libs-local/miss.bin", "deployer", "never stored")
	if stolen.Body.String() != miss.Body.String() {
		t.Fatalf("an unreadable hit %q differs from a miss %q", stolen.Body, miss.Body)
	}
	if _, err := w.db.Items().
		Get(ctx, "libs-local", false, "stolen.bin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused checksum deploy stored the item: %v", err)
	}
	// One file it may read is enough, whatever other repositories hold.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/seen/secret.bin", "admin", "secret"),
		http.StatusCreated,
	)
	wantStatus(t, byChecksum("libs-local/reused.bin", "deployer", "secret"), http.StatusCreated)
	got := w.do("GET", "/artifactory/libs-local/reused.bin", "deployer", "")
	if got.Body.String() != "secret" {
		t.Fatalf("reused content %q", got.Body)
	}
	// A trashed file is not one: once both readable copies are deleted the
	// content is out of reach again, though the trash can still holds it.
	for _, p := range []string{"seen/secret.bin", "reused.bin"} {
		deleted := w.do("DELETE", "/artifactory/libs-local/"+p, "admin", "")
		wantStatus(t, deleted, http.StatusNoContent)
	}
	wantStatus(t, byChecksum("libs-local/again.bin", "deployer", "secret"), http.StatusNotFound)
	// An administrator reads every file, so other-local's copy serves it.
	wantStatus(t, byChecksum("libs-local/admin.bin", "admin", "secret"), http.StatusCreated)
}

// TestReadsContent covers what the HTTP tests cannot reach: a file in a
// repository that no longer exists counts for no one, a readable file past
// the first page is still found, and store failures are errors, which
// answer 500 rather than a 404 that would send the client to upload.
func TestReadsContent(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	libs := *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})
	if err := db.Repos().Create(ctx, libs); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex("content")
	put := func(key, name string) {
		t.Helper()
		if _, err := db.Items().Put(ctx, types.Item{
			RepoKey: key, Name: name, Actual: types.Checksums{SHA256: sum},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n := range store.MaxLimit {
		put("gone", fmt.Sprintf("f%d.bin", n))
	}
	acc, err := accessFor(ctx, db, auth.Principal{Name: "admin", Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := readsContent(ctx, db, acc, sum); ok || err != nil {
		t.Fatalf("files of a deleted repository: %v %v", ok, err)
	}
	put("libs-local", "late.bin")
	if ok, err := readsContent(ctx, db, acc, sum); !ok || err != nil {
		t.Fatalf("a readable file on the second page: %v %v", ok, err)
	}
	for name, st := range map[string]store.Store{
		"find": buildSearchFaults{Store: db, fail: "find"},
		"repo": faultyStore{Store: db, fail: map[string]bool{"get": true}},
	} {
		if _, err := readsContent(ctx, st, acc, sum); !errors.Is(err, errBoom) {
			t.Errorf("%s failing: %v", name, err)
		}
	}
}

func TestFoldersAndListing(t *testing.T) {
	w := newItemWorld(t, false)
	rec := w.do("PUT", "/artifactory/libs-local/a/b/;owner=me", "admin", "")
	wantStatus(t, rec, http.StatusCreated)
	// A folder's path and uri end in a slash (#400).
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if _, ok := created["downloadUri"]; ok || created["path"] != "/a/b/" ||
		created["uri"] != "https://reg.example/artifactory/libs-local/a/b/" ||
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
		!strings.HasPrefix(
			rec.Header().Get("Location"),
			"https://storage.example/"+sha256Hex("redirected")+"?",
		) {
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
	// anonymousRemotes lets anonymous read remote repositories.
	wantStatus(t, anon.do("GET", "/artifactory/up/absent", "", ""), http.StatusNotFound)
	// Without it, the seeded permissions leave anonymous nothing, remotes
	// included: since 7.84.3 Any Remote does not grant it (#383).
	if err := anon.db.Permissions().Delete(context.Background(), anonymousRemotes); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, anon.do("GET", "/artifactory/up/absent", "", ""), http.StatusUnauthorized)
}

// TestDeployIdenticalContent (#400): replacing a file with its own content
// needs deploy alone, as on Artifactory 7.x, so a CI job re-running jf rt u
// with a user that may not delete succeeds. The request must declare the
// file's SHA-1 or SHA-256, and the bytes stored must be the file's: the
// declaration is the client's word, which nothing checks under
// server-generated-checksums. Anything else still needs delete.
func TestDeployIdenticalContent(t *testing.T) {
	ctx := context.Background()
	w := newItemWorld(t, false)
	const file = "/artifactory/libs-local/same.txt"
	wantStatus(t, w.do("PUT", file, "admin", "content"), http.StatusCreated)
	before := w.item("libs-local", false, "same.txt")
	for name, headers := range map[string][]string{
		"sha1":       {"X-Checksum-Sha1", sha1Hex("content")},
		"sha256":     {"X-Checksum-Sha256", sha256Hex("content")},
		"x-checksum": {"X-Checksum", sha256Hex("content")},
		"all":        {"X-Checksum-Sha1", sha1Hex("content"), "X-Checksum", sha256Hex("content")},
	} {
		rec := w.do("PUT", file, "deployer", "content", headers...)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	after := w.item("libs-local", false, "same.txt")
	if after.ID != before.ID || after.CreatedBy != "admin" || after.ModifiedBy != "deployer" ||
		after.Actual != before.Actual {
		t.Fatalf("re-deployed item %+v, was %+v", after, before)
	}
	if b, err := w.db.Blobs().Get(ctx, before.Actual.SHA256); err != nil || b.Refs != 1 {
		t.Fatalf("content references %+v %v", b, err)
	}
	for name, tc := range map[string]struct {
		body    string
		headers []string
		want    int
	}{
		// Artifactory protects a file from a request that declares nothing.
		"nothing declared": {"content", nil, http.StatusForbidden},
		// MD5 does not count, as in Artifactory 6.20's isFileOverwrite.
		"md5 only":      {"content", []string{"X-Checksum-Md5", md5Hex("content")}, http.StatusForbidden},
		"other content": {"other", []string{"X-Checksum-Sha1", sha1Hex("other")}, http.StatusForbidden},
		// Bytes that are not what the request declares fail the checksum
		// policy, whatever the file holds.
		"bytes differ from the declaration": {
			"other", []string{"X-Checksum-Sha1", sha1Hex("content")}, http.StatusConflict,
		},
	} {
		if rec := w.do("PUT", file, "deployer", tc.body, tc.headers...); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, tc.want, rec.Body)
		}
	}
	if got := w.do("GET", file, "admin", ""); got.Body.String() != "content" {
		t.Fatalf("a refused deploy replaced the file: %q", got.Body)
	}
	// The right to delete still replaces it with anything.
	wantStatus(t, w.do("PUT", file, "admin", "replaced"), http.StatusCreated)

	// Under server-generated-checksums the declaration is not checked, so
	// only the stored bytes can show the content is the file's.
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-server",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"server-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	const server = "/artifactory/server-local/same.txt"
	wantStatus(t, w.do("PUT", server, "admin", "content"), http.StatusCreated)
	wantStatus(t, w.do("PUT", server, "deployer", "other", "X-Checksum-Sha1", sha1Hex("content")),
		http.StatusForbidden)
	wantStatus(t, w.do("PUT", server, "deployer", "content", "X-Checksum-Sha1", sha1Hex("content")),
		http.StatusCreated)
	if got := w.do("GET", server, "admin", ""); got.Body.String() != "content" {
		t.Fatalf("a false declaration replaced the file: %q", got.Body)
	}
}

// TestChecksumDeployIdenticalContent (#400): a checksum deploy of a file's
// own content over it needs deploy alone, unless
// deploy.enforce_permission_check_on_identical_checksum_deploy is on. It
// still needs to read the content (#379), and the checksum is compared only
// after that, so a caller that may not read what it would replace learns
// nothing from a guess: the answer is the 404 of a miss whether or not the
// guess is the file's checksum.
func TestChecksumDeployIdenticalContent(t *testing.T) {
	byChecksum := func(w *itemWorld, p, as, content string) *httptest.ResponseRecorder {
		return w.do("PUT", "/artifactory/"+p, as, "",
			"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex(content))
	}
	w := newItemWorld(t, false)
	for p, body := range map[string]string{
		"libs-local/same.bin":  "content",
		"libs-local/other.bin": "other",
		"other-local/hidden":   "hidden",
	} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", body), http.StatusCreated)
	}
	rec := byChecksum(w, "libs-local/same.bin", "deployer", "content")
	wantStatus(t, rec, http.StatusCreated)
	const uri = `"uri":"https://reg.example/artifactory/libs-local/same.bin"`
	if !strings.Contains(rec.Body.String(), uri) {
		t.Fatalf("checksum deploy answered %s", rec.Body)
	}
	if it := w.item("libs-local", false, "same.bin"); it.ModifiedBy != "deployer" {
		t.Fatalf("re-deployed item %+v", it)
	}
	// Content it may read but that is not the file's needs delete.
	wantStatus(t, byChecksum(w, "libs-local/same.bin", "deployer", "other"), http.StatusForbidden)
	// Content it may not read is a miss, as at a new path.
	hidden := byChecksum(w, "libs-local/same.bin", "deployer", "hidden")
	miss := byChecksum(w, "libs-local/same.bin", "deployer", "never stored")
	wantStatus(t, hidden, http.StatusNotFound)
	if hidden.Body.String() != miss.Body.String() {
		t.Fatalf("an unreadable hit %q differs from a miss %q", hidden.Body, miss.Body)
	}

	// The setting turns the check back on for checksum deploys alone; the
	// jf rt u that follows a refused checksum deploy uploads, and passes.
	strict := newItemWorld(t, false, func(c *Config) { c.EnforceIdenticalChecksumDeploy = true })
	wantStatus(t, strict.do("PUT", "/artifactory/libs-local/same.bin", "admin", "content"),
		http.StatusCreated)
	wantStatus(t, byChecksum(strict, "libs-local/same.bin", "deployer", "content"),
		http.StatusForbidden)
	wantStatus(t, byChecksum(strict, "libs-local/same.bin", "admin", "content"), http.StatusCreated)
	wantStatus(t, strict.do("PUT", "/artifactory/libs-local/same.bin", "deployer", "content",
		"X-Checksum-Sha1", sha1Hex("content")), http.StatusCreated)
}

// TestItemCreated (#400): every deploy answers uri with the download URL,
// as downloadUri, and a folder's path and uri end in a slash.
func TestItemCreated(t *testing.T) {
	w := newItemWorld(t, false)
	const base = "https://reg.example/artifactory/"
	body := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		wantStatus(t, rec, http.StatusCreated)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	zip := string(zipBytes(t, map[string]string{"inner.txt": "inner"}))
	for name, tc := range map[string]struct {
		rec       *httptest.ResponseRecorder
		path, uri string
		file      bool
	}{
		"file": {
			w.do("PUT", "/artifactory/libs-local/d/a%20b.txt;k=v", "admin", "ab"),
			"/d/a b.txt", base + "libs-local/d/a%20b.txt", true,
		},
		"checksum deploy": {
			w.do("PUT", "/artifactory/other-local/copy.txt", "admin", "",
				"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex("ab")),
			"/copy.txt", base + "other-local/copy.txt", true,
		},
		"through a virtual repository": {
			w.do("PUT", "/artifactory/virt/v.txt", "admin", "v"),
			"/v.txt", base + "libs-local/v.txt", true,
		},
		"folder": {
			w.do("PUT", "/artifactory/libs-local/f/g/", "admin", ""),
			"/f/g/", base + "libs-local/f/g/", false,
		},
		"explode": {
			w.do("PUT", "/artifactory/libs-local/ex/bundle.zip", "admin", zip,
				"X-Explode-Archive", "true"),
			"/ex/", base + "libs-local/ex/", false,
		},
		"explode at the root": {
			w.do("PUT", "/artifactory/other-local/bundle.zip", "admin", zip,
				"X-Explode-Archive", "true"),
			"/", base + "other-local/", false,
		},
	} {
		got := body(tc.rec)
		_, download := got["downloadUri"]
		if got["path"] != tc.path || got["uri"] != tc.uri || download != tc.file ||
			(tc.file && got["downloadUri"] != tc.uri) {
			t.Errorf("%s: %v", name, got)
		}
	}
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
	empty := w.do("POST", "/artifactory/api/search/aql", "admin", " \n")
	if empty.Code != 400 || !strings.Contains(empty.Body.String(), "empty query") {
		t.Fatalf("empty %d %s", empty.Code, empty.Body)
	}
	// An output rule refused by the parser is a 400 too.
	wantStatus(t, w.do("POST", "/artifactory/api/search/aql", "admin",
		`items.find().include("name").sort({"$asc":["size"]})`), http.StatusBadRequest)

	// Indented by default, as Artifactory answers; ?compact=true minifies,
	// and stream: true changes nothing.
	pretty := w.do("POST", "/artifactory/api/search/aql", "admin", query)
	if !strings.HasPrefix(pretty.Body.String(), "{\n  \"results\": [\n    {\n") {
		t.Fatalf("pretty %s", pretty.Body)
	}
	compact := w.do("POST", "/artifactory/api/search/aql?compact=true", "admin", query,
		"stream", "true")
	var want bytes.Buffer
	if err := json.Compact(&want, pretty.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if compact.Body.String() != want.String() ||
		compact.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("compact %s", compact.Body)
	}
}

const aqlURL = "/artifactory/api/search/aql"

// aqlRows runs query as user and returns each row as "<repo>/<path>/<name>"
// and the range block.
func aqlRows(
	t *testing.T,
	w *itemWorld,
	user, query string,
	headers ...string,
) ([]string, map[string]any) {
	t.Helper()
	rec := w.do("POST", aqlURL, user, query, headers...)
	var res struct {
		Results []map[string]any `json:"results"`
		Range   map[string]any   `json:"range"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != 200 {
		t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
	}
	out := make([]string, len(res.Results))
	for n, r := range res.Results {
		out[n] = fmt.Sprintf("%v/%v/%v", r["repo"], r["path"], r["name"])
	}
	return out, res.Range
}

// TestSearchAQLWindowBesideProperties: jf rt s --offset sends .offset()
// beside "property", and the window counts items (decision 0012). x.jar is
// skipped whole although it has two properties, a limit returns whole items
// with all their properties, and a non-admin gets the same rows.
func TestSearchAQLWindowBesideProperties(t *testing.T) {
	w := newItemWorld(t, false)
	for _, p := range []string{"a/x.jar;k=1;j=2", "a/y.jar;k=3", "z.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	const find = `items.find({"repo":"libs-local"}).include("repo","path","name","property")`
	type kv struct{ Key, Value string }
	run := func(user, query string) ([]string, [][]kv, map[string]any) {
		t.Helper()
		rec := w.do("POST", aqlURL, user, query)
		var res struct {
			Results []struct {
				Repo, Path, Name string
				Properties       []kv
			}
			Range map[string]any
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != 200 {
			t.Fatalf("%s as %s: %d %s", query, user, rec.Code, rec.Body)
		}
		var rows []string
		var props [][]kv
		for _, r := range res.Results {
			rows = append(rows, r.Repo+"/"+r.Path+"/"+r.Name)
			props = append(props, r.Properties)
		}
		return rows, props, res.Range
	}
	for _, user := range []string{"admin", "deployer"} {
		rows, props, rng := run(user, find+`.offset(1)`)
		if !slices.Equal(rows, []string{"libs-local/a/y.jar", "libs-local/./z.jar"}) ||
			!slices.Equal(props[0], []kv{{"k", "3"}}) || props[1] != nil ||
			rng["start_pos"] != float64(1) || rng["total"] != float64(2) {
			t.Fatalf("offset as %s: %q %v %v", user, rows, props, rng)
		}
		rows, props, _ = run(user, find+`.limit(1)`)
		if !slices.Equal(rows, []string{"libs-local/a/x.jar"}) ||
			!slices.Equal(props[0], []kv{{"j", "2"}, {"k", "1"}}) {
			t.Fatalf("limit as %s: %q %v", user, rows, props)
		}
		rows, _, _ = run(user, find+`.sort({"$desc":["name"]}).offset(1).limit(1)`)
		if !slices.Equal(rows, []string{"libs-local/a/y.jar"}) {
			t.Fatalf("sort as %s: %q", user, rows)
		}
	}
}

func TestSearchAQLAnonymous(t *testing.T) {
	// With anonymous access on, an anonymous query reaches the handler,
	// which refuses it: Artifactory answers 403, not 401.
	w := newItemWorld(t, true)
	rec := w.do("POST", aqlURL, "", `items.find()`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "authenticated") {
		t.Fatalf("anonymous %d %s", rec.Code, rec.Body)
	}
}

func TestSearchAQLCap(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.AQL.MaxResults = 2 })
	for _, p := range []string{"a.jar", "b.jar", "c.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	const all = `items.find({"name":{"$match":"*.jar"}}).include("repo","path","name")`
	for query, wantTrimmed := range map[string]bool{
		all:                            true,
		all + `.limit(5)`:              true,
		all + `.limit(2)`:              false,
		all + `.limit(1)`:              false,
		`items.find({"name":"a.jar"})`: false,
	} {
		rows, rng := aqlRows(t, w, "admin", query)
		if note, _ := rng["notification"].(string); wantTrimmed != (note != "") ||
			(wantTrimmed && (note != aqlTrimmed || len(rows) != 2)) {
			t.Errorf("%s: %v %v", query, rows, rng)
		}
	}
	if !strings.HasPrefix(
		aqlTrimmed,
		"AQL query reached the search hard limit, results are trimmed.",
	) {
		t.Fatal(aqlTrimmed)
	}
}

func TestSearchAQLScanFactor(t *testing.T) {
	put := func(w *itemWorld) {
		// other-local's rows come first, and deployer may not read them.
		for _, p := range []string{
			"other-local/a.jar", "other-local/b.jar",
			"libs-local/c.jar", "libs-local/d.jar", "libs-local/e.jar",
		} {
			wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
		}
	}
	const query = `items.find({"name":{"$match":"*.jar"}}).include("repo","path","name").limit(2)`
	w := newItemWorld(t, false)
	put(w)
	for factor, want := range map[string][]string{
		"":    {},
		"1":   {},
		"x":   {},
		"2":   {"libs-local/./c.jar", "libs-local/./d.jar"},
		"3":   {"libs-local/./c.jar", "libs-local/./d.jar"},
		"100": {"libs-local/./c.jar", "libs-local/./d.jar"},
	} {
		rows, rng := aqlRows(t, w, "deployer", query, "scan-factor", factor)
		if !slices.Equal(rows, want) || rng["limit"] != float64(2) {
			t.Errorf("scan-factor %q: %v %v", factor, rows, rng)
		}
	}
	// Administrators see everything, so they read no further.
	if rows, _ := aqlRows(t, w, "admin", query, "scan-factor", "3"); !slices.Equal(rows,
		[]string{"other-local/./a.jar", "other-local/./b.jar"}) {
		t.Fatalf("admin %v", rows)
	}
	// The configured factor applies without a header, and a huge one does
	// not overflow.
	for _, f := range []int{2, math.MaxInt} {
		w = newItemWorld(t, false, func(c *Config) { c.AQL.ScanFactor = f })
		put(w)
		if rows, _ := aqlRows(t, w, "deployer", query); len(rows) != 2 {
			t.Fatalf("factor %d: %v", f, rows)
		}
	}
}

func TestSearchAQLRepositoryPatterns(t *testing.T) {
	ctx := context.Background()
	w := newItemWorld(t, false)
	for _, p := range []string{"a/x.jar", "a/x-sources.jar", "b/y.jar", "b/y.txt", "c/z.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	c, err := w.db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	c.IncludesPattern = ptr("a/**, b/*.jar")
	c.ExcludesPattern = ptr("**/*-sources.jar")
	if err := w.db.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	const query = `items.find({"repo":"libs-local","type":"any"}).include("repo","path","name")`
	rows, _ := aqlRows(t, w, "deployer", query)
	want := []string{
		"libs-local/./a", "libs-local/a/x.jar", "libs-local/./b", "libs-local/b/y.jar",
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("deployer %v", rows)
	}
	// Administrators are not filtered.
	if rows, _ := aqlRows(t, w, "admin", query); len(rows) != 8 {
		t.Fatalf("admin %v", rows)
	}
}

func TestSearchAQLSecondaryDomains(t *testing.T) {
	ctx := context.Background()
	w := newItemWorld(t, false)
	for _, p := range []string{"libs-local/x.jar", "other-local/y.jar", "server-local/z.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	x := w.item("libs-local", false, "x.jar")
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := w.db.Items().PutStats(ctx, x.ID, types.ItemStats{
		DownloadCount: 3, LastDownloaded: at, LastDownloadedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().SetProperties(ctx, x.ID, types.Properties{
		"keep": {"1"}, "drop": {"2"},
	}, at); err != nil {
		t.Fatal(err)
	}
	rec := w.do(
		"POST",
		aqlURL+"?compact=true",
		"admin",
		`items.find({"name":{"$match":"*.jar"}}).include("repo","path","name","virtual_repos","stat","@keep")`,
	)
	want := `{"results":[{"repo":"libs-local","path":".","name":"x.jar",` +
		`"virtual_repos":["virt","virt-nodeploy"],"properties":[{"key":"keep","value":"1"}],` +
		`"stats":[{"downloaded":"2026-02-03T04:05:06.000Z","downloads":3,"downloaded_by":"admin",` +
		`"remote_downloads":0}]}],"range":{"start_pos":0,"end_pos":1,"total":1}}`
	if rec.Body.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", rec.Body, want)
	}
	// virtual_repos follows nested virtual repositories, and a repository in
	// none renders an empty list.
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "outer", Class: repo.Virtual, Repositories: []string{"virt"},
	})); err != nil {
		t.Fatal(err)
	}
	rec = w.do("POST", aqlURL+"?compact=true", "admin",
		`items.find({"name":{"$match":"*.jar"}}).include("repo","path","name","virtual_repos")`)
	for _, part := range []string{
		`"name":"y.jar","virtual_repos":["outer","virt"]`,
		`"name":"z.jar","virtual_repos":[]`,
	} {
		if !strings.Contains(rec.Body.String(), part) {
			t.Fatalf("%s lacks %s", rec.Body, part)
		}
	}
	// A non-admin's virtual_repos query is filtered like any other.
	rows, _ := aqlRows(t, w, "deployer",
		`items.find({"name":{"$match":"*.jar"}}).include("repo","path","name","virtual_repos")`)
	if !slices.Equal(rows, []string{"libs-local/./x.jar"}) {
		t.Fatalf("deployer %v", rows)
	}
}

func TestSearchAQLRemoteAndVirtualKeys(t *testing.T) {
	ctx := context.Background()
	w := newItemWorld(t, false)
	for _, p := range []string{"libs-local/x.jar", "other-local/y.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	// Nothing deploys into a cache, so the cached item is stored directly.
	if _, err := w.db.Items().Put(ctx, types.Item{
		RepoKey: "up", Cached: true, Path: types.RootPath, Name: "c.jar",
	}); err != nil {
		t.Fatal(err)
	}
	const (
		include = `.include("repo","path","name")`
		x       = "libs-local/./x.jar"
		y       = "other-local/./y.jar"
		c       = "up-cache/./c.jar"
	)
	for _, tc := range []struct {
		user, query string
		want        []string
	}{
		{"admin", `items.find({"repo":"up"})` + include, []string{c}},
		{"admin", `items.find({"repo":"up-cache"})` + include, []string{c}},
		{"admin", `items.find({"repo":"virt"})` + include, []string{x, y, c}},
		{"admin", `items.find({"repo":{"$ne":"virt-nodeploy"}})` + include, []string{y, c}},
		{"admin", `items.find({"repo":{"$match":"u?"}})` + include, []string{c}},
		{"admin", `items.find({"repo":{"$nmatch":"virt*"}})` + include, nil},
		// Permissions apply to the cache as to any row: reader reads every
		// repository, deployer only libs-local.
		{"reader", `items.find({"repo":"up"})` + include, []string{c}},
		{"deployer", `items.find({"repo":"up"})` + include, nil},
		{"deployer", `items.find({"repo":"virt"})` + include, []string{x}},
	} {
		if rows, _ := aqlRows(t, w, tc.user, tc.query); !slices.Equal(rows, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.user, tc.query, rows, tc.want)
		}
	}
	// A virtual repository's key adds virtual_repos to the default fields.
	rec := w.do("POST", aqlURL+"?compact=true", "admin", `items.find({"repo":"virt"})`)
	for _, part := range []string{
		`"repo":"up-cache","path":".","name":"c.jar","type":"file","size":0,`,
		`"virtual_repos":["virt"]}`,
		`"virtual_repos":["virt","virt-nodeploy"]}`,
	} {
		if !strings.Contains(rec.Body.String(), part) {
			t.Fatalf("%s lacks %s", rec.Body, part)
		}
	}
	// $ne names no virtual repository's contents, so it adds nothing.
	rec = w.do("POST", aqlURL+"?compact=true", "admin", `items.find({"repo":{"$ne":"virt"}})`)
	if strings.Contains(rec.Body.String(), "virtual_repos") {
		t.Fatalf("$ne added virtual_repos: %s", rec.Body)
	}
}

func TestUploadsConfig(t *testing.T) {
	w := newItemWorld(t, false)
	rec := w.do("GET", "/artifactory/api/v1/uploads/config", "admin", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"supported":false}` {
		t.Fatalf("uploads config %d %s", rec.Code, rec.Body)
	}
}

// Matrix values are form-decoded as the JFrog CLI encodes them (QueryEscape)
// and Artifactory decodes them (URLDecoder): "+" is a space and "%2B" a
// plus. Keys keep "+", whose trailing use marks a mandatory property.
func TestMatrixParameterDecoding(t *testing.T) {
	for name, c := range map[string]struct {
		escaped   string
		want      types.Properties
		mandatory []string
	}{
		"plus is a space":       {"libs/a.txt;build.name=my+build", types.Properties{"build.name": {"my build"}}, nil},
		"encoded plus is plus":  {"libs/a.txt;v=a%2Bb", types.Properties{"v": {"a+b"}}, nil},
		"percent-encoded space": {"libs/a.txt;v=a%20b", types.Properties{"v": {"a b"}}, nil},
		"mandatory key":         {"libs/a.txt;k+=v", types.Properties{"k": {"v"}}, []string{"k"}},
		"mandatory, plus value": {"libs/a.txt;k+=x+y", types.Properties{"k": {"x y"}}, []string{"k"}},
		"several values":        {"libs/a.txt;k=a,b", types.Properties{"k": {"a", "b"}}, nil},
		"encoded separator":     {"libs/a.txt;k=a%2Cb", types.Properties{"k": {"a", "b"}}, nil},
		"escaped comma":         {"libs/a.txt;k=a%5C%2Cb%2Cc", types.Properties{"k": {"a,b", "c"}}, nil},
		"as jf sends a build": {
			"libs/a.txt;build.name=my+build;build.number=1;build.timestamp=1760000000000",
			types.Properties{
				"build.name": {"my build"}, "build.number": {"1"}, "build.timestamp": {"1760000000000"},
			},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := parseEscapedItemPath(c.escaped)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.props) != len(c.want) {
				t.Fatalf("props = %v, want %v", req.props, c.want)
			}
			for k, v := range c.want {
				if !slices.Equal(req.props[k], v) {
					t.Errorf("%s = %q, want %q", k, req.props[k], v)
				}
			}
			for _, k := range c.mandatory {
				if !req.mandatory[k] {
					t.Errorf("%s is not mandatory", k)
				}
			}
			if req.path != "a.txt" {
				t.Errorf("path = %q", req.path)
			}
		})
	}
	if _, err := parseEscapedItemPath("libs/a.txt;k=%zz"); err == nil {
		t.Error("a malformed escape was accepted")
	}
}
