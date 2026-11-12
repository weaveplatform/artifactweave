package swifthttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func blackOut(c *repo.Config) {
	on := true
	c.BlackedOut = &on
}

func restorePolicy(c *repo.Config) {
	c.BlackedOut, c.IncludesPattern, c.ExcludesPattern = new(bool), new(string), new(string)
}

func excludes(patterns string) func(*repo.Config) {
	return func(c *repo.Config) { c.ExcludesPattern = &patterns }
}

// wantProblem checks that rec answers status as a problem detail whose
// detail is message, with none of an archive's headers (R7).
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		body.Status != status || body.Title != http.StatusText(status) ||
		body.Detail != message {
		t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json; charset=utf-8" ||
		rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Digest") != "" {
		t.Fatalf("problem detail headers: %v", rec.Header())
	}
}

// wantReleases checks that rec answers 200 listing exactly versions.
func wantReleases(t *testing.T, rec *httptest.ResponseRecorder, versions ...string) {
	t.Helper()
	var list struct {
		Releases map[string]any `json:"releases"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("release list: %d %s", rec.Code, rec.Body)
	}
	var got []string
	for version := range list.Releases {
		got = append(got, version)
	}
	slices.Sort(got)
	if !slices.Equal(got, versions) {
		t.Fatalf("release list %v, want %v", got, versions)
	}
}

// wantOK checks that rec answers 200 with a body holding want.
func wantOK(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("%d %s, want 200 with %q", rec.Code, rec.Body, want)
	}
}

func (f *fixture) publish(key, version string, zip []byte) {
	f.t.Helper()
	body, contentType := form(f.t, zip)
	rec := f.do(http.MethodPut, "/artifactory/api/swift/"+key+"/acme/Widget/"+version, body,
		contentType, "admin")
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("publish %s %s: %d %s", key, version, rec.Code, rec.Body)
	}
}

// A blacked-out local repository refuses every Swift read, the release
// list and release metadata at the requested path before anything is
// computed and the archive with the policy's own message. Patterns that
// reject the archive refuse only the archive: the list keeps the release,
// and its metadata and Package.swift answer as before (R2a, R3, R6).
func TestUnitSwiftLocalReadsFollowTheDownloadPolicy(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			f.publish("packages", "1.0.0", archive(t, "Widget/"))
			get := func(path, user string) *httptest.ResponseRecorder {
				return f.do(http.MethodGet, base+path, nil, "", user)
			}
			const zip = "acme/Widget/Widget-1.0.0.zip"

			f.patch("packages", blackOut)
			for _, tc := range []struct{ request, named string }{
				{"acme/Widget", "acme/Widget"},
				{"acme/Widget/1.0.0", "acme/Widget/1.0.0"},
				{"acme/Widget/1.0.0.json", "acme/Widget/1.0.0.json"},
				{"acme/Widget/1.0.0/Package.swift", "acme/Widget/1.0.0/Package.swift"},
				{"acme/Widget/1.0.0.zip", zip},
			} {
				wantProblem(t, get(tc.request, "admin"), http.StatusNotFound,
					fmt.Sprintf(blackedOut, "packages", tc.named))
				denied := get(tc.request, "nobody")
				if denied.Code != http.StatusForbidden ||
					strings.Contains(denied.Body.String(), "blacked") {
					t.Fatalf("%s as a caller without Read: %d %s", tc.request, denied.Code,
						denied.Body)
				}
			}

			f.patch("packages", restorePolicy)
			f.patch("packages", excludes(zip))
			wantReleases(t, get("acme/Widget", "admin"), "1.0.0")
			wantOK(t, get("acme/Widget/1.0.0", "admin"), `"checksum":"`)
			wantOK(t, get("acme/Widget/1.0.0/Package.swift", "admin"), "import PackageDescription")
			wantProblem(t, get("acme/Widget/1.0.0.zip", "admin"), http.StatusNotFound,
				fmt.Sprintf(rejected, "packages", zip))
			wantProblem(t, get("acme/Widget/9.9.9.zip", "admin"), http.StatusNotFound,
				"Swift release not found")
		})
	}
}

