package nugethttp_test

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

// wantError fails unless rec answered status with message in the
// Artifactory envelope.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if got, msg := envelope(t, rec); got != status || msg != message {
		t.Fatalf("%d %q, want %d %q", got, msg, status, message)
	}
}

// blackedOut and rejected are the download policy's refusals of path in
// key.
func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" + key + ":" +
		path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

const (
	symbol     = "/app.pdb/0123456789ABCDEF0123456789ABCDEF0/app.pdb"
	symbolPath = ".symbols/app.pdb/0123456789ABCDEF0123456789ABCDEF0/app.pdb"
)

// A blacked-out local repository refuses what restore reads, the version
// list, the registration hives and the v2 entries, before building them,
// naming the request's own path [I]. A .nuspec takes its .nupkg's refusal,
// a missing PDB's refusal is the policy's and not NuGet's own 404, and
// search, listing and the service documents keep their answers. Read
// permission comes first.
func TestAccLocalResolutionPolicy(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Mixed", "1.0.0", "")
	f.push("Acme.Mixed", "2.0.0", "")

	f.setPolicy(t, "nuget-local", true, "", "")
	for _, p := range []string{
		"flatcontainer/acme.mixed/index.json",
		"flatcontainer/absent/index.json",
		"registration/acme.mixed/index.json",
		"registration-semver2/acme.mixed/page/1.0.0/2.0.0.json",
		"registration/acme.mixed/1.0.0.json",
	} {
		wantError(t, f.do(http.MethodGet, v3+"/"+p, "reader"), http.StatusNotFound,
			blackedOut("nuget-local", p))
		wantError(t, f.do(http.MethodGet, v3+"/"+p, "nobody"), http.StatusNotFound,
			"not found: GET "+v3+"/"+p)
	}
	for _, p := range []string{
		"FindPackagesById()?id='Acme.Mixed'",
		"FindPackagesById()/$count?id='Acme.Mixed'",
		"Packages(Id='Acme.Mixed',Version='1.0.0')",
	} {
		name, _, _ := strings.Cut(p, "?")
		wantError(t, f.do(http.MethodGet, v2+"/"+p, "reader"), http.StatusNotFound,
			blackedOut("nuget-local", name))
	}
	wantError(t, f.do(http.MethodGet, v2+"/Packages(Id='Acme.Mixed',Version='1.0.0')", "nobody"),
		http.StatusNotFound, "not found: GET "+v2+"/Packages(Id='Acme.Mixed',Version='1.0.0')")
	wantError(t, f.do(http.MethodGet, v3+"/flatcontainer/acme.mixed/2.0.0/acme.mixed.nuspec",
		"reader"), http.StatusNotFound, blackedOut("nuget-local", "Acme.Mixed.2.0.0.nupkg"))
	wantError(t, f.do(http.MethodGet, v3+"/flatcontainer/acme.mixed/2.0.0/acme.mixed.nuspec",
		"nobody"), http.StatusForbidden, "Forbidden")
	wantError(t, f.do(http.MethodGet, v3+symbol, "reader"), http.StatusNotFound,
		blackedOut("nuget-local", symbolPath))
	wantError(t, f.do(http.MethodGet, v3+symbol, "nobody"), http.StatusForbidden, "Forbidden")
	// Search and listing are computed, and 6.20's searchers do not check
	// the blackout (decision 0032): they keep their answers [I].
	for _, p := range []string{v3 + "/index.json", v2 + "/"} {
		wantStatus(t, f.do(http.MethodGet, p, "reader"), http.StatusOK)
	}
	for _, p := range []string{
		v3 + "/query?q=acme", v3 + "/autocomplete?q=acme",
		v2 + "/Packages()", v2 + "/Search()?searchTerm='acme'",
	} {
		rec := f.do(http.MethodGet, p, "reader")
		wantStatus(t, rec, http.StatusOK)
		if !strings.Contains(rec.Body.String(), "Acme.Mixed") {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}

	// A pattern on one version's .nupkg leaves the version listed, with its
	// content URL, and refuses the .nupkg and its .nuspec alone (RTFACT-6205,
	// RTFACT-8218).
	f.setPolicy(t, "nuget-local", false, "", "**/Acme.Mixed.1.0.0.nupkg")
	rec := f.do(http.MethodGet, v3+"/flatcontainer/acme.mixed/index.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"versions":["1.0.0","2.0.0"]}` {
		t.Errorf("versions %s", got)
	}
	rec = f.do(http.MethodGet, v3+"/registration/acme.mixed/1.0.0.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"packageContent"`) {
		t.Errorf("leaf %s", rec.Body)
	}
	rec = f.do(http.MethodGet, v2+"/FindPackagesById()?id='Acme.Mixed'", "reader")
	wantStatus(t, rec, http.StatusOK)
	for _, v := range []string{"1.0.0", "2.0.0"} {
		if !strings.Contains(rec.Body.String(), "Download/Acme.Mixed/"+v) {
			t.Errorf("FindPackagesById() lacks %s: %s", v, rec.Body)
		}
	}
	for _, p := range []string{"acme.mixed.1.0.0.nupkg", "acme.mixed.nuspec"} {
		wantError(t, f.do(http.MethodGet, v3+"/flatcontainer/acme.mixed/1.0.0/"+p, "reader"),
			http.StatusNotFound, rejected("nuget-local", "Acme.Mixed.1.0.0.nupkg"))
	}
	wantStatus(t, f.do(http.MethodGet, v3+"/flatcontainer/acme.mixed/2.0.0/acme.mixed.nuspec",
		"reader"), http.StatusOK)

	f.setPolicy(t, "nuget-local", false, "", ".symbols/**")
	wantError(t, f.do(http.MethodGet, v3+symbol, "reader"), http.StatusNotFound,
		rejected("nuget-local", symbolPath))
	f.setPolicy(t, "nuget-local", false, "", "")
	wantError(t, f.do(http.MethodGet, v3+symbol, "reader"), http.StatusNotFound,
		"not found: GET "+v3+symbol)
}

// A remote repository's v3 documents are refused by its blackout alone: a
// pattern on a package's .nupkg, by name or by the package's folder, leaves
// its versions listed and refuses the .nupkg and its .nuspec, naming the
// .nupkg (RTFACT-6205, RTFACT-8218), and the upstream search goes on
// whatever the patterns say (RTFACT-30307).
func TestAccRemoteDocumentPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
	f.setJSON("/query", map[string]any{"totalHits": 1, "data": []any{map[string]any{
		"id": "Acme.Mixed", "version": "2.0.0",
		"versions": []any{map[string]any{"version": "1.0.0"}, map[string]any{"version": "2.0.0"}},
	}}})
	const refused = "acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg"

	for _, exclude := range []string{"**/acme.mixed.1.0.0.nupkg", "acme.mixed/**"} {
		f.setPolicy(t, "nuget-remote", false, "", exclude)
		rec := f.do(http.MethodGet, remoteV3+"flatcontainer/acme.mixed/index.json", "reader")
		wantStatus(t, rec, http.StatusOK)
		if got := strings.TrimSpace(rec.Body.String()); got != `{"versions":["1.0.0","2.0.0"]}` {
			t.Errorf("%s: versions %s", exclude, got)
		}
		for _, p := range []string{
			"registration-semver2/acme.mixed/index.json",
			"registration-semver2/acme.mixed/1.0.0.json",
			"query?q=acme",
		} {
			rec = f.do(http.MethodGet, remoteV3+p, "reader")
			wantStatus(t, rec, http.StatusOK)
			if !strings.Contains(rec.Body.String(), "1.0.0") {
				t.Errorf("%s: %s lacks 1.0.0: %s", exclude, p, rec.Body)
			}
		}
		for _, p := range []string{"acme.mixed.1.0.0.nupkg", "acme.mixed.nuspec"} {
			wantError(t, f.do(http.MethodGet, remoteV3+"flatcontainer/acme.mixed/1.0.0/"+p,
				"reader"), http.StatusNotFound, rejected("nuget-remote", refused))
		}
	}
	wantError(t, f.do(http.MethodGet,
		remoteV3+"flatcontainer/acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg", "reader"),
		http.StatusNotFound,
		rejected("nuget-remote", "acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg"))

	// [I] The path the blackout names is the service index's, read first.
	f.setPolicy(t, "nuget-remote", true, "", "")
	wantError(t, f.do(http.MethodGet, remoteV3+"flatcontainer/acme.mixed/index.json", "reader"),
		http.StatusNotFound, blackedOut("nuget-remote", ""))
}

