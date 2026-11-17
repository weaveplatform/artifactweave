package artifacts_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // the SHA-1 checksum file's wire value
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// A read through a virtual repository asks a remote member's upstream for
// a file no earlier step holds, once every member's own copy has been
// looked for (decision 0008, R1, R3 and R10; #1535), and answers it as a
// direct read of the member does: 7.161.26's processStandard walks the
// locals, then the remotes' caches, then the remotes, and the remote that
// finds the file downloads it into its cache.

// virtualRemoteWorld is the router over an application runtime on one SQL
// store, with the generic remotes remote-a and remote-b, each over a fake
// upstream of its own that counts what it is asked; the generic local
// libs; the virtual repositories virt, [remote-a, libs], whose local wins
// although it is listed last, and pair, [remote-a, remote-b]; and the
// Maven remote maven-remote over a third upstream under the Maven virtual
// maven-virt. reader may read every member and fill the remotes' caches;
// viewer may read remote-a and libs but not fill a cache; outsider may read
// libs alone. Every password is "pw".
type virtualRemoteWorld struct {
	t                 *testing.T
	rt                *app.Runtime
	h                 http.Handler
	upA, upB, upMaven *fakeupstream.Server
}

func newVirtualRemoteWorld(t *testing.T, backend string) *virtualRemoteWorld {
	t.Helper()
	rt := harness.App(t, func(cfg *config.Config) {
		cfg.Bootstrap.AdminPassword = "pw"
		if backend == "postgres" {
			cfg.Database.Driver = "postgres"
			cfg.Database.DSN = platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
		}
	})
	w := &virtualRemoteWorld{
		t:       t,
		rt:      rt,
		upA:     fakeupstream.New(t, fakeupstream.Options{}),
		upB:     fakeupstream.New(t, fakeupstream.Options{}),
		upMaven: fakeupstream.New(t, fakeupstream.Options{}),
	}
	for _, c := range []repo.Config{
		{Key: "remote-a", Class: repo.Remote, PackageType: repo.Generic, URL: w.upA.URL},
		{Key: "remote-b", Class: repo.Remote, PackageType: repo.Generic, URL: w.upB.URL},
		{Key: "libs", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "virt", Class: repo.Virtual, PackageType: repo.Generic,
			Repositories: []string{"remote-a", "libs"},
		},
		{
			Key: "pair", Class: repo.Virtual, PackageType: repo.Generic,
			Repositories: []string{"remote-a", "remote-b"},
		},
		{Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven, URL: w.upMaven.URL},
		{
			Key: "maven-virt", Class: repo.Virtual, PackageType: repo.Maven,
			Repositories: []string{"maven-remote"},
		},
	} {
		if err := rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"reader", "viewer", "outsider"} {
		if err := rt.Store.Users().
			Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	read, fill := accessapi.ActionRead, accessapi.ActionRead|accessapi.ActionWrite
	for _, grant := range []struct {
		name    string
		users   map[string]accessapi.Actions
		targets []string
	}{
		{
			"remotes",
			map[string]accessapi.Actions{"reader": fill},
			[]string{"remote-a", "remote-b", "maven-remote"},
		},
		{"viewer-a", map[string]accessapi.Actions{"viewer": read}, []string{"remote-a"}},
		{
			"libs",
			map[string]accessapi.Actions{"reader": read, "viewer": read, "outsider": read},
			[]string{"libs"},
		},
	} {
		targets := map[string]accesstest.Target{}
		for _, key := range grant.targets {
			targets[key] = accesstest.Target{}
		}
		if err := rt.Store.Permissions().Create(t.Context(), accesstest.Permission{
			Name: grant.name,
			Resources: map[accessapi.ResourceType]accesstest.Resource{
				accessapi.ResourceArtifact: {
					Actions: accesstest.Grants{Users: grant.users},
					Targets: targets,
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.h = harness.Router(harness.Config{
		Store:         rt.Store,
		Blobs:         rt.Blobs,
		Auth:          rt.Auth,
		Tokens:        rt.Tokens,
		Keys:          rt.Keys,
		RemoteSources: rt.Artifacts.RemoteSources(),
		RemoteHTTP:    rt.Artifacts.RemoteHTTP(),
		Logger:        slog.New(slog.DiscardHandler),
	})
	return w
}

// do sends method path with body as user, whose password is "pw".
func (w *virtualRemoteWorld) do(method, path, user, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// update saves c over the stored repository of its key.
func (w *virtualRemoteWorld) update(c repo.Config) {
	w.t.Helper()
	if err := w.rt.Store.Repos().Update(w.t.Context(), *repo.Defaults(&c)); err != nil {
		w.t.Fatal(err)
	}
}

// wantFetched fails unless rec answered status with body.
func wantFetched(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || rec.Body.String() != body {
		t.Fatalf("%s: %d %q, want %d %q", what, rec.Code, rec.Body, status, body)
	}
}

// answeredHeaders are the headers a download answers a file with, which a
// read through a virtual repository answers as a direct read of the member
// that serves it does.
var answeredHeaders = []string{
	"Content-Type", "Content-Length", "Content-Disposition", "Last-Modified", "ETag",
	"Accept-Ranges", "X-Checksum-Sha1", "X-Checksum-Sha256", "X-Checksum-Md5",
	"X-Artifactory-Filename", "X-Artifactory-Id",
}

// sameHeaders fails unless got carries want's answeredHeaders.
func sameHeaders(t *testing.T, what string, got, want http.Header) {
	t.Helper()
	for _, name := range answeredHeaders {
		if got.Get(name) != want.Get(name) {
			t.Errorf("%s: %s %q, want %q", what, name, got.Get(name), want.Get(name))
		}
	}
}

func TestAccVirtualFetchesRemoteMembersUpstreams(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newVirtualRemoteWorld(t, backend)
			virtualRemoteCold(t, w)
			virtualRemoteOrder(t, w)
			virtualRemotePassOver(t, w)
			virtualRemoteChecksumsAndArchives(t, w)
			virtualRemoteMaven(t, w)
			virtualRemoteTrace(t, w)
			virtualRemoteExpired(t, w)
		})
	}
}

// A cold file only remote-a's upstream holds: the virtual answers its
// bytes with the headers a direct read of remote-a answers, the upstream
// is asked once, remote-a's cache keeps the file, and a HEAD answers the
// GET's headers with no body.
func virtualRemoteCold(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	w.upA.Set("/cold.bin", fakeupstream.Resource{Body: []byte("cold bytes")})
	got := w.do(http.MethodGet, "/artifactory/virt/cold.bin", "reader", "")
	wantFetched(t, "virtual GET", got, 200, "cold bytes")
	if n := w.upA.Count(http.MethodGet, "/cold.bin"); n != 1 {
		t.Fatalf("upstream GETs %d, want 1", n)
	}
	cached := w.do(http.MethodGet, "/artifactory/remote-a-cache/cold.bin", "reader", "")
	wantFetched(t, "cache GET", cached, 200, "cold bytes")
	direct := w.do(http.MethodGet, "/artifactory/remote-a/cold.bin", "reader", "")
	wantFetched(t, "direct GET", direct, 200, "cold bytes")
	sameHeaders(t, "virtual GET", got.Header(), direct.Header())
	head := w.do(http.MethodHead, "/artifactory/virt/cold.bin", "reader", "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("virtual HEAD: %d %q", head.Code, head.Body)
	}
	sameHeaders(t, "virtual HEAD", head.Header(), got.Header())

	// A HEAD of a file no cache holds asks the upstream what a direct HEAD
	// of remote-a asks it, and answers the headers of the GET that follows.
	for _, key := range []string{"remote-a", "virt"} {
		path := "/" + key + "-head.bin"
		w.upA.Set(path, fakeupstream.Resource{Body: []byte("head bytes")})
		head = w.do(http.MethodHead, "/artifactory/"+key+path, "reader", "")
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatalf("%s cold HEAD: %d %q", key, head.Code, head.Body)
		}
		got = w.do(http.MethodGet, "/artifactory/"+key+path, "reader", "")
		wantFetched(t, key+" GET after HEAD", got, 200, "head bytes")
		sameHeaders(t, key+" cold HEAD", head.Header(), got.Header())
	}
	if direct, virtual := upstreamMethods(w.upA, "/remote-a-head.bin"),
		upstreamMethods(w.upA, "/virt-head.bin"); direct != virtual {
		t.Fatalf("cold HEAD and GET asked the upstream %s through the virtual, %s directly",
			virtual, direct)
	}
}

// upstreamMethods are the methods of the requests up received for path, in
// order.
func upstreamMethods(up *fakeupstream.Server, path string) string {
	var methods []string
	for _, r := range requestsFor(up, path) {
		methods = append(methods, r.Method)
	}
	return strings.Join(methods, " ")
}

// Every member's own copy is looked for before any upstream is asked: a
// local holding the path wins although it is listed after the remote, and
// remote-b's cache wins over remote-a's upstream.
func virtualRemoteOrder(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	w.upA.Set("/local.bin", fakeupstream.Resource{Body: []byte("upstream copy")})
	if rec := w.do(http.MethodPut, "/artifactory/libs/local.bin", "admin", "local copy"); rec.Code != 201 {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	wantFetched(t, "local over remote", w.do(http.MethodGet, "/artifactory/virt/local.bin", "reader", ""),
		200, "local copy")
	if n := len(requestsFor(w.upA, "/local.bin")); n != 0 {
		t.Fatalf("remote-a's upstream asked %d times for a local's file", n)
	}

	w.upB.Set("/both.bin", fakeupstream.Resource{Body: []byte("b bytes")})
	wantFetched(t, "seed remote-b", w.do(http.MethodGet, "/artifactory/remote-b/both.bin", "reader", ""),
		200, "b bytes")
	w.upA.Set("/both.bin", fakeupstream.Resource{Body: []byte("a bytes")})
	wantFetched(t, "cache over upstream", w.do(http.MethodGet, "/artifactory/pair/both.bin", "reader", ""),
		200, "b bytes")
	if n := len(requestsFor(w.upA, "/both.bin")); n != 0 {
		t.Fatalf("remote-a's upstream asked %d times for remote-b's cached file", n)
	}
}

// requestsFor are the requests up received for path.
func requestsFor(up *fakeupstream.Server, path string) []fakeupstream.Request {
	var out []fakeupstream.Request
	for _, r := range up.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// An offline or blacked-out remote, an upstream miss and an upstream
// failure pass over the member: the next one serves, or the virtual
// answers 404 "Could not find resource". A caller who may not read a
// remote member is not fetched for and is answered the member's refusal,
// and one who may read but not fill its cache the member's own refusal of
// the fill.
func virtualRemotePassOver(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	a := repo.Config{Key: "remote-a", Class: repo.Remote, PackageType: repo.Generic, URL: w.upA.URL}
	w.upA.Set("/next.bin", fakeupstream.Resource{Body: []byte("a next")})
	w.upB.Set("/next.bin", fakeupstream.Resource{Body: []byte("b next")})
	for _, tc := range []struct {
		name string
		edit func(*repo.Config)
	}{
		{"offline", func(c *repo.Config) { c.Offline = ptr(true) }},
		{"blacked out", func(c *repo.Config) { c.BlackedOut = ptr(true) }},
	} {
		c := a
		tc.edit(&c)
		w.update(c)
		wantFetched(t, tc.name, w.do(http.MethodGet, "/artifactory/pair/next.bin", "reader", ""),
			200, "b next")
		wantFetched(t, tc.name+" miss", w.do(http.MethodGet, "/artifactory/pair/none.bin", "reader", ""),
			404, errorBody(t, 404, "Could not find resource; Path: 'pair:none.bin'"))
		if n := len(requestsFor(w.upA, "/next.bin")) + len(requestsFor(w.upA, "/none.bin")); n != 0 {
			t.Fatalf("%s remote asked %d times", tc.name, n)
		}
		w.update(a)
		if rec := w.do(http.MethodDelete, "/artifactory/remote-b-cache/next.bin", "admin", ""); rec.Code != 204 {
			t.Fatalf("clear remote-b's cache: %d %s", rec.Code, rec.Body)
		}
	}

	// An upstream miss and an upstream failure pass over the member; each
	// upstream is asked once.
	w.upA.Set("/broken.bin", fakeupstream.Resource{Status: 500, Body: []byte("boom")})
	w.upB.Set("/broken.bin", fakeupstream.Resource{Body: []byte("b fallback")})
	wantFetched(t, "upstream failure", w.do(http.MethodGet, "/artifactory/pair/broken.bin", "reader", ""),
		200, "b fallback")
	wantFetched(t, "upstream misses", w.do(http.MethodGet, "/artifactory/pair/none.bin", "reader", ""),
		404, errorBody(t, 404, "Could not find resource; Path: 'pair:none.bin'"))
	if len(requestsFor(w.upA, "/none.bin")) != 1 || len(requestsFor(w.upB, "/none.bin")) != 1 {
		t.Fatalf("upstream misses asked a %d, b %d times",
			len(requestsFor(w.upA, "/none.bin")), len(requestsFor(w.upB, "/none.bin")))
	}

	// A caller who may not read remote-a: its upstream is not asked, and
	// the walk answers remote-a's refusal (decision 0008, R3).
	w.upA.Set("/secret.bin", fakeupstream.Resource{Body: []byte("secret")})
	wantFetched(t, "outsider", w.do(http.MethodGet, "/artifactory/virt/secret.bin", "outsider", ""),
		403, errorBody(t, 403, forbiddenIn("remote-a-cache", "virt", "secret.bin", "outsider")))
	// A caller who may read remote-a but not fill its cache is answered
	// remote-a's refusal of the fill, as a direct read of remote-a answers
	// it (decision 0008, R10).
	wantFetched(t, "viewer", w.do(http.MethodGet, "/artifactory/virt/secret.bin", "viewer", ""),
		403, errorBody(t, 403, "Rejected artifact download request: User viewer is not permitted "+
			"to deploy 'secret.bin' into 'remote-a-cache:secret.bin'."))
	direct := w.do(http.MethodGet, "/artifactory/remote-a/secret.bin", "viewer", "")
	wantFetched(t, "viewer direct", direct, 403, w.do(http.MethodGet, "/artifactory/virt/secret.bin",
		"viewer", "").Body.String())
	if rec := w.do(http.MethodGet, "/artifactory/remote-a-cache/secret.bin", "reader", ""); rec.Code != 404 {
		t.Fatalf("refused fill cached: %d", rec.Code)
	}
}

// A checksum file through the virtual answers the fetched file's
// checksum, and an archive entry the archive's entry, each fetched as a
// direct read of the member fetches it.
func virtualRemoteChecksumsAndArchives(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	// The checksum of the file the virtual fetched, from remote-a's cache;
	// under the pass-through checksum policy, the checksum the upstream's
	// answer for a cold file carries, read through remote-a's upstream step
	// as a direct read of remote-a reads it (#1495); and otherwise a cold
	// file's checksum is not found, as on a direct read.
	body := []byte("summed bytes")
	sum := sha1.Sum(body) //nolint:gosec // the SHA-1 checksum file's wire value
	sha := hex.EncodeToString(sum[:])
	passThru := repo.Config{
		Key: "remote-a", Class: repo.Remote, PackageType: repo.Generic, URL: w.upA.URL,
		RemoteRepoChecksumPolicyType: repo.PassThru,
	}
	w.update(passThru)
	for _, key := range []string{"remote-a", "virt"} {
		w.upA.Set("/"+key+"-sum.bin", fakeupstream.Resource{
			Body: body, Header: http.Header{"X-Checksum-Sha1": {sha}},
		})
		wantFetched(t, key+" cold sha1",
			w.do(http.MethodGet, "/artifactory/"+key+"/"+key+"-sum.bin.sha1", "reader", ""), 200, sha)
	}
	w.update(repo.Config{Key: "remote-a", Class: repo.Remote, PackageType: repo.Generic, URL: w.upA.URL})
	w.upA.Set("/fetched.bin", fakeupstream.Resource{Body: body})
	wantFetched(t, "fetch", w.do(http.MethodGet, "/artifactory/virt/fetched.bin", "reader", ""),
		200, string(body))
	wantFetched(t, "fetched sha1",
		w.do(http.MethodGet, "/artifactory/virt/fetched.bin.sha1", "reader", ""), 200, sha)
	for _, key := range []string{"remote-a", "virt"} {
		w.upA.Set("/"+key+"-nosum.bin", fakeupstream.Resource{Body: body})
		wantFetched(t, key+" no checksum",
			w.do(http.MethodGet, "/artifactory/"+key+"/"+key+"-nosum.bin.sha1", "reader", ""),
			404, errorBody(t, 404, "Checksum not found for "+key+"-nosum.bin"))
	}

	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create("inner/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("entry bytes")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"remote-a", "virt"} {
		path := "/" + key + ".zip"
		w.upA.Set(path, fakeupstream.Resource{Body: archive.Bytes()})
		wantFetched(t, key+" archive entry",
			w.do(http.MethodGet, "/artifactory/"+key+path+"!/inner/readme.txt", "reader", ""),
			200, "entry bytes")
		if n := w.upA.Count(http.MethodGet, path); n != 1 {
			t.Fatalf("%s archive fetched %d times", key, n)
		}
	}
	// A caller who may not fill the cache: the archive is not kept, and
	// the entry is not found (RemoteRepoBase.getRemoteZipRepoResource).
	w.upA.Set("/view.zip", fakeupstream.Resource{Body: archive.Bytes()})
	wantFetched(t, "viewer archive entry",
		w.do(http.MethodGet, "/artifactory/remote-a/view.zip!/inner/readme.txt", "viewer", ""),
		404, errorBody(t, 404,
			"Could not download 'inner/readme.txt' from main zip resource; Path: 'remote-a:view.zip'"))
}

// A Maven virtual over a Maven remote serves a cold JAR and POM.
func virtualRemoteMaven(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	for path, body := range map[string]string{
		"org/acme/app/1.0/app-1.0.jar": "jar bytes",
		"org/acme/app/1.0/app-1.0.pom": "<project/>",
	} {
		w.upMaven.Set("/"+path, fakeupstream.Resource{Body: []byte(body)})
		wantFetched(t, path, w.do(http.MethodGet, "/artifactory/maven-virt/"+path, "reader", ""),
			200, body)
		if n := w.upMaven.Count(http.MethodGet, "/"+path); n != 1 {
			t.Fatalf("%s fetched %d times", path, n)
		}
	}
}

// A trace through the virtual names each step, the upstream steps after
// every cache, and what each one found.
func virtualRemoteTrace(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	w.upA.Set("/trace.bin", fakeupstream.Resource{Status: 404})
	w.upB.Set("/trace.bin", fakeupstream.Resource{Body: []byte("traced")})
	rec := w.do(http.MethodGet, "/artifactory/pair/trace.bin?trace", "reader", "")
	_, steps, ok := strings.Cut(rec.Body.String(), "Steps: \n")
	if rec.Code != 200 || !ok {
		t.Fatalf("trace: %d %q", rec.Code, rec.Body)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSuffix(steps, "\n"), "\n") {
		_, msg, _ := strings.Cut(line, " ")
		got = append(got, msg)
	}
	wantSteps(t, "trace", got,
		"Received request",
		"Retrieving info from virtual repository 'pair' type generic",
		"Searching for the resource within remote-a-cache",
		"Unable to find resource in remote-a-cache:trace.bin",
		"Searching for the resource within remote-b-cache",
		"Unable to find resource in remote-b-cache:trace.bin",
		"Searching for the resource within remote-a",
		"Resolving remote source with current read authorization",
		"Unable to find resource in remote-a:trace.bin",
		"Searching for the resource within remote-b",
		"Resolving remote source with current read authorization",
		"Returning found resource from remote-b-cache:trace.bin",
		"Responding with found resource",
		"Request succeeded",
	)
}

// A copy a remote's cache holds that has expired is passed over in the
// remote's cache step, as 7.161.26's DbCacheRepo.getInfo returns it as an
// ExpiredRepoResource, which processStandard does not take for found: a
// later member's fresh copy serves, and otherwise the remote's own upstream
// step reads the file as a direct read of the remote does. A release never
// expires, nor does an offline remote's copy (DbCacheRepo.isExpired).
func virtualRemoteExpired(t *testing.T, w *virtualRemoteWorld) {
	t.Helper()
	upB := fakeupstream.New(t, fakeupstream.Options{})
	// maven-remote's snapshots expire as soon as they are fetched.
	w.update(repo.Config{
		Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven, URL: w.upMaven.URL,
		RetrievalCachePeriodSecs: ptr(0),
	})
	for _, c := range []repo.Config{
		{Key: "maven-remote-b", Class: repo.Remote, PackageType: repo.Maven, URL: upB.URL},
		{
			Key: "maven-pair", Class: repo.Virtual, PackageType: repo.Maven,
			Repositories: []string{"maven-remote", "maven-remote-b"},
		},
	} {
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.rt.Store.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "maven-remote-b",
		Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
					"reader": accessapi.ActionRead | accessapi.ActionWrite,
				}},
				Targets: map[string]accesstest.Target{"maven-remote-b": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// snapshot is the path of artifact's non-unique snapshot jar, which
	// Maven classifies as a mutable file.
	snapshot := func(artifact string) string {
		return "org/acme/" + artifact + "/1.0-SNAPSHOT/" + artifact + "-1.0-SNAPSHOT.jar"
	}
	fill := func(up *fakeupstream.Server, key, artifact, body string) {
		t.Helper()
		up.Set("/"+snapshot(artifact), fakeupstream.Resource{Body: []byte(body)})
		wantFetched(t, key+" "+artifact, w.do(http.MethodGet,
			"/artifactory/"+key+"/"+snapshot(artifact), "reader", ""), 200, body)
	}
	asked := func(up *fakeupstream.Server, artifact string) int {
		return len(requestsFor(up, "/"+snapshot(artifact)))
	}

	// A later member's fresh copy serves, and maven-remote's upstream is
	// not asked.
	fill(w.upMaven, "maven-remote", "both", "a v1")
	fill(upB, "maven-remote-b", "both", "b v1")
	w.upMaven.Set("/"+snapshot("both"), fakeupstream.Resource{Body: []byte("a v2")})
	before := asked(w.upMaven, "both")
	wantFetched(t, "fresh copy", w.do(http.MethodGet, "/artifactory/maven-pair/"+snapshot("both"),
		"reader", ""), 200, "b v1")
	trace := w.do(http.MethodGet, "/artifactory/maven-pair/"+snapshot("both")+"?trace", "reader", "")
	if n := asked(w.upMaven, "both"); n != before {
		t.Fatalf("maven-remote's upstream asked %d times for an expired copy a later cache holds", n-before)
	}
	at := 0
	for _, step := range []string{
		"Searching for the resource within maven-remote-cache",
		"Found the resource in the cache - checking for expiry",
		"Returning resource as expired",
		"Searching for the resource within maven-remote-b-cache",
		"Found the resource in the cache - checking for expiry",
		"Returning cached resource",
		"Returning found resource from maven-remote-b-cache:" + snapshot("both"),
	} {
		i := strings.Index(trace.Body.String()[at:], step)
		if i < 0 {
			t.Fatalf("trace: no %q after offset %d in\n%s", step, at, trace.Body)
		}
		at += i + len(step)
	}

	// The expired copy alone: maven-remote's upstream step revalidates it
	// with the requests a direct read of maven-remote makes, and serves the
	// newer file.
	for _, artifact := range []string{"alone", "alone-direct"} {
		fill(w.upMaven, "maven-remote", artifact, "a v1")
		w.upMaven.Set("/"+snapshot(artifact), fakeupstream.Resource{Body: []byte("a v2")})
	}
	filled := upstreamMethods(w.upMaven, "/"+snapshot("alone"))
	wantFetched(t, "expired copy", w.do(http.MethodGet,
		"/artifactory/maven-pair/"+snapshot("alone"), "reader", ""), 200, "a v2")
	wantFetched(t, "direct expired copy", w.do(http.MethodGet,
		"/artifactory/maven-remote/"+snapshot("alone-direct"), "reader", ""), 200, "a v2")
	walkedAsked, directAsked := upstreamMethods(w.upMaven, "/"+snapshot("alone")),
		upstreamMethods(w.upMaven, "/"+snapshot("alone-direct"))
	if walkedAsked == filled || walkedAsked != directAsked {
		t.Fatalf("expired copy: upstream asked %q through the walk, %q directly",
			walkedAsked, directAsked)
	}
	if n := asked(upB, "alone"); n != 0 {
		t.Fatalf("maven-remote-b's upstream asked %d times after maven-remote served", n)
	}

	// The upstream lacks the file now: maven-remote's upstream step finds
	// nothing, and the walk serves the expired copy, as 7.161.26's
	// RemoteRepoBase.internalGetInfo returns it (535-539).
	fill(w.upMaven, "maven-remote", "gone", "a v1")
	w.upMaven.Set("/"+snapshot("gone"), fakeupstream.Resource{Status: http.StatusNotFound})
	before = asked(w.upMaven, "gone")
	wantFetched(t, "upstream miss", w.do(http.MethodGet,
		"/artifactory/maven-pair/"+snapshot("gone"), "reader", ""), 200, "a v1")
	if asked(w.upMaven, "gone") == before {
		t.Fatal("upstream miss: the expired copy served without asking maven-remote's upstream")
	}

	// A release never expires: its copy serves from the cache step.
	w.upMaven.Set("/org/acme/rel/1.0/rel-1.0.jar", fakeupstream.Resource{Body: []byte("release")})
	wantFetched(t, "release fill", w.do(http.MethodGet,
		"/artifactory/maven-remote/org/acme/rel/1.0/rel-1.0.jar", "reader", ""), 200, "release")
	before = len(requestsFor(w.upMaven, "/org/acme/rel/1.0/rel-1.0.jar"))
	wantFetched(t, "release", w.do(http.MethodGet,
		"/artifactory/maven-pair/org/acme/rel/1.0/rel-1.0.jar", "reader", ""), 200, "release")
	if n := len(requestsFor(w.upMaven, "/org/acme/rel/1.0/rel-1.0.jar")) - before; n != 0 {
		t.Fatalf("maven-remote's upstream asked %d times for a release", n)
	}

	// An offline remote's copy never expires.
	fill(w.upMaven, "maven-remote", "offline", "a v1")
	fill(upB, "maven-remote-b", "offline", "b v1")
	w.update(repo.Config{
		Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven, URL: w.upMaven.URL,
		RetrievalCachePeriodSecs: ptr(0), Offline: ptr(true),
	})
	wantFetched(t, "offline copy", w.do(http.MethodGet,
		"/artifactory/maven-pair/"+snapshot("offline"), "reader", ""), 200, "a v1")
}
