package nuget_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/internal/testfault"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

const v2 = "/artifactory/api/nuget/nuget-local"

// pushForm is the body PackageUpdateResource.CreateRequest sends: one
// part named package, file name package.nupkg, as .NET's
// MultipartFormDataContent writes its headers, under a quoted boundary.
func pushForm(pkg []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition",
		`form-data; name=package; filename=package.nupkg; filename*=utf-8''package.nupkg`)
	w, _ := mw.CreatePart(h)
	_, _ = w.Write(pkg)
	_ = mw.Close()
	return &buf, `multipart/form-data; boundary="` + mw.Boundary() + `"`
}

// pushTo makes the client's push of pkg to url as user: a PUT with
// Transfer-Encoding chunked, as the client sends it, so it can stop when
// authentication fails.
func (f *fixture) pushTo(url, user string, pkg []byte) *httptest.ResponseRecorder {
	body, contentType := pushForm(pkg)
	r := httptest.NewRequest(http.MethodPut, url, io.NopCloser(body))
	r.ContentLength = -1
	r.TransferEncoding = []string{"chunked"}
	r.Header.Set("Content-Type", contentType)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// stored reports whether nuget-local holds a file at path.
func (f *fixture) stored(path string) bool {
	f.t.Helper()
	_, err := f.db.Items().Get(context.Background(), "nuget-local", false, path)
	return err == nil
}

// flatVersions are the versions the flat container lists for id to reader.
func (f *fixture) flatVersions(id string) []string {
	f.t.Helper()
	rec := f.do(http.MethodGet, v3+"/flatcontainer/"+strings.ToLower(id)+"/index.json", "reader")
	if rec.Code == http.StatusNotFound {
		return nil
	}
	var doc struct {
		Versions []string `json:"versions"`
	}
	decodeJSON(f.t, rec, &doc)
	return doc.Versions
}

// dotnet nuget push sends the package to PackagePublish with a trailing
// slash; it lands at its canonical name at the root, where the JFrog CLI
// looks for it, answering 201 with no body. A second push of the version
// answers 409, which --skip-duplicate accepts, to every caller.
func TestPushAsDotnetSends(t *testing.T) {
	f := newFixture(t)
	rec := f.pushTo(v2+"/", "deployer", nupkg("Acme.Lib", "1.0", ""))
	wantStatus(t, rec, http.StatusCreated)
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body)
	}
	if !f.stored("Acme.Lib.1.0.0.nupkg") {
		t.Fatal("not at its canonical name")
	}
	if got := f.flatVersions("Acme.Lib"); len(got) != 1 || got[0] != "1.0.0" {
		t.Fatalf("versions %v", got)
	}
	for _, user := range []string{"deployer", "writer", "admin"} {
		// The same version, however written, and the same content too.
		for _, pkg := range [][]byte{
			nupkg("Acme.Lib", "1.0", ""), nupkg("acme.lib", "1.0.0.0", "<title>x</title>"),
		} {
			rec := f.pushTo(v2+"/", user, pkg)
			wantStatus(t, rec, http.StatusConflict)
			if _, msg := envelope(t, rec); msg !=
				"The repository 'nuget-local' already contains Acme.Lib 1.0.0" &&
				msg != "The repository 'nuget-local' already contains acme.lib 1.0.0" {
				t.Fatalf("%s: %q", user, msg)
			}
		}
	}
	// Without the trailing slash, and into folders at any depth.
	wantStatus(t, f.pushTo(v2, "deployer", nupkg("Acme.Two", "1.0.0", "")), http.StatusCreated)
	wantStatus(t, f.pushTo(v2+"/team/sub/", "deployer", nupkg("Acme.Three", "1.0.0", "")),
		http.StatusCreated)
	if !f.stored("Acme.Two.1.0.0.nupkg") || !f.stored("team/sub/Acme.Three.1.0.0.nupkg") {
		t.Fatal("not where pushed")
	}
	// A version is held once, whichever folder brings it again.
	wantStatus(t, f.pushTo(v2+"/other/", "admin", nupkg("Acme.Three", "1.0.0", "")),
		http.StatusConflict)
	wantStatus(t, f.pushTo(v2+"/team/sub", "admin", nupkg("Acme.Three", "1.0.0", "")),
		http.StatusConflict)
}

// envelope decodes the Artifactory envelope's one error.
func envelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	decodeJSON(t, rec, &body)
	if len(body.Errors) != 1 {
		t.Fatalf("envelope %s", rec.Body)
	}
	return body.Errors[0].Status, body.Errors[0].Message
}

