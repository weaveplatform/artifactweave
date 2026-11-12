package pypihttp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
)

// setPolicy sets key's blackout and include and exclude patterns; an empty
// include is the default, every path.
func (f *fixture) setPolicy(t *testing.T, key string, blackedOut bool, include, exclude string) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if include == "" {
		include = "**/*"
	}
	c.BlackedOut, c.IncludesPattern, c.ExcludesPattern = &blackedOut, &include, &exclude
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

// wantAnswer fails unless rec answered status with exactly body.
func wantAnswer(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, status, body)
	}
}

// blackedOut and rejected are the download policy's refusals of path in
// key, in Artifactory's envelope.
func blackedOut(key, path string) string {
	return `{"errors":[{"message":"The repository '` + key +
		`' is blacked out and cannot serve artifact '` + key + `:` + path + `'.","status":404}]}`
}

func rejected(key, path string) string {
	return `{"errors":[{"message":"The repository '` + key + `' rejected the artifact '` + key +
		`:` + path + `' due to its include/exclude pattern settings.","status":404}]}`
}

// A local repository's pages are checked at the paths 7.x keeps a remote's
// pages at (.pypi/<name>.html, .pypi/simple.html), and the JSON API by the
// blackout alone; a pattern on one file leaves the pages and the JSON
// listing it.
func TestAccLocalPagePolicy(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{SimpleJSON: true, CoreMetadata: true})
	seedDemo(t, f)
	const wheel = "demo/1.0/demo-1.0-py3-none-any.whl"

	f.setPolicy(t, "pypi-local", true, "", "")
	for path, want := range map[string]string{
		"simple/demo/":       blackedOut("pypi-local", ".pypi/demo.html"),
		"simple/":            blackedOut("pypi-local", ".pypi/simple.html"),
		"simple/absent/":     blackedOut("pypi-local", ".pypi/absent.html"),
		"pypi/demo/json":     blackedOut("pypi-local", "pypi/demo/json"),
		"pypi/Demo/1.0/json": blackedOut("pypi-local", "pypi/Demo/1.0/json"),
	} {
		for _, user := range []string{"reader", "admin"} {
			wantAnswer(t, f.do(http.MethodGet, api+path, user), http.StatusNotFound, want)
		}
		// Read permission first (DV-057).
		wantStatus(t, f.do(http.MethodGet, api+path, "nobody"), http.StatusForbidden)
	}
	rec := f.do(http.MethodHead, api+"simple/demo/", "reader")
	wantStatus(t, rec, http.StatusNotFound)
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "" {
		t.Errorf("a refused page sends the page's headers: %v", rec.Header())
	}

	for _, c := range []struct{ include, exclude, refused, served string }{
		{"", ".pypi/demo.html", "simple/demo/", "simple/"},
		{"", ".pypi/simple.html", "simple/", "simple/demo/"},
		{"demo/**", "", "simple/demo/", ""},
	} {
		f.setPolicy(t, "pypi-local", false, c.include, c.exclude)
		doc := ".pypi/demo.html"
		if c.refused == "simple/" {
			doc = ".pypi/simple.html"
		}
		wantAnswer(t, f.do(http.MethodGet, api+c.refused, "reader"), http.StatusNotFound,
			rejected("pypi-local", doc))
		if c.served != "" {
			wantStatus(t, f.do(http.MethodGet, api+c.served, "reader"), http.StatusOK)
		}
		// Only the blackout refuses the JSON API.
		wantStatus(t, f.do(http.MethodGet, api+"pypi/demo/1.0/json", "reader"), http.StatusOK)
	}

	// A pattern on one file leaves it listed, and refuses only its download
	// (RTFACT-24511).
	f.setPolicy(t, "pypi-local", false, "", "**/demo-1.0-py3-none-any.whl")
	if page := f.do(http.MethodGet, api+"simple/demo/", "reader").Body.String(); !strings.Contains(
		page, "demo-1.0-py3-none-any.whl") {
		t.Errorf("page lacks the refused wheel:\n%s", page)
	}
	rec = f.do(http.MethodGet, api+"pypi/demo/1.0/json", "reader")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"filename":"demo-1.0-py3-none-any.whl"`) {
		t.Errorf("JSON lacks the refused wheel: %s", rec.Body)
	}
	wantAnswer(t, f.do(http.MethodGet, api+wheel, "reader"), http.StatusNotFound,
		rejected("pypi-local", wheel))
}

// A wheel's core metadata, the index's or a file stored at its path, takes
// the wheel's download policy, as decision 0032 checks a sidecar at its
// file: refused with the wheel, naming it, and not by a pattern on
// .metadata paths. A .metadata file of no wheel is a file like any other.
func TestAccCoreMetadataPolicy(t *testing.T) {
	const (
		computed = "demo/1.0/demo-1.0-py3-none-any.whl"
		stored   = "manual/demo-5.0-py3-none-any.whl"
		notes    = "manual/notes.metadata"
	)
	f := newFixture(t, pypifmt.Settings{CoreMetadata: true})
	seedDemo(t, f)
	f.deploy(t, "pypi-local", stored, []byte("not a zip"),
		map[string][]string{pypifmt.PropName: {"demo"}, pypifmt.PropVersion: {"5.0"}})
	f.deploy(t, "pypi-local", stored+".metadata", []byte("mine"), nil)
	f.deploy(t, "pypi-local", notes, []byte("notes"), nil)

	f.setPolicy(t, "pypi-local", true, "", "")
	for _, wheel := range []string{computed, stored} {
		wantAnswer(t, f.do(http.MethodGet, api+wheel+".metadata", "reader"), http.StatusNotFound,
			blackedOut("pypi-local", wheel))
		wantStatus(t, f.do(http.MethodGet, api+wheel+".metadata", "nobody"), http.StatusForbidden)
	}
	wantAnswer(t, f.do(http.MethodGet, api+notes, "reader"), http.StatusNotFound,
		blackedOut("pypi-local", notes))

	for _, wheel := range []string{computed, stored} {
		f.setPolicy(t, "pypi-local", false, "", "**/"+wheel[strings.LastIndex(wheel, "/")+1:])
		wantAnswer(t, f.do(http.MethodGet, api+wheel+".metadata", "reader"), http.StatusNotFound,
			rejected("pypi-local", wheel))
	}

	f.setPolicy(t, "pypi-local", false, "", "**/*.metadata")
	wantAnswer(t, f.do(http.MethodGet, api+computed+".metadata", "reader"), http.StatusOK,
		strings.TrimSpace(demoMeta))
	f.served = nil
	wantStatus(t, f.do(http.MethodGet, api+stored+".metadata", "reader"), http.StatusOK)
	if len(f.served) != 1 || f.served[0] != "pypi-local/"+stored+".metadata" {
		t.Errorf("served %v", f.served)
	}
	wantAnswer(t, f.do(http.MethodGet, api+notes, "reader"), http.StatusNotFound,
		rejected("pypi-local", notes))

	// With pypi.core_metadata off, a .metadata stored beside a wheel is
	// still that wheel's.
	off := newFixture(t, pypifmt.Settings{})
	seedDemo(t, off)
	off.deploy(t, "pypi-local", computed+".metadata", []byte("stored"), nil)
	off.setPolicy(t, "pypi-local", false, "", "**/demo-1.0-py3-none-any.whl")
	wantAnswer(t, off.do(http.MethodGet, api+computed+".metadata", "reader"),
		http.StatusNotFound, rejected("pypi-local", computed))
	off.setPolicy(t, "pypi-local", false, "", "")
	wantAnswer(t, off.do(http.MethodGet, api+computed+".metadata", "reader"), http.StatusOK,
		"served pypi-local/"+computed+".metadata")
}

// hashedProject serves name's versions from PyPI.org's layout,
// /packages/<a>/<b>/<rest of the hash>/<file>, and returns each wheel's
// path there by version.
func (f *remoteFixture) hashedProject(
	t *testing.T,
	name string,
	versions ...string,
) map[string]string {
	t.Helper()
	paths := map[string]string{}
	files := []map[string]any{}
	for _, v := range versions {
		filename := name + "-" + v + "-py3-none-any.whl"
		meta := metadata(name, v, "Requires-Python: >=3.8\n")
		body := wheel(t, name, v, meta)
		sum := sha256.Sum256(body)
		msum := sha256.Sum256([]byte(meta))
		hx := hex.EncodeToString(sum[:])
		paths[v] = "packages/" + hx[:2] + "/" + hx[2:4] + "/" + hx[4:] + "/" + filename
		files = append(files, map[string]any{
			"filename":      filename,
			"url":           "/" + paths[v],
			"hashes":        map[string]string{"sha256": hx},
			"core-metadata": map[string]string{"sha256": hex.EncodeToString(msum[:])},
		})
		octets := http.Header{"Content-Type": []string{"application/octet-stream"}}
		f.up.Set("/"+paths[v], fakeupstream.Resource{Body: body, Header: octets})
		f.up.Set(
			"/"+paths[v]+".metadata",
			fakeupstream.Resource{Body: []byte(meta), Header: octets},
		)
	}
	f.setJSON("/custom/simple/"+name+"/", map[string]any{
		"meta": map[string]string{"api-version": "1.1"}, "name": name,
		"files": files, "versions": versions,
	})
	f.setJSON("/custom/simple/", map[string]any{
		"meta":     map[string]string{"api-version": "1.1"},
		"projects": []map[string]string{{"name": name}},
	})
	entries := map[string]any{}
	for i, file := range files {
		entries[versions[i]] = []any{map[string]any{
			"filename": file["filename"], "url": file["url"], "digests": file["hashes"],
		}}
	}
	f.setJSON("/pypi/"+name+"/json", map[string]any{
		"info":     map[string]string{"name": name, "version": versions[0]},
		"urls":     entries[versions[0]],
		"releases": entries,
	})
	return paths
}

// A remote repository's pages are checked at their 7.x cache paths, and
// its files at their upstream paths, which for PyPI.org hold the file's
// hash (RTFACT-31147); a download's read of the page checks the blackout
// alone, and a .metadata file takes its wheel's check.
func TestAccRemotePagePolicy(t *testing.T) {
	f := newRemoteFixture(t)
	hashed := f.hashedProject(t, "demo", "1.0", "2.0")
	const (
		wheel1 = "demo/1.0/demo-1.0-py3-none-any.whl"
		wheel2 = "demo/2.0/demo-2.0-py3-none-any.whl"
	)
	get := func(path string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, remoteRegistry+path, "reader", "Accept", pipAccept)
	}

	f.setPolicy(t, "pypi-remote", true, "", "")
	for path, want := range map[string]string{
		"simple/demo/":       blackedOut("pypi-remote", ".pypi/demo.html"),
		"simple/":            blackedOut("pypi-remote", ".pypi/simple.html"),
		wheel1:               blackedOut("pypi-remote", wheel1),
		wheel1 + ".metadata": blackedOut("pypi-remote", wheel1),
		// [I] Where 7.x caches the JSON API is not known: the project's
		// folder is checked, as before.
		"pypi/demo/json": blackedOut("pypi-remote", "demo"),
	} {
		wantAnswer(t, get(path), http.StatusNotFound, want)
	}
	// A file path that is no item path names the page it is read from.
	wantAnswer(t, get("demo//"+wheel1[strings.LastIndex(wheel1, "/")+1:]), http.StatusNotFound,
		blackedOut("pypi-remote", ".pypi/demo.html"))

	f.setPolicy(t, "pypi-remote", false, "", ".pypi/demo.html")
	wantAnswer(
		t,
		get("simple/demo/"),
		http.StatusNotFound,
		rejected("pypi-remote", ".pypi/demo.html"),
	)
	requireRemote(t, get("simple/"), http.StatusOK, `"name":"demo"`)
	wantStatus(t, get(wheel1), http.StatusOK)
	f.setPolicy(t, "pypi-remote", false, "", ".pypi/simple.html")
	wantAnswer(t, get("simple/"), http.StatusNotFound, rejected("pypi-remote", ".pypi/simple.html"))
	wantStatus(t, get("simple/demo/"), http.StatusOK)

	// A pattern on one file leaves every listing whole and refuses the
	// file, and its metadata, at the file's upstream path.
	f.setPolicy(t, "pypi-remote", false, "", "**/demo-1.0-py3-none-any.whl")
	requireRemote(t, get("simple/demo/"), http.StatusOK, "demo-1.0-py3-none-any.whl",
		"demo-2.0-py3-none-any.whl")
	requireRemote(t, get("simple/"), http.StatusOK, `"name":"demo"`)
	requireRemote(t, get("pypi/demo/json"), http.StatusOK, "demo-1.0-py3-none-any.whl")
	for _, path := range []string{wheel1, wheel1 + ".metadata"} {
		wantAnswer(t, get(path), http.StatusNotFound, rejected("pypi-remote", hashed["1.0"]))
	}
	wantStatus(t, get(wheel2), http.StatusOK)

	// A pattern on the project's folder matches neither the pages' paths
	// nor the files' (JFrog on RTFACT-17256), and one on PyPI.org's
	// packages/ folder refuses the files alone.
	f.setPolicy(t, "pypi-remote", false, "", "demo/**")
	for _, path := range []string{"simple/demo/", "simple/", wheel1, wheel1 + ".metadata"} {
		wantStatus(t, get(path), http.StatusOK)
	}
	f.setPolicy(t, "pypi-remote", false, "", "packages/**")
	wantStatus(t, get("simple/demo/"), http.StatusOK)
	wantAnswer(t, get(wheel2), http.StatusNotFound, rejected("pypi-remote", hashed["2.0"]))

	// Read permission is still checked at artifactweave's own paths.
	f.setPolicy(t, "pypi-remote", false, "", "")
	wantStatus(t, f.do(http.MethodGet, remoteRegistry+wheel2, "partial"), http.StatusForbidden)
	wantStatus(t, f.do(http.MethodGet, remoteRegistry+wheel1, "partial"), http.StatusOK)
}
