package pypi_test

import (
	"net/http"
	"strings"
	"testing"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// demoMeta is the core metadata of demo 1.0's wheel, whose Requires-Python
// needs escaping in HTML.
var demoMeta = metadata("Demo", "1.0", "Summary: The demo\nRequires-Python: <4,>=3.9")

// seedDemo deploys the files the page tests read:
//   - demo 1.0's wheel and sdist at their canonical paths;
//   - demo 1.5's wheel, yanked without a reason;
//   - demo 2.0's wheel in a folder of the deployer's choosing, whose name
//     needs escaping in a link, yanked with a reason that needs escaping in
//     HTML;
//   - demo 3.0, a zip indexed by its properties alone;
//   - Other.Pkg 0.1's wheel, project other-pkg;
//   - a text file, which no index holds.
func seedDemo(t *testing.T, f *fixture) {
	t.Helper()
	f.deploy(t, "pypi-local", "demo/1.0/demo-1.0-py3-none-any.whl",
		wheel(t, "demo", "1.0", demoMeta), nil)
	f.deploy(t, "pypi-local", "demo/1.0/demo-1.0.tar.gz",
		sdist(t, "demo", "1.0", metadata("demo", "1.0", "Requires-Python: >=3.9")), nil)
	f.deploy(t, "pypi-local", "demo/1.5/demo-1.5-py3-none-any.whl",
		wheel(t, "demo", "1.5", metadata("demo", "1.5", "")),
		types.Properties{pypifmt.PropYanked: {"true"}})
	f.deploy(t, "pypi-local", "uploads/x y/demo-2.0-py3-none-any.whl",
		wheel(t, "demo", "2.0", metadata("demo", "2.0", "")),
		types.Properties{pypifmt.PropYanked: {`broken <b>"2.0"</b>`}})
	f.deploy(t, "pypi-local", "manual/demo-3.0.zip", []byte("not a zip"),
		types.Properties{pypifmt.PropName: {"demo"}, pypifmt.PropVersion: {"3.0"}})
	f.deploy(t, "pypi-local", "other/0.1/Other.Pkg-0.1-py3-none-any.whl",
		wheel(t, "Other.Pkg", "0.1", metadata("Other.Pkg", "0.1", "")), nil)
	f.deploy(t, "pypi-local", "notes.txt", []byte("notes"), nil)
}

// The pages in both forms, with and without PEP 658's attributes, as golden
// files: the project list, and demo's page, ordered by version and then
// file name, with relative links to wherever the files are stored.
func TestAccPages(t *testing.T) {
	for _, core := range []bool{false, true} {
		f := newFixture(t, pypifmt.Settings{SimpleJSON: true, CoreMetadata: core})
		seedDemo(t, f)
		suffix := ""
		if core {
			suffix = "-core"
		}
		for _, c := range []struct {
			path, accept, golden, contentType string
		}{
			{api + "simple/", "text/html", "list.html", "text/html"},
			{api + "simple/", pipAccept, "list.json", "application/vnd.pypi.simple.v1+json"},
			{api + "simple/demo/", "text/html", "demo" + suffix + ".html", "text/html"},
			{
				api + "simple/demo/", pipAccept, "demo" + suffix + ".json",
				"application/vnd.pypi.simple.v1+json",
			},
		} {
			rec := f.do(http.MethodGet, c.path, "reader", "Accept", c.accept)
			wantStatus(t, rec, http.StatusOK)
			if got := rec.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("%s: Content-Type %q, want %q", c.golden, got, c.contentType)
			}
			if c.golden == "list.html" || c.golden == "list.json" {
				if core {
					continue
				}
			}
			golden(t, c.golden, rec.Body.Bytes())
		}
	}
}

// An empty repository's list is an empty page; a project no file names
// is 404.
func TestAccEmptyRepository(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{SimpleJSON: true})
	rec := f.do(http.MethodGet, api+"simple/", "reader")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "<body>\n</body>") {
		t.Errorf("empty list: %s", rec.Body)
	}
	rec = f.do(http.MethodGet, api+"simple/", "reader", "Accept", pipAccept)
	if got := rec.Body.String(); got != `{"meta":{"api-version":"1.1"},"projects":[]}`+"\n" {
		t.Errorf("empty JSON list: %s", got)
	}
	wantStatus(t, f.do(http.MethodGet, api+"simple/demo/", "reader"), http.StatusNotFound)
}