// A push is refused in decision 0038's order: who may deploy in the folder
// before the body is read, then the package, then who may deploy at its
// canonical path.
func TestPushRefusals(t *testing.T) {
	f := newFixture(t)
	pkg := nupkg("Acme.Lib", "1.0.0", "")
	// The body is not read: a body that fails if read would answer 400.
	unread := func(user, url string) *httptest.ResponseRecorder {
		body, contentType := pushForm(pkg)
		return f.doBody(http.MethodPut, url, user, testfault.Disconnect(body, 0),
			"Content-Type", contentType)
	}
	for _, c := range []struct {
		user, url string
		want      int
	}{
		{"nobody", v2 + "/", http.StatusForbidden},
		{"reader", v2 + "/", http.StatusForbidden},
		{"anonymous", v2 + "/", http.StatusUnauthorized},
		{"", v2 + "/", http.StatusUnauthorized},
		{"teamwriter", v2 + "/other/", http.StatusForbidden},
	} {
		rec := unread(c.user, c.url)
		wantStatus(t, rec, c.want)
		if c.want == http.StatusUnauthorized &&
			rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
			t.Fatalf("%s: 401 without the challenge", c.user)
		}
	}
	// teamwriter may deploy under team/ but not at the root, where the
	// package's canonical path is once the folder check passes.
	wantStatus(t, f.pushTo(v2+"/team/", "teamwriter", pkg), http.StatusCreated)
	wantStatus(t, f.pushTo(v2+"/", "teamwriter", nupkg("Acme.Root", "1.0.0", "")),
		http.StatusForbidden)

	for name, c := range map[string]struct {
		body        io.Reader
		contentType string
		want        int
	}{
		"not multipart": {strings.NewReader("{}"), "application/json", http.StatusUnsupportedMediaType},
		"no file part": {
			strings.NewReader("--b\r\nContent-Disposition: form-data; name=x\r\n\r\nv\r\n--b--\r\n"),
			"multipart/form-data; boundary=b", http.StatusBadRequest,
		},
		"broken off": {testfault.Disconnect(mustForm(pkg), 100), formType(pkg), http.StatusBadRequest},
	} {
		rec := f.doBody(http.MethodPut, v2+"/", "admin", c.body, "Content-Type", c.contentType)
		if rec.Code != c.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	for name, c := range map[string]struct {
		pkg     []byte
		message string
	}{
		"no zip": {[]byte("not a zip"), "The pushed file is not a valid NuGet package: it is no zip."},
		"no nuspec": {
			zipOf(zipEntry{"lib/a.dll", "MZ"}),
			"The pushed file is not a valid NuGet package: no .nuspec at its root.",
		},
		"bad id": {
			nupkg("bad id", "1.0.0", ""),
			`The pushed file is not a valid NuGet package: the .nuspec's id "bad id" is not a valid NuGet id.`,
		},
		"symbols": {
			snupkg("Acme.Sym", "1.0.0"),
			"Acme.Sym 1.0.0 is a symbols package; push it to " + base + v3 + "/symbols",
		},
	} {
		rec := f.pushTo(v2+"/", "admin", c.pkg)
		wantStatus(t, rec, http.StatusBadRequest)
		if _, msg := envelope(t, rec); msg != c.message {
			t.Errorf("%s: %q", name, msg)
		}
	}
	for url, want := range map[string]int{
		"/artifactory/api/nuget/nuget-remote/": http.StatusNotImplemented,
		"/artifactory/api/nuget/libs-local/":   http.StatusNotFound,
		"/artifactory/api/nuget/missing/":      http.StatusNotFound,
		// The JFrog operation's other methods.
		v2 + "/reindex": http.StatusMethodNotAllowed,
	} {
		wantStatus(t, f.pushTo(url, "admin", pkg), want)
	}
	wantStatus(t, f.pushTo(v2+"/a;=x/", "admin", pkg), http.StatusBadRequest)
}

func mustForm(pkg []byte) io.Reader { b, _ := pushForm(pkg); return b }

func formType(pkg []byte) string { _, ct := pushForm(pkg); return ct }

// A store that fails while the package is stored, or read, is a fault.
func TestPushFaults(t *testing.T) {
	f := newFixture(t)
	f.items.Blobs = testfault.Blobs{Store: f.blobs, Commit: errBroken}
	wantStatus(t, f.pushTo(v2+"/", "admin", nupkg("Acme.Lib", "1.0.0", "")),
		http.StatusInternalServerError)

	// The package is uploaded, then cannot be read back.
	o := newFixture(t)
	o.items.Blobs = unopenable{Store: o.blobs}
	wantStatus(t, o.pushTo(v2+"/", "admin", nupkg("Acme.Lib", "1.0.0", "")),
		http.StatusInternalServerError)

	g := newFixture(t)
	g.items.Store = failingStore{Store: g.db, failing: "lease"}
	wantStatus(t, g.pushTo(v2+"/", "admin", nupkg("Acme.Lib", "1.0.0", "")),
		http.StatusInternalServerError)
	if len(g.faults) == 0 {
		t.Fatal("no fault logged")
	}
}

