package pypi_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
)

// A file path downloads through the item routes' download, wherever the
// file is stored; a folder, the repository's root and a missing file are
// 404, and a caller who may not read the file is refused.
func TestAccDownloads(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	seedDemo(t, f)
	for _, c := range []struct {
		method, path, user string
		expected           int
		served             string
	}{
		{
			http.MethodGet, api + "demo/1.0/demo-1.0-py3-none-any.whl", "reader", 200,
			"pypi-local/demo/1.0/demo-1.0-py3-none-any.whl",
		},
		{
			http.MethodHead, api + "demo/1.0/demo-1.0.tar.gz", "reader", 200,
			"pypi-local/demo/1.0/demo-1.0.tar.gz",
		},
		// The link the page gives, resolved against the page's URL.
		{
			http.MethodGet, api + "uploads/x%20y/demo-2.0-py3-none-any.whl", "reader", 200,
			"pypi-local/uploads/x y/demo-2.0-py3-none-any.whl",
		},
		{http.MethodGet, api + "notes.txt", "reader", 200, "pypi-local/notes.txt"},
		{http.MethodGet, api + "demo/1.0/", "reader", 404, ""},
		{http.MethodGet, api + "demo/1.0", "reader", 404, ""},
		{http.MethodGet, api, "reader", 404, ""},
		{http.MethodGet, "/artifactory/api/pypi/pypi-local", "reader", 404, ""},
		{http.MethodGet, api + "demo/9.9/demo-9.9.tar.gz", "reader", 404, ""},
		// 6.x's link form is not served.
		{http.MethodGet, api + "packages/demo/1.0/demo-1.0.tar.gz", "reader", 404, ""},
		{
			http.MethodGet, api + "demo/1.0/demo-1.0.tar.gz", "partial", 200,
			"pypi-local/demo/1.0/demo-1.0.tar.gz",
		},
		{http.MethodGet, api + "demo/1.5/demo-1.5-py3-none-any.whl", "partial", 403, ""},
		{http.MethodGet, api + "demo/1.0/demo-1.0.tar.gz", "anonymous", 401, ""},
		{http.MethodGet, api + "demo/1.0/demo-1.0.tar.gz", "", 401, ""},
		{http.MethodGet, api + "demo/x;=v", "reader", 400, ""},
		// Without pypi.core_metadata, .metadata is an ordinary path.
		{http.MethodGet, api + "demo/1.0/demo-1.0-py3-none-any.whl.metadata", "reader", 404, ""},
	} {
		f.served = nil
		rec := f.do(c.method, c.path, c.user)
		if rec.Code != c.expected {
			t.Errorf(
				"%s %s as %q: %d, want %d: %s",
				c.method,
				c.path,
				c.user,
				rec.Code,
				c.expected,
				rec.Body,
			)
			continue
		}
		var want []string
		if c.served != "" {
			want = []string{c.served}
		}
		if !slices.Equal(f.served, want) {
			t.Errorf("%s %s: served %v, want %v", c.method, c.path, f.served, want)
		}
	}
}

// With pypi.core_metadata on, a wheel's path plus .metadata answers the
// core metadata the index keeps, byte for byte, under the wheel's Read
// permission, with the pages' caching headers. An sdist, a wheel without
// whole METADATA and a missing wheel have none, so the path is an ordinary
// item path; a file deployed there is not served while the wheel has
// metadata.
func TestAccCoreMetadataFiles(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{CoreMetadata: true})
	seedDemo(t, f)
	whl := api + "demo/1.0/demo-1.0-py3-none-any.whl.metadata"
	f.deploy(t, "pypi-local", "demo/1.0/demo-1.0-py3-none-any.whl.metadata", []byte("stale"), nil)
	rec := f.do(http.MethodGet, whl, "reader")
	wantStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); got != demoMeta {
		t.Errorf("metadata %q, want %q", got, demoMeta)
	}
	if rec.Header().Get("Content-Type") != "application/octet-stream" ||
		rec.Header().Get("ETag") == "" || rec.Header().Get("Vary") != "" {
		t.Errorf("headers %v", rec.Header())
	}
	wantStatus(t, f.do(http.MethodGet, whl, "reader", "If-None-Match", rec.Header().Get("ETag")),
		http.StatusNotModified)
	wantStatus(t, f.do(http.MethodHead, whl, "reader"), http.StatusOK)
	// The hash the page advertises is the served file's.
	page := f.do(http.MethodGet, api+"simple/demo/", "reader").Body.String()
	if want := `data-core-metadata="sha256=` + sha256Hex(
		[]byte(demoMeta),
	) + `"`; !strings.Contains(
		page,
		want,
	) {
		t.Errorf("page lacks %s:\n%s", want, page)
	}
	for _, c := range []struct {
		path, user string
		expected   int
	}{
		{whl, "partial", 200},
		{api + "demo/1.5/demo-1.5-py3-none-any.whl.metadata", "partial", 403},
		{whl, "nobody", 403},
		{whl, "anonymous", 401},
		{api + "demo/1.0/demo-1.0.tar.gz.metadata", "reader", 404},
		{api + "demo/9.9/demo-9.9-py3-none-any.whl.metadata", "reader", 404},
		{api + "demo/1.0.whl.metadata", "reader", 404},
	} {
		wantStatus(t, f.do(http.MethodGet, c.path, c.user), c.expected)
	}
	// A wheel the index holds only by its properties has no core metadata:
	// its .metadata is the file deployed there.
	f.deploy(t, "pypi-local", "manual/demo-5.0-py3-none-any.whl", []byte("not a zip"),
		map[string][]string{pypifmt.PropName: {"demo"}, pypifmt.PropVersion: {"5.0"}})
	f.deploy(t, "pypi-local", "manual/demo-5.0-py3-none-any.whl.metadata", []byte("mine"), nil)
	f.served = nil
	wantStatus(t, f.do(http.MethodGet, api+"manual/demo-5.0-py3-none-any.whl.metadata", "reader"),
		http.StatusOK)
	if !slices.Equal(f.served, []string{"pypi-local/manual/demo-5.0-py3-none-any.whl.metadata"}) {
		t.Errorf("served %v", f.served)
	}
	// A wheel no index holds has none either.
	f.deploy(t, "pypi-local", "loose/x.whl", []byte("loose"), nil)
	wantStatus(t, f.do(http.MethodGet, api+"loose/x.whl.metadata", "reader"), http.StatusNotFound)
}