// JSON is answered only with pypi.simple_json on, and then only to a
// request whose Accept ranks a JSON type at least as high as any HTML type
// it accepts.
func TestAccNegotiation(t *testing.T) {
	cases := []struct {
		accept string
		json   bool
	}{
		{pipAccept, true},
		{uvAccept, true},
		{"application/vnd.pypi.simple.latest+json", true},
		{"application/vnd.pypi.simple.v1+json;q=0.5, text/html;q=0.5", true},
		{"application/vnd.pypi.simple.v1+json;q=0.5, text/html;q=0.9", false},
		{"application/vnd.pypi.simple.v1+json;q=0.5, */*", false},
		{"application/vnd.pypi.simple.v1+json;q=0", false},
		{"application/vnd.pypi.simple.v1+json;q=x, text/html", false},
		{"application/vnd.pypi.simple.v1+html", false},
		{"application/vnd.pypi.simple.latest+html, application/*;q=0.1", false},
		{"text/html", false},
		{"*/*", false},
		{"application/*", false},
		{"not a type,, ;", false},
		{"", false},
	}
	for _, on := range []bool{false, true} {
		f := newFixture(t, pypifmt.Settings{SimpleJSON: on})
		seedDemo(t, f)
		for _, c := range cases {
			for _, path := range []string{api + "simple/", api + "simple/demo/"} {
				rec := f.do(http.MethodGet, path, "reader", "Accept", c.accept)
				wantStatus(t, rec, http.StatusOK)
				want := "text/html"
				if on && c.json {
					want = "application/vnd.pypi.simple.v1+json"
				}
				if got := rec.Header().Get("Content-Type"); got != want {
					t.Errorf(
						"setting %v, Accept %q, %s: %q, want %q",
						on,
						c.accept,
						path,
						got,
						want,
					)
				}
				if rec.Header().Get("Vary") != "Accept" {
					t.Errorf("%s: no Vary: Accept", path)
				}
			}
		}
	}
}

// /simple and every page URL without its slash redirect to the page's own
// URL, as does a name that is not normalised; the Location is absolute,
// from the base URL resolver, which honours X-JFrog-Override-Base-Url.
func TestAccRedirects(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	seedDemo(t, f)
	for _, c := range []struct{ path, location string }{
		{api + "simple", base + api + "simple/"},
		{api + "simple/demo", base + api + "simple/demo/"},
		{api + "simple/Demo/", base + api + "simple/demo/"},
		{api + "simple/Other_Pkg", base + api + "simple/other-pkg/"},
		{api + "simple/other..PKG/", base + api + "simple/other-pkg/"},
		// A project the index lacks redirects too: the redirect says
		// nothing of what is there.
		{api + "simple/Absent", base + api + "simple/absent/"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := f.do(method, c.path, "nobody")
			wantStatus(t, rec, http.StatusFound)
			if got := rec.Header().Get("Location"); got != c.location {
				t.Errorf("%s %s: Location %q, want %q", method, c.path, got, c.location)
			}
		}
	}
	rec := f.do(http.MethodGet, api+"simple", "reader",
		"X-JFrog-Override-Base-Url", "https://proxy.example/art")
	if got := rec.Header().Get("Location"); got != "https://proxy.example/art"+api+"simple/" {
		t.Errorf("override: Location %q", got)
	}
	// A name that is no project name, and a path below a project, name no
	// page.
	for _, path := range []string{api + "simple/-demo-/", api + "simple/demo/x/", api + "simple/a%20b"} {
		wantStatus(t, f.do(http.MethodGet, path, "reader"), http.StatusNotFound)
	}
}

