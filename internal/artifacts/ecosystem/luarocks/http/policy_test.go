package luarockshttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The download policy's messages (generic.CheckDownload, decision 0032).
const (
	blackedOut = "The repository '%[1]s' is blacked out and cannot serve artifact '%[1]s:%[2]s'."
	rejected   = "The repository '%[1]s' rejected the artifact '%[1]s:%[2]s' due to its " +
		"include/exclude pattern settings."
	noMember = "No repository found to serve the request for %s:%s"
)

// patchRepo changes the stored configuration of the repository key.
func patchRepo(t *testing.T, f *fixture, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func blackOut(c *repo.Config) {
	on := true
	c.BlackedOut = &on
}

// restorePolicy clears the blackout and the patterns.
func restorePolicy(c *repo.Config) {
	c.BlackedOut, c.IncludesPattern, c.ExcludesPattern = new(bool), new(string), new(string)
}

func includes(patterns string) func(*repo.Config) {
	return func(c *repo.Config) { c.IncludesPattern = &patterns }
}

func excludes(patterns string) func(*repo.Config) {
	return func(c *repo.Config) { c.ExcludesPattern = &patterns }
}

// wantError checks that rec answers status in Artifactory's error envelope
// with message alone.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// wantBody checks that rec answers 200 with a body holding each of want and
// none of not.
func wantBody(t *testing.T, rec *httptest.ResponseRecorder, want, not []string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", rec.Code, rec.Body)
	}
	for _, s := range want {
		if !strings.Contains(rec.Body.String(), s) {
			t.Fatalf("body %s lacks %q", rec.Body, s)
		}
	}
	for _, s := range not {
		if strings.Contains(rec.Body.String(), s) {
			t.Fatalf("body %s holds %q", rec.Body, s)
		}
	}
}

// wantDenied checks that a caller without Read is refused 403 before the
// download policy, so the answer says nothing of how the repository is
// set (DV-057).
func wantDenied(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "repository") {
		t.Fatalf("a caller without Read: %d %s, want 403", rec.Code, rec.Body)
	}
}

// A local repository's manifest is a stored document that 7.x reads
// through the download path: a blackout and patterns that reject the
// manifest's own path refuse it, after Read permission, while patterns
// that reject one rock refuse only the rock, which the manifest still
// lists (R1, R3).
func TestAccLocalManifestAndRocksFollowTheDownloadPolicy(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			const base = "/artifactory/api/luarocks/rocks/"
			get := func(path, user string) *httptest.ResponseRecorder {
				return f.do(http.MethodGet, base+path, nil, "", user)
			}
			uploadSpecTo(t, f, "rocks", "hello", "1.0-1", spec("hello", "1.0-1"))
			wantBody(t, get("manifest-5.4", "admin"), []string{`["hello"]`}, nil)

			patchRepo(t, f, "rocks", blackOut)
			for _, path := range []string{"manifest", "manifest-5.4", "hello-1.0-1.rockspec"} {
				wantError(t, get(path, "admin"), http.StatusNotFound,
					fmt.Sprintf(blackedOut, "rocks", path))
			}
			wantDenied(t, get("manifest-5.4", "nobody"))

			patchRepo(t, f, "rocks", restorePolicy)
			patchRepo(t, f, "rocks", includes("**/*.rock,**/*.rockspec"))
			wantError(t, get("manifest-5.4", "admin"), http.StatusNotFound,
				fmt.Sprintf(rejected, "rocks", "manifest-5.4"))
			if rec := get("hello-1.0-1.rockspec", "admin"); rec.Code != http.StatusOK {
				t.Fatalf("included rockspec: %d %s", rec.Code, rec.Body)
			}

			patchRepo(t, f, "rocks", restorePolicy)
			patchRepo(t, f, "rocks", excludes("hello-*.rockspec"))
			wantBody(t, get("manifest-5.4", "admin"),
				[]string{`["hello"]`, `arch = "rockspec"`}, nil)
			wantError(t, get("hello-1.0-1.rockspec", "admin"), http.StatusNotFound,
				fmt.Sprintf(rejected, "rocks", "hello-1.0-1.rockspec"))
		})
	}
}

