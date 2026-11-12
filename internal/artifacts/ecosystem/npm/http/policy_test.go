package npmhttp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
// key, in npm's envelope.
func blackedOut(key, path string) string {
	return `{"error":"The repository '` + key + `' is blacked out and cannot serve artifact '` +
		key + `:` + path + `'."}`
}

func rejected(key, path string) string {
	return `{"error":"The repository '` + key + `' rejected the artifact '` + key + `:` + path +
		`' due to its include/exclude pattern settings."}`
}

// documentReads are the reads 7.x answers from mixed's stored packument,
// .npm/mixed/package.json.
var documentReads = []string{"mixed", "mixed/1.0.0", "mixed/latest", "-/package/mixed/dist-tags"}

// A local repository's packument, version manifest and dist-tags are
// refused as 7.x refuses .npm/<name>/package.json, the document it answers
// them from (RTFACT-13337): by the blackout and by a pattern on that path,
// whether or not the package exists, and only after read permission.
func TestAccLocalDocumentPolicy(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "mixed", "1.0.0", `{"name":"mixed","version":"1.0.0"}`, nil)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0"}`, nil)
	f.deploy(t, "@acme/tool", "1.0.0", `{"name":"@acme/tool","version":"1.0.0"}`, nil)
	const doc = ".npm/mixed/package.json"

	f.setPolicy(t, "npm-local", true, "", "")
	for _, p := range documentReads {
		for _, user := range []string{"reader", "admin"} {
			wantAnswer(t, f.do(http.MethodGet, registry+p, user), http.StatusNotFound,
				blackedOut("npm-local", doc))
		}
	}
	rec := f.do(http.MethodHead, registry+"mixed", "reader")
	wantStatus(t, rec, http.StatusNotFound)
	if rec.Header().Get("ETag") != "" || rec.Header().Get("X-Checksum-Sha1") != "" {
		t.Errorf("a refusal sends the packument's validators: %v", rec.Header())
	}
	wantAnswer(t, f.do(http.MethodGet, registry+"absent", "reader"), http.StatusNotFound,
		blackedOut("npm-local", ".npm/absent/package.json"))
	wantAnswer(t, f.do(http.MethodGet, registry+"@acme%2ftool", "reader"), http.StatusNotFound,
		blackedOut("npm-local", ".npm/@acme/tool/package.json"))
	wantAnswer(t, f.do(http.MethodGet, registry+"mixed", "nobody"), http.StatusForbidden,
		`{"error":"Forbidden"}`)
	wantAnswer(t, f.do(http.MethodGet, registry+"mixed/-/mixed-1.0.0.tgz", "reader"),
		http.StatusNotFound, blackedOut("npm-local", "mixed/-/mixed-1.0.0.tgz"))
	// Search is computed, and 6.20's searchers do not check the blackout
	// (decision 0032): it keeps its answer [I].
	rec = f.do(http.MethodGet, registry+"-/v1/search?text=mixed", "reader")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"name":"mixed"`) {
		t.Errorf("search: %s", rec.Body)
	}

	// A pattern on the document's path, here the include of the tarballs
	// alone that npm's docs warn of, refuses the document.
	for _, c := range []struct{ include, exclude string }{
		{"", ".npm/mixed/**"},
		{"mixed/**", ""},
	} {
		f.setPolicy(t, "npm-local", false, c.include, c.exclude)
		for _, p := range documentReads {
			wantAnswer(t, f.do(http.MethodGet, registry+p, "reader"), http.StatusNotFound,
				rejected("npm-local", doc))
		}
	}
	f.setPolicy(t, "npm-local", false, "", ".npm/@acme/**")
	wantAnswer(t, f.do(http.MethodGet, registry+"@acme%2ftool/1.0.0", "reader"),
		http.StatusNotFound, rejected("npm-local", ".npm/@acme/tool/package.json"))
	wantStatus(t, f.do(http.MethodGet, registry+"mixed", "reader"), http.StatusOK)

	// A pattern on one version's tarball leaves the version listed, with its
	// tarball URL, and refuses only the tarball (RTFACT-19925).
	f.setPolicy(t, "npm-local", false, "", "**/mixed-1.0.0.tgz")
	rec = f.do(http.MethodGet, registry+"mixed", "reader")
	wantStatus(t, rec, http.StatusOK)
	versions, _ := decode(t, rec)["versions"].(map[string]any)
	if _, ok := versions["1.0.0"]; !ok || len(versions) != 2 {
		t.Errorf("versions %v, want 1.0.0 and 2.0.0", versions)
	}
	wantStatus(t, f.do(http.MethodGet, registry+"mixed/1.0.0", "reader"), http.StatusOK)
	wantAnswer(t, f.do(http.MethodGet, registry+"-/package/mixed/dist-tags", "reader"),
		http.StatusOK, `{"latest":"2.0.0"}`)
	wantAnswer(t, f.do(http.MethodGet, registry+"mixed/-/mixed-1.0.0.tgz", "reader"),
		http.StatusNotFound, rejected("npm-local", "mixed/-/mixed-1.0.0.tgz"))
}

// A remote repository's packument is checked at the path 7.x caches it at,
// .npm/<name>/package.json (RTFACT-18463), with read permission still on
// the package's folder; a tarball's lookup of it is refused only by the
// blackout, naming the tarball, so the tarball's own path decides the
// patterns; and a version whose tarball the patterns reject stays listed.
func TestAccRemoteDocumentPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDoc(t, "mixed", "1.0.0", "2.0.0")
	f.setJSON("/-/v1/search", map[string]any{"objects": []any{
		map[string]any{"package": map[string]any{"name": "mixed", "version": "1.0.0"}},
	}, "total": 1})
	const doc = ".npm/mixed/package.json"
	tarballs := []string{"mixed/-/mixed-1.0.0.tgz", "mixed/-/mixed-2.0.0.tgz"}

	f.setPolicy(t, "npm-remote", true, "", "")
	for _, p := range documentReads {
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+p, "reader"), http.StatusNotFound,
			blackedOut("npm-remote", doc))
	}
	for _, p := range tarballs {
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+p, "reader"), http.StatusNotFound,
			blackedOut("npm-remote", p))
	}
	wantAnswer(t, f.do(http.MethodGet, remoteRegistry+"-/v1/search?text=mixed", "reader"),
		http.StatusNotFound, blackedOut("npm-remote", ""))
	// A tarball path that is no item path names the packument instead.
	wantAnswer(t, f.do(http.MethodGet, remoteRegistry+"mixed//-/mixed-1.0.0.tgz", "reader"),
		http.StatusNotFound, blackedOut("npm-remote", doc))

	f.setPolicy(t, "npm-remote", false, "", ".npm/mixed/**")
	for _, p := range documentReads {
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+p, "reader"), http.StatusNotFound,
			rejected("npm-remote", doc))
	}
	// [I] Whether 7.x reads the packument to fetch a tarball is not known;
	// the tarball's own path is accepted.
	wantStatus(t, f.do(http.MethodGet, remoteRegistry+tarballs[0], "reader"), http.StatusOK)

	// A pattern on the tarballs, by name or by the package's folder, leaves
	// every version listed and refuses the tarballs alone, naming each.
	for _, exclude := range []string{"**/mixed-1.0.0.tgz", "mixed/**"} {
		f.setPolicy(t, "npm-remote", false, "", exclude)
		rec := f.do(http.MethodGet, remoteRegistry+"mixed", "reader")
		wantStatus(t, rec, http.StatusOK)
		versions, _ := decode(t, rec)["versions"].(map[string]any)
		if _, ok := versions["1.0.0"]; !ok || len(versions) != 2 {
			t.Errorf("%s: versions %v, want 1.0.0 and 2.0.0", exclude, versions)
		}
		requireRemote(t, f.do(http.MethodGet, remoteRegistry+"mixed/1.0.0", "reader"), 200,
			`"version":"1.0.0"`)
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+"-/package/mixed/dist-tags", "reader"),
			http.StatusOK, `{"latest":"2.0.0"}`)
		// The upstream search goes on whatever the patterns say (RTFACT-30307).
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+"-/v1/search?text=mixed", "reader"),
			http.StatusOK, `{"objects":[{"package":{"name":"mixed","version":"1.0.0"}}]}`)
		wantAnswer(t, f.do(http.MethodGet, remoteRegistry+tarballs[0], "reader"),
			http.StatusNotFound, rejected("npm-remote", tarballs[0]))
	}
	wantAnswer(t, f.do(http.MethodGet, remoteRegistry+tarballs[1], "reader"),
		http.StatusNotFound, rejected("npm-remote", tarballs[1]))

	// Read permission comes first, and stays on the package's folder.
	f.setPolicy(t, "npm-remote", true, "", "")
	wantAnswer(t, f.do(http.MethodGet, remoteRegistry+"mixed", "nobody"), http.StatusForbidden,
		`{"error":"Forbidden"}`)
	f.setPolicy(t, "npm-remote", false, "", "")
	rec := f.do(http.MethodGet, remoteRegistry+"mixed", "partial")
	wantStatus(t, rec, http.StatusOK)
	versions, _ := decode(t, rec)["versions"].(map[string]any)
	if _, ok := versions["1.0.0"]; !ok || len(versions) != 1 {
		t.Errorf("partial: versions %v, want 1.0.0 alone", versions)
	}
}