// The pages carry X-Checksum-Sha1 and an ETag equal to it, unquoted, over
// the body sent, and answer 304 to a matching If-None-Match, quoted or
// not; the forms have their own ETags.
func TestAccPageETags(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{SimpleJSON: true})
	seedDemo(t, f)
	page := api + "simple/demo/"
	rec := f.do(http.MethodGet, page, "reader")
	tag := rec.Header().Get("ETag")
	if tag == "" || tag != rec.Header().Get("X-Checksum-Sha1") ||
		rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers %v", rec.Header())
	}
	for _, match := range []string{tag, `"` + tag + `"`, `W/"` + tag + `", "other"`, "*"} {
		rec := f.do(http.MethodGet, page, "reader", "If-None-Match", match)
		wantStatus(t, rec, http.StatusNotModified)
		if rec.Header().Get("ETag") != tag || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: %v %q", match, rec.Header(), rec.Body)
		}
	}
	wantStatus(t, f.do(http.MethodGet, page, "reader", "If-None-Match", "other"), http.StatusOK)
	if json := f.do(
		http.MethodGet,
		page,
		"reader",
		"Accept",
		pipAccept,
	); json.Header().
		Get("ETag") ==
		tag {
		t.Error("the JSON page has the HTML page's ETag")
	}
	// An upload changes the page, and its ETag.
	f.deploy(t, "pypi-local", "demo/4.0/demo-4.0-py3-none-any.whl",
		wheel(t, "demo", "4.0", metadata("demo", "4.0", "")), nil)
	wantStatus(t, f.do(http.MethodGet, page, "reader", "If-None-Match", tag), http.StatusOK)
	// HEAD answers the headers alone.
	head := f.do(http.MethodHead, page, "reader")
	wantStatus(t, head, http.StatusOK)
	if head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD headers %v", head.Header())
	}
}

// The pages show a caller only the files it may read; a project with none
// is 404, and a caller who may read nothing in the repository is refused,
// anonymous with 401 so pip sends credentials.
func TestAccPagesFollowPermissions(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	seedDemo(t, f)
	rec := f.do(http.MethodGet, api+"simple/demo/", "partial")
	wantStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, "demo-1.0.tar.gz") || strings.Contains(body, "demo-1.5") ||
		strings.Contains(body, "demo-2.0") {
		t.Errorf("partial's page: %s", body)
	}
	rec = f.do(http.MethodGet, api+"simple/", "partial")
	if !strings.Contains(rec.Body.String(), ">demo<") ||
		strings.Contains(rec.Body.String(), "other-pkg") {
		t.Errorf("partial's list: %s", rec.Body)
	}
	wantStatus(t, f.do(http.MethodGet, api+"simple/other-pkg/", "partial"), http.StatusNotFound)
	for _, c := range []struct {
		path, user string
		expected   int
	}{
		{api + "simple/", "nobody", http.StatusForbidden},
		{api + "simple/demo/", "nobody", http.StatusForbidden},
		{api + "simple/absent/", "nobody", http.StatusForbidden},
		{api + "simple/demo/", "anonymous", http.StatusUnauthorized},
		{api + "simple/", "", http.StatusUnauthorized},
		{api + "simple/absent/", "reader", http.StatusNotFound},
	} {
		wantStatus(t, f.do(http.MethodGet, c.path, c.user), c.expected)
	}
}

// A federated repository is served as a local one; a remote or virtual one
// is P9's and P10's, and another package type's is not PyPI's.
func TestAccRepositoryClasses(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	f.deploy(t, "pypi-fed", "demo/1.0/demo-1.0-py3-none-any.whl",
		wheel(t, "demo", "1.0", demoMeta), nil)
	wantStatus(t, f.do(http.MethodGet, "/artifactory/api/pypi/pypi-fed/simple/demo/", "reader"),
		http.StatusOK)
	wantStatus(t, f.do(http.MethodGet, "/artifactory/api/pypi/pypi-remote/simple/demo/", "admin"),
		http.StatusNotImplemented)
	wantStatus(t, f.do(http.MethodGet, "/artifactory/api/pypi/libs-local/simple/", "admin"),
		http.StatusNotFound)
	wantStatus(t, f.do(http.MethodGet, "/artifactory/api/pypi/missing/simple/", "admin"),
		http.StatusNotFound)
}