// A remote repository's own policy is checked at the path a client asks
// for: the manifest file, where 7.x caches it (RTFACT-30802), and the rock
// itself, which a blackout names. A rock whose file the patterns reject
// stays listed and its download answers the patterns' 404, not 502. The
// manifest lookup a rock download makes is refused only by the blackout.
func TestAccRemoteManifestAndRocksFollowTheRemotePolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed()
	get := func(path, user string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, remoteBase+path, nil, "", user)
	}
	patchRepo(t, f.fixture, "rocks", blackOut)
	for _, path := range []string{"manifest-5.4", "visible-1.0-1.rockspec"} {
		wantError(t, get(path, "admin"), http.StatusNotFound,
			fmt.Sprintf(blackedOut, "rocks", path))
	}
	if n := f.up.Count(http.MethodGet, ""); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}
	wantDenied(t, get("visible-1.0-1.rockspec", "nobody"))

	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "rocks", includes("**/*.rock,**/*.rockspec"))
	wantError(t, get("manifest-5.4", "admin"), http.StatusNotFound,
		fmt.Sprintf(rejected, "rocks", "manifest-5.4"))
	got := get("visible-1.0-1.rockspec", "admin")
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), spec("visible", "1.0-1")) {
		t.Fatalf("a rock whose manifest the patterns reject: %d %s", got.Code, got.Body)
	}

	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "rocks", excludes("visible-1.0-1.src.rock"))
	for range 2 {
		wantBody(t, get("manifest-5.4", "admin"),
			[]string{`["visible"]`, `"src"`, `["hidden"]`}, nil)
	}
	wantBody(t, get("manifest-5.4", "partial"), []string{`"src"`}, []string{"hidden"})
	wantError(t, get("visible-1.0-1.src.rock", "admin"), http.StatusNotFound,
		fmt.Sprintf(rejected, "rocks", "visible-1.0-1.src.rock"))
	if n := f.up.Count(http.MethodGet, "/rocks/visible-1.0-1.src.rock"); n != 0 {
		t.Fatalf("a rejected rock reached upstream %d times", n)
	}
}

// A virtual repository leaves a blacked-out member out of its manifest and
// passes over it for a rock, remote members included. It keeps a rock a
// member's patterns reject listed, passing over that member for the file,
// and leaves out a member whose patterns reject the manifest. Its own
// patterns refuse the manifest and a rock with "No repository found to
// serve the request" (R5).
func TestAccVirtualManifestAndRocksFollowTheDownloadPolicy(t *testing.T) {
	f := newVirtualFixture(t)
	localSpec := append(spec("visible", "1.0-1"), []byte("-- local\n")...)
	uploadSpecTo(t, f.fixture, "local", "visible", "1.0-1", localSpec)
	uploadSpecTo(t, f.fixture, "local", "local-only", "2.0-1", spec("local-only", "2.0-1"))
	get := func(path, user string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, virtualBase+path, nil, "", user)
	}
	wantSpec := func(path string, want []byte) {
		t.Helper()
		got := get(path, "admin")
		if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), want) {
			t.Fatalf("%s: %d %s", path, got.Code, got.Body)
		}
	}

	patchRepo(t, f.fixture, "local", blackOut)
	wantBody(t, get("manifest-5.4", "admin"),
		[]string{`["visible"]`, `["hidden"]`}, []string{"local-only"})
	wantSpec("visible-1.0-1.rockspec", spec("visible", "1.0-1"))
	wantError(t, get("local-only-2.0-1.rockspec", "admin"), http.StatusNotFound,
		"Could not find resource")

	patchRepo(t, f.fixture, "local", restorePolicy)
	patchRepo(t, f.fixture, "rocks", blackOut)
	asked := f.up.Count(http.MethodGet, "")
	wantBody(t, get("manifest-5.4", "admin"),
		[]string{"local-only", `["visible"]`}, []string{"hidden"})
	wantError(t, get("visible-1.0-1.src.rock", "admin"), http.StatusNotFound,
		"Could not find resource")
	patchRepo(t, f.fixture, "combined", func(c *repo.Config) {
		c.Repositories = []string{"rocks", "local"}
	})
	wantSpec("visible-1.0-1.rockspec", localSpec)
	if n := f.up.Count(http.MethodGet, "") - asked; n != 0 {
		t.Fatalf("a blacked-out remote member asked its upstream %d times", n)
	}

	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "rocks", excludes("visible-1.0-1.src.rock"))
	wantBody(t, get("manifest-5.4", "admin"),
		[]string{"local-only", `"src"`, `["hidden"]`}, nil)
	wantError(t, get("visible-1.0-1.src.rock", "admin"), http.StatusNotFound,
		"Could not find resource")

	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "rocks", includes("**/*.rock,**/*.rockspec"))
	wantBody(t, get("manifest-5.4", "admin"),
		[]string{"local-only", `["visible"]`}, []string{"hidden"})

	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "combined", excludes("manifest*"))
	wantError(t, get("manifest-5.4", "admin"), http.StatusNotFound,
		fmt.Sprintf(noMember, "combined", "manifest-5.4"))
	patchRepo(t, f.fixture, "combined", excludes("visible-1.0-1.rockspec"))
	wantError(t, get("visible-1.0-1.rockspec", "admin"), http.StatusNotFound,
		fmt.Sprintf(noMember, "combined", "visible-1.0-1.rockspec"))
	wantDenied(t, get("visible-1.0-1.rockspec", "nobody"))
	wantBody(t, get("manifest-5.4", "admin"), []string{`["visible"]`, `"rockspec"`}, nil)
}