// A remote repository's v2 feeds are refused by its blackout alone, as its
// v3 documents are.
func TestAccRemoteV2FeedPolicy(t *testing.T) {
	f := newLegacyFixture(t)
	one, two := f.legacyPackage("Acme.Mixed", "1.0.0"), f.legacyPackage("Acme.Mixed", "2.0.0")
	f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(one+two, ""))
	f.setXML("/custom/feed/Packages", legacyFeedXML(one+two, ""))
	feed := remoteV2 + "FindPackagesById()?id=%27ACME.MIXED%27&semVerLevel=2.0.0"

	for _, exclude := range []string{"**/acme.mixed.1.0.0.nupkg", "acme.mixed/**"} {
		f.setPolicy(t, "nuget-remote", false, "", exclude)
		for _, p := range []string{feed, remoteV2 + "Packages"} {
			rec := f.do(http.MethodGet, p, "reader")
			wantStatus(t, rec, http.StatusOK)
			for _, v := range []string{"1.0.0", "2.0.0"} {
				if !strings.Contains(rec.Body.String(), "Download/Acme.Mixed/"+v) {
					t.Errorf("%s: %s lacks %s: %s", exclude, p, v, rec.Body)
				}
			}
		}
		wantError(t, f.do(http.MethodGet, remoteV2+"Download/Acme.Mixed/1.0.0", "reader"),
			http.StatusNotFound, rejected("nuget-remote", "acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg"))
	}

	f.setPolicy(t, "nuget-remote", true, "", "")
	wantError(t, f.do(http.MethodGet, feed, "reader"), http.StatusNotFound,
		blackedOut("nuget-remote", "acme.mixed"))
	wantError(t, f.do(http.MethodGet, remoteV2+"Packages", "reader"), http.StatusNotFound,
		blackedOut("nuget-remote", ""))
}