// unopenable is a blob store whose content cannot be opened once stored.
type unopenable struct{ blob.Store }

func (unopenable) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errBroken
}

// A symbols package is stored at the root under its canonical name,
// whether or not its package is there; its version's symbols are pushed
// once.
func TestSymbolsPush(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.pushTo(v3+"/symbols/", "deployer", snupkg("Acme.Lib", "1.0.0")),
		http.StatusCreated)
	if !f.stored("Acme.Lib.1.0.0.snupkg") {
		t.Fatal("symbols not stored")
	}
	// No package yet, so no version to restore.
	if got := f.flatVersions("Acme.Lib"); got != nil {
		t.Fatalf("versions %v", got)
	}
	wantStatus(t, f.pushTo(v2+"/", "deployer", nupkg("Acme.Lib", "1.0.0", "")), http.StatusCreated)
	if got := f.flatVersions("Acme.Lib"); len(got) != 1 {
		t.Fatalf("versions %v", got)
	}
	rec := f.pushTo(v3+"/symbols", "admin", snupkg("acme.lib", "1.0"))
	wantStatus(t, rec, http.StatusConflict)
	if _, msg := envelope(t, rec); msg !=
		"The repository 'nuget-local' already contains the symbols of acme.lib 1.0.0" {
		t.Fatalf("message %q", msg)
	}
	// After its package, as dotnet nuget push sends it beside a .nupkg.
	wantStatus(t, f.pushTo(v2+"/", "deployer", nupkg("Acme.Two", "1.0.0", "")), http.StatusCreated)
	wantStatus(t, f.pushTo(v3+"/symbols/", "deployer", snupkg("Acme.Two", "1.0.0")),
		http.StatusCreated)

	rec = f.pushTo(v3+"/symbols/", "admin", nupkg("Acme.Three", "1.0.0", ""))
	wantStatus(t, rec, http.StatusBadRequest)
	if _, msg := envelope(t, rec); msg !=
		"Acme.Three 1.0.0 is no symbols package: it declares no SymbolsPackage package type" {
		t.Fatalf("message %q", msg)
	}
	wantStatus(t, f.pushTo(v3+"/symbols/", "admin", []byte("x")), http.StatusBadRequest)
	wantStatus(t, f.pushTo(v3+"/symbols/", "nobody", snupkg("Acme.Four", "1.0.0")),
		http.StatusForbidden)
	wantStatus(t, f.pushTo(v3+"/symbols/", "anonymous", snupkg("Acme.Four", "1.0.0")),
		http.StatusUnauthorized)
	// Symbols land at the root, where teamwriter may not deploy.
	wantStatus(t, f.pushTo(v3+"/symbols/", "teamwriter", snupkg("Acme.Four", "1.0.0")),
		http.StatusForbidden)
	wantStatus(t, f.do(http.MethodGet, v3+"/symbols", "admin"), http.StatusMethodNotAllowed)
}

// Concurrent pushes of one version into different folders store one
// package and refuse the rest with 409, on every store.
func TestConcurrentPushes(t *testing.T) {
	testConcurrentPushes(t, newFixture(t))
}

// On PostgreSQL, whose transactions read only what others committed, the
// format's check alone would let several through.
func TestConcurrentPushesOnPostgres(t *testing.T) {
	db, err := postgres.Open(context.Background(), sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqltest.Reset(t, db.DB(), postgres.Dialect)
	testConcurrentPushes(t, newFixtureOn(t, db))
}

func testConcurrentPushes(t *testing.T, f *fixture) {
	t.Helper()
	const pushers = 8
	for round := range 3 {
		ver := fmt.Sprintf("1.0.%d", round)
		pkg := nupkg("Acme.Race", ver, "")
		codes := make([]int, pushers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range pushers {
			wg.Go(func() {
				<-start
				codes[i] = f.pushTo(fmt.Sprintf("%s/f%d/", v2, i), "admin", pkg).Code
			})
		}
		close(start)
		wg.Wait()
		created, conflicts, files := 0, 0, 0
		for i, code := range codes {
			switch code {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflicts++
			}
			if f.stored(fmt.Sprintf("f%d/Acme.Race.%s.nupkg", i, ver)) {
				files++
			}
		}
		if created != 1 || conflicts != pushers-1 || files != 1 {
			t.Fatalf("round %d: %d created, %d refused, %d stored: %v",
				round, created, conflicts, files, codes)
		}
	}
}