// A remote repository whose patterns reject one archive keeps the release
// listed and answers its metadata and Package.swift, refusing only the
// archive, with the patterns' 404 rather than 500 or 502. Its blackout
// still names the package folder its metadata reads are checked at.
func TestUnitSwiftRemoteReadsFollowTheRemotePolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed(t, "correct")
	get := func(path string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, base+path, nil, "", "admin")
	}
	const zip = "acme/Widget/Widget-1.0.0.zip"
	f.patch("packages", excludes(zip))
	for range 2 {
		wantReleases(t, get("acme/Widget"), "1.0.0", "2.0.0")
		wantOK(t, get("acme/Widget/1.0.0"), `"checksum":"`)
		wantOK(t, get("acme/Widget/1.0.0/Package.swift"), "import PackageDescription")
	}
	wantProblem(t, get("acme/Widget/1.0.0.zip"), http.StatusNotFound,
		fmt.Sprintf(rejected, "packages", zip))
	if n := f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip"); n != 0 {
		t.Fatalf("a rejected archive reached upstream %d times", n)
	}

	f.patch("packages", restorePolicy)
	f.patch("packages", blackOut)
	for _, request := range []string{
		"acme/Widget", "acme/Widget/1.0.0", "acme/Widget/1.0.0/Package.swift",
		"acme/Widget/1.0.0.zip",
	} {
		wantProblem(t, get(request), http.StatusNotFound,
			fmt.Sprintf(blackedOut, "packages", "acme/Widget"))
	}
}

// A virtual repository leaves a blacked-out member out of its release
// list and passes over a member that refuses the archive, remote members
// included, keeping a release whose archive a member's patterns reject
// listed and answered. Its own patterns refuse only the archive, with "No
// repository found to serve the request" (R5).
func TestUnitSwiftVirtualReadsFollowTheDownloadPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	remoteZIP := f.seed(t, "correct")
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local},
		{
			Key: "virtual", Class: repo.Virtual,
			Repositories: []string{"local", "packages"},
		},
	} {
		c.PackageType = repo.Swift
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	localZIP := archive(t, "LocalWidget/")
	f.publish("local", "1.0.0", localZIP)
	f.publish("local", "3.0.0", localZIP)
	const virtual = "/artifactory/api/swift/virtual/"
	get := func(path, user string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, virtual+path, nil, "", user)
	}
	wantZIP := func(path string, want []byte) {
		t.Helper()
		rec := get(path, "admin")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s: %d %d bytes", path, rec.Code, rec.Body.Len())
		}
	}
	const zip = "acme/Widget/Widget-1.0.0.zip"

	f.patch("local", blackOut)
	wantReleases(t, get("acme/Widget", "admin"), "1.0.0", "2.0.0")
	wantZIP("acme/Widget/1.0.0.zip", remoteZIP)
	wantProblem(t, get("acme/Widget/3.0.0", "admin"), http.StatusNotFound,
		"Swift release not found")

	f.patch("local", restorePolicy)
	f.patch("packages", blackOut)
	f.patch("virtual", func(c *repo.Config) { c.Repositories = []string{"packages", "local"} })
	wantReleases(t, get("acme/Widget", "admin"), "1.0.0", "3.0.0")
	wantZIP("acme/Widget/1.0.0.zip", localZIP)

	f.patch("packages", restorePolicy)
	f.patch("packages", excludes(zip))
	fetched := f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip")
	wantReleases(t, get("acme/Widget", "admin"), "1.0.0", "2.0.0", "3.0.0")
	wantOK(t, get("acme/Widget/1.0.0", "admin"), `"checksum":"`)
	wantZIP("acme/Widget/1.0.0.zip", localZIP)
	if n := f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip") - fetched; n != 0 {
		t.Fatalf("a rejected archive reached upstream %d times", n)
	}

	f.patch("packages", restorePolicy)
	f.patch("virtual", excludes(zip))
	wantProblem(t, get("acme/Widget/1.0.0.zip", "admin"), http.StatusNotFound,
		fmt.Sprintf(noMember, "virtual", zip))
	if rec := get("acme/Widget/1.0.0.zip", "nobody"); rec.Code != http.StatusForbidden {
		t.Fatalf("a caller without Read: %d %s", rec.Code, rec.Body)
	}
	wantReleases(t, get("acme/Widget", "admin"), "1.0.0", "2.0.0", "3.0.0")
	wantOK(t, get("acme/Widget/1.0.0", "admin"), `"checksum":"`)
	f.patch("virtual", func(c *repo.Config) { c.Repositories = []string{"local", "packages"} })
	wantOK(t, get("acme/Widget/1.0.0", "admin"), `"checksum":"`)
}
