package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const storageURI = "https://reg.example/artifactory/api/storage/"

// searchWorld is an item world holding:
//   - libs-local/org/a/lib-1.0.jar (team=core, tier=gold), deployed with
//     its true MD5 and SHA-1 declared;
//   - libs-local/org/a/lib-1.0.pom (team=core);
//   - other-local/org/b/lib-2.0.jar (team=edge), the same bytes as the jar;
//   - server-local/bad.jar, whose declared MD5 is wrong;
//   - up's cache cached.txt, created an hour ago and never downloaded.
//
// deployer reads libs-local only; anonymous, when on, reads up's cache.
func searchWorld(t *testing.T, anonymous bool, tune ...func(*Config)) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous, tune...)
	w.put("/artifactory/other-local/org/b/lib-2.0.jar;team=edge", "jar")
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/org/a/lib-1.0.jar;team=core;tier=gold",
		"admin", "jar", "X-Checksum-Md5", md5Hex("jar"), "X-Checksum-Sha1", sha1Hex("jar")),
		http.StatusCreated)
	w.put("/artifactory/libs-local/org/a/lib-1.0.pom;team=core", "pom")
	wantStatus(t, w.do("PUT", "/artifactory/server-local/bad.jar", "admin", "bad",
		"X-Checksum-Md5", md5Hex("other")), http.StatusCreated)
	hour := time.Now().Add(-time.Hour).UTC()
	if _, err := w.db.Items().Put(context.Background(), types.Item{
		RepoKey: "up", Cached: true, Path: types.RootPath, Name: "cached.txt", Size: 1,
		Actual:  types.Checksums{SHA1: sha1Hex("c"), MD5: md5Hex("c")},
		Created: hour, Modified: hour, Updated: hour,
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// hitsOf returns a search's results, failing unless it answered 200.
func hitsOf(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if body.Results == nil {
		t.Fatalf("results missing or null: %s", rec.Body)
	}
	return body.Results
}

// uris returns the results' uris under the storage API prefix.
func uris(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out []string
	for _, r := range hitsOf(t, rec) {
		u, _ := r["uri"].(string)
		out = append(out, strings.TrimPrefix(u, storageURI))
	}
	return out
}

func wantURIs(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	got := uris(t, rec)
	if !slices.Equal(got, want) {
		t.Fatalf("uris %q, want %q", got, want)
	}
}

func TestSearchArtifact(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/artifact?name="
	rec := w.do("GET", q+"lib", "admin", "")
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	// Files only, in deploy order; the folders named lib-* are not files.
	wantURIs(t, rec, "other-local/org/b/lib-2.0.jar", "libs-local/org/a/lib-1.0.jar",
		"libs-local/org/a/lib-1.0.pom")
	wantURIs(t, w.do("GET", q+"lib*.jar", "admin", ""),
		"other-local/org/b/lib-2.0.jar", "libs-local/org/a/lib-1.0.jar")
	wantURIs(t, w.do("GET", q+"LIB", "admin", ""))
	wantURIs(t, w.do("GET", q+"lib&repos=libs-local", "admin", ""),
		"libs-local/org/a/lib-1.0.jar", "libs-local/org/a/lib-1.0.pom")
	// A remote's key and its cache key both search the cache, reported
	// under the cache key; a virtual key and an unknown one find nothing.
	wantURIs(t, w.do("GET", q+"cached&repos=up", "admin", ""), "up-cache/cached.txt")
	wantURIs(t, w.do("GET", q+"cached&repos=up-cache", "admin", ""), "up-cache/cached.txt")
	wantURIs(t, w.do("GET", q+"lib&repos=virt", "admin", ""))
	wantURIs(t, w.do("GET", q+"lib&repos=nope,libs-cache", "admin", ""))
	// A local repository whose key ends in -cache keeps its own items.
	if err := w.db.Repos().Create(context.Background(),
		*repo.Defaults(&repo.Config{Key: "tools-cache", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Items().Put(context.Background(), types.Item{
		RepoKey: "tools-cache", Path: types.RootPath, Name: "lib-tool.jar",
		Created: time.Now(), Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	wantURIs(t, w.do("GET", q+"lib&repos=tools-cache", "admin", ""), "tools-cache/lib-tool.jar")
	wantURIs(t, w.do("GET", q+"lib&repos=nope,%20other-local", "admin", ""),
		"other-local/org/b/lib-2.0.jar")
	// What the caller may not read is left out.
	wantURIs(t, w.do("GET", q+"lib", "deployer", ""),
		"libs-local/org/a/lib-1.0.jar", "libs-local/org/a/lib-1.0.pom")
	wantURIs(t, w.do("GET", q+"lib", "user", ""))

	vendor := w.do("GET", q+"pom", "admin", "", "Accept", mediaArtifactSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaArtifactSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q, "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", "/artifactory/api/search/artifact", "admin", ""),
		http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"lib", "", ""), http.StatusUnauthorized)
}

func TestSearchResultDetail(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/artifact?name=lib-1.0.jar"
	info := hitsOf(t, w.do("GET", q, "admin", "", "X-Result-Detail", "info"))[0]
	for k, want := range map[string]any{
		"uri":         storageURI + "libs-local/org/a/lib-1.0.jar",
		"repo":        "libs-local",
		"path":        "/org/a/lib-1.0.jar",
		"downloadUri": "https://reg.example/artifactory/libs-local/org/a/lib-1.0.jar",
		"size":        "3",
		"createdBy":   "admin",
	} {
		if info[k] != want {
			t.Errorf("info %s = %v, want %v", k, info[k], want)
		}
	}
	if sums, _ := info["checksums"].(map[string]any); sums["sha1"] != sha1Hex("jar") {
		t.Errorf("info checksums %v", info["checksums"])
	}
	if sums, _ := info["originalChecksums"].(map[string]any); sums["md5"] != md5Hex("jar") {
		t.Errorf("info original checksums %v", info["originalChecksums"])
	}
	if _, ok := info["properties"]; ok {
		t.Errorf("info alone carries properties: %v", info)
	}

	props := hitsOf(t, w.do("GET", q, "admin", "", "X-Result-Detail", "properties"))[0]
	if len(props) != 2 || fmt.Sprint(props["properties"]) != "map[team:[core] tier:[gold]]" {
		t.Fatalf("properties %v", props)
	}
	both := hitsOf(t, w.do("GET", q, "admin", "", "X-Result-Detail", "info, properties"))[0]
	if both["repo"] != "libs-local" || both["properties"] == nil {
		t.Fatalf("info and properties %v", both)
	}
	// An item without properties has no properties field; an unknown word
	// widens nothing.
	bare := hitsOf(t, w.do("GET", "/artifactory/api/search/artifact?name=bad.jar", "admin", "",
		"X-Result-Detail", "Properties,colour"))[0]
	if len(bare) != 1 {
		t.Fatalf("bad.jar %v", bare)
	}
	// A cached item's info names the cache.
	cached := hitsOf(t, w.do("GET", "/artifactory/api/search/artifact?name=cached", "admin", "",
		"X-Result-Detail", "info"))[0]
	if cached["repo"] != "up-cache" || cached["uri"] != storageURI+"up-cache/cached.txt" {
		t.Fatalf("cached info %v", cached)
	}
}

func TestSearchAnonymous(t *testing.T) {
	w := searchWorld(t, true)
	// Anonymous reads remote caches only (newItemWorld's anonymousRemotes).
	wantURIs(t, w.do("GET", "/artifactory/api/search/artifact?name=c", "", ""),
		"up-cache/cached.txt")
	wantURIs(t, w.do("GET", "/artifactory/api/search/checksum?sha1="+sha1Hex("jar"), "", ""))
	wantURIs(t, w.do("GET", "/artifactory/api/search/prop?team", "", ""))
	wantURIs(t, w.do("GET", "/artifactory/api/search/badChecksum?type=sha1", "", ""),
		"up-cache/cached.txt")
	for _, p := range []string{
		"creation?from=0", "dates?from=0&dateFields=created", "usage?notUsedSince=0",
		"pattern?pattern=up:*",
	} {
		wantStatus(t, w.do("GET", "/artifactory/api/search/"+p, "", ""), http.StatusUnauthorized)
	}
}

func TestSearchProperty(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/prop?"
	wantURIs(t, w.do("GET", q+"team=core", "admin", ""),
		"libs-local/org/a/lib-1.0.jar", "libs-local/org/a/lib-1.0.pom")
	wantURIs(t, w.do("GET", q+"team=core&tier=gold", "admin", ""), "libs-local/org/a/lib-1.0.jar")
	// Comma-separated values are alternatives, a key alone or with an empty
	// value is any value, and a value with a wildcard matches as a pattern.
	all := []string{
		"other-local/org/b/lib-2.0.jar", "libs-local/org/a/lib-1.0.jar",
		"libs-local/org/a/lib-1.0.pom",
	}
	wantURIs(t, w.do("GET", q+"team=core,edge", "admin", ""), all...)
	wantURIs(t, w.do("GET", q+"team=core&team=edge", "admin", ""), all...)
	wantURIs(t, w.do("GET", q+"team", "admin", ""), all...)
	wantURIs(t, w.do("GET", q+"team=", "admin", ""), all...)
	wantURIs(t, w.do("GET", q+"team=e*", "admin", ""), "other-local/org/b/lib-2.0.jar")
	// Folders carry properties too.
	wantStatus(
		t,
		w.do("PUT", "/artifactory/api/storage/libs-local/org/a?properties=team=dir&recursive=0",
			"admin", ""),
		http.StatusNoContent,
	)
	wantURIs(t, w.do("GET", q+"team=dir", "admin", ""), "libs-local/org/a")
	folder := hitsOf(t, w.do("GET", q+"team=dir", "admin", "", "X-Result-Detail", "info"))[0]
	if children, _ := folder["children"].([]any); len(children) != 2 {
		t.Fatalf("folder info %v", folder)
	}
	// A virtual key searches its members.
	wantURIs(t, w.do("GET", q+"team=edge&repos=virt", "admin", ""),
		"other-local/org/b/lib-2.0.jar")
	wantURIs(t, w.do("GET", q+"team=edge&repos=libs-local", "admin", ""))
	wantURIs(t, w.do("GET", q+"team=edge", "deployer", ""))

	wantStatus(t, w.do("GET", q+"repos=libs-local", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"=x", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"team=%zz", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"team", "", ""), http.StatusUnauthorized)
}

func TestSearchChecksum(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/checksum?"
	both := []string{"other-local/org/b/lib-2.0.jar", "libs-local/org/a/lib-1.0.jar"}
	wantURIs(t, w.do("GET", q+"sha1="+sha1Hex("jar"), "admin", ""), both...)
	wantURIs(t, w.do("GET", q+"md5="+strings.ToUpper(md5Hex("jar")), "admin", ""), both...)
	wantURIs(t, w.do("GET", q+"sha256="+sha256Hex("jar"), "admin", ""), both...)
	// Every checksum given must match.
	wantURIs(t, w.do("GET", q+"sha1="+sha1Hex("jar")+"&md5="+md5Hex("pom"), "admin", ""))
	wantURIs(t, w.do("GET", q+"sha1="+sha1Hex("jar")+"&repos=virt", "admin", ""), both...)
	wantURIs(t, w.do("GET", q+"sha1="+sha1Hex("jar")+"&repos=other-local", "admin", ""),
		"other-local/org/b/lib-2.0.jar")
	wantURIs(t, w.do("GET", q+"sha1="+sha1Hex("c")+"&repos=up", "admin", ""),
		"up-cache/cached.txt")
	props := hitsOf(t, w.do("GET", q+"sha1="+sha1Hex("jar")+"&repos=other-local", "admin", "",
		"X-Result-Detail", "properties"))
	if fmt.Sprint(props[0]["properties"]) != "map[team:[edge]]" {
		t.Fatalf("properties %v", props)
	}

	wantStatus(t, w.do("GET", q, "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"sha1=abc", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"md5="+sha1Hex("jar"), "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"sha1="+sha1Hex("jar"), "", ""), http.StatusUnauthorized)
}

func TestSearchBadChecksum(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/badChecksum?type="
	rec := w.do("GET", q+"md5", "admin", "")
	byURI := map[string]map[string]any{}
	for _, r := range hitsOf(t, rec) {
		u, _ := r["uri"].(string)
		byURI[strings.TrimPrefix(u, storageURI)] = r
	}
	// The jar declared its true MD5; the pom, the copy and the cached file
	// declared none, and bad.jar a wrong one.
	if len(byURI) != 4 || byURI["libs-local/org/a/lib-1.0.jar"] != nil {
		t.Fatalf("bad md5 %s", rec.Body)
	}
	if r := byURI["server-local/bad.jar"]; r["clientMd5"] != md5Hex("other") ||
		r["serverMd5"] != md5Hex("bad") {
		t.Fatalf("bad.jar %v", r)
	}
	if r := byURI["libs-local/org/a/lib-1.0.pom"]; r["clientMd5"] != "" ||
		r["serverMd5"] != md5Hex("pom") {
		t.Fatalf("pom %v", r)
	}
	// clientMd5 carries the type asked for.
	sha := hitsOf(t, w.do("GET", q+"sha1&repos=libs-local", "admin", ""))
	if len(sha) != 1 || sha[0]["serverMd5"] != sha1Hex("pom") {
		t.Fatalf("bad sha1 %v", sha)
	}
	wantURIs(t, w.do("GET", q+"md5&repos=virt", "deployer", ""), "libs-local/org/a/lib-1.0.pom")
	vendor := w.do("GET", q+"md5", "admin", "", "Accept", mediaBadChecksumSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaBadChecksumSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q+"sha256", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", "/artifactory/api/search/badChecksum", "admin", ""),
		http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"md5", "", ""), http.StatusUnauthorized)
}

func TestSearchCreation(t *testing.T) {
	w := searchWorld(t, false)
	const q = "/artifactory/api/search/creation?from="
	minute := time.Now().Add(-time.Minute).UnixMilli()
	rec := w.do("GET", fmt.Sprintf("%s%d&repos=libs-local", q, minute), "admin", "")
	res := hitsOf(t, rec)
	if len(res) != 2 || res[0]["uri"] != storageURI+"libs-local/org/a/lib-1.0.jar" {
		t.Fatalf("created %s", rec.Body)
	}
	created, err := time.Parse(time.RFC3339, res[0]["created"].(string))
	if err != nil || time.Since(created) > time.Minute ||
		!strings.HasSuffix(res[0]["created"].(string), "Z") {
		t.Fatalf("created %v: %v", res[0]["created"], err)
	}
	// The window is inclusive and to defaults to now: the cached file was
	// created an hour ago.
	wantURIs(t, w.do("GET", fmt.Sprintf("%s0&to=%d&repos=up", q, minute), "admin", ""),
		"up-cache/cached.txt")
	wantURIs(t, w.do("GET", fmt.Sprintf("%s%d&repos=up", q, minute), "admin", ""))
	wantURIs(t, w.do("GET", q+"0&repos=virt", "admin", ""))
	wantURIs(t, w.do("GET", q+"0", "user", ""))

	wantStatus(t, w.do("GET", q+"0&repos=nope", "admin", ""), http.StatusNotFound)
	// A repository the caller may not see is as good as absent.
	wantStatus(t, w.do("GET", q+"0&repos=other-local", "deployer", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", q+"10&to=5", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"x", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", "/artifactory/api/search/creation", "admin", ""),
		http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"0", "", ""), http.StatusUnauthorized)
}

func TestSearchDates(t *testing.T) {
	w := searchWorld(t, false)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/org/a/lib-1.0.pom", "admin", ""),
		http.StatusOK)
	const q = "/artifactory/api/search/dates?"
	hour := time.Now().Add(-30 * time.Minute).UTC()
	iso := hour.Format("2006-01-02T15:04:05.000-07:00")
	rec := w.do("GET", q+"dateFields=lastDownloaded&from="+iso, "admin", "")
	res := hitsOf(t, rec)
	if len(res) != 1 || res[0]["uri"] != storageURI+"libs-local/org/a/lib-1.0.pom" ||
		len(res[0]) != 2 || res[0]["lastDownloaded"] == nil {
		t.Fatalf("downloaded %s", rec.Body)
	}
	// Each named field is returned when the item has it; an item matches
	// when any of them is in the window.
	rec = w.do("GET", q+"dateFields=created,lastDownloaded&from=0&repos=libs-local", "admin", "")
	res = hitsOf(t, rec)
	if len(res) != 2 || len(res[0]) != 2 || res[0]["created"] == nil || len(res[1]) != 3 {
		t.Fatalf("created or downloaded %s", rec.Body)
	}
	wantURIs(t, w.do("GET", q+"dateFields=lastModified&from=0&to="+iso, "admin", ""),
		"up-cache/cached.txt")
	// A "+" left unescaped in the offset arrives as a space.
	plus := hour.Format("2006-01-02T15:04:05") + "+00:00"
	wantURIs(t, w.do("GET", q+"dateFields=created&from=2000-01-01&to="+plus, "admin", ""),
		"up-cache/cached.txt")
	wantURIs(t, w.do("GET", q+"dateFields=created&from=0&repos=up", "deployer", ""))

	for _, bad := range []string{
		"dateFields=created", "from=0", "dateFields=updated&from=0", "dateFields=&from=0",
		"dateFields=created&from=yesterday", "dateFields=created&from=0&to=never",
		"dateFields=created&from=2000-01-02&to=2000-01-01",
	} {
		if rec := w.do("GET", q+bad, "admin", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", bad, rec.Code, rec.Body)
		}
	}
	wantStatus(t, w.do("GET", q+"dateFields=created&from=0", "", ""), http.StatusUnauthorized)
}

func TestSearchUsage(t *testing.T) {
	w := searchWorld(t, false)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/org/a/lib-1.0.pom", "admin", ""),
		http.StatusOK)
	const q = "/artifactory/api/search/usage?notUsedSince="
	later := time.Now().Add(time.Hour).UnixMilli()
	rec := w.do("GET", fmt.Sprintf("%s%d&repos=libs-local", q, later), "admin", "")
	res := hitsOf(t, rec)
	// The jar was never downloaded; the pom was, before notUsedSince.
	if len(res) != 2 || len(res[0]) != 1 || res[1]["lastDownloaded"] == nil {
		t.Fatalf("unused %s", rec.Body)
	}
	// Downloaded since: not unused.
	now := time.Now().Add(-time.Minute).UnixMilli()
	wantURIs(t, w.do("GET", fmt.Sprintf("%s%d&createdBefore=%d&repos=libs-local", q, now, later),
		"admin", ""), "libs-local/org/a/lib-1.0.jar")
	// createdBefore defaults to notUsedSince: only the cached file is older.
	wantURIs(t, w.do("GET", fmt.Sprintf("%s%d", q, now), "admin", ""), "up-cache/cached.txt")
	wantURIs(t, w.do("GET", fmt.Sprintf("%s%d&repos=up", q, later), "deployer", ""))
	vendor := w.do("GET", q+"0", "admin", "", "Accept", mediaUsageSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaUsageSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q+"soon", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"0", "", ""), http.StatusUnauthorized)
}

func TestSearchPattern(t *testing.T) {
	w := searchWorld(t, false)
	w.put("/artifactory/libs-local/org/a/b/deep.jar", "deep")
	const q = "/artifactory/api/search/pattern?pattern="
	pattern := func(p, as string) map[string]any {
		t.Helper()
		rec := w.do("GET", q+p, as, "")
		wantStatus(t, rec, http.StatusOK)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	files := func(p, as string, want ...string) {
		t.Helper()
		got := fmt.Sprint(pattern(p, as)["files"])
		if got != fmt.Sprint(append([]string{}, want...)) {
			t.Fatalf("%s: files %s, want %q", p, got, want)
		}
	}
	body := pattern("libs-local:org/*/*.jar", "admin")
	if body["repositoryUri"] != "https://reg.example/artifactory/libs-local" ||
		body["sourcePattern"] != "libs-local:org/*/*.jar" ||
		fmt.Sprint(body["files"]) != "[org/a/lib-1.0.jar]" {
		t.Fatalf("pattern %v", body)
	}
	// * and ? stay within their segment.
	files("libs-local:org/*.jar", "admin")
	files("libs-local:org/a/*/*.jar", "admin", "org/a/b/deep.jar")
	files("libs-local:/org/?/lib-1.0.*", "admin", "org/a/lib-1.0.jar", "org/a/lib-1.0.pom")
	files("up:*.txt", "admin", "cached.txt")
	files("up-cache:cached.txt", "admin", "cached.txt")
	files("other-local:org/*/*.jar", "deployer")
	// A key naming no repository the caller may search finds nothing.
	files("nope:*", "admin")
	files("virt:*", "admin")
	files("a,b:*", "admin")

	for _, bad := range []string{
		"libs-local", ":x", "libs-local:org/**/x", "libs-local:",
		"libs-local:org/", "libs-local:org//x",
	} {
		if rec := w.do("GET", q+bad, "admin", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", bad, rec.Code, rec.Body)
		}
	}
	vendor := w.do("GET", q+"up:*", "admin", "", "Accept", mediaPatternSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaPatternSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q+"up:*", "", ""), http.StatusUnauthorized)
}

func TestSearchLimits(t *testing.T) {
	limited := func(internal bool) func(*Config) {
		return func(c *Config) {
			c.Search = SearchLimits{UserQueryLimit: 1, LimitInternalUsers: internal}
		}
	}
	const q = "/artifactory/api/search/artifact?name=."
	w := searchWorld(t, true, limited(false))
	if n := len(hitsOf(t, w.do("GET", q, "", ""))); n != 1 {
		t.Fatalf("anonymous %d results", n)
	}
	if n := len(hitsOf(t, w.do("GET", q, "reader", ""))); n != 5 {
		t.Fatalf("reader %d results", n)
	}
	w = searchWorld(t, false, limited(true))
	if n := len(hitsOf(t, w.do("GET", q, "reader", ""))); n != 1 {
		t.Fatalf("limited reader %d results", n)
	}
	if n := len(hitsOf(t, w.do("GET", q, "admin", ""))); n != 5 {
		t.Fatalf("admin %d results", n)
	}
	// The bad checksum search has its own cap, and the AQL hard cap
	// bounds every search, pattern included.
	if n := len(hitsOf(t, w.do("GET", "/artifactory/api/search/badChecksum?type=md5", "reader",
		""))); n != 4 {
		t.Fatalf("bad checksum %d results", n)
	}
	w = searchWorld(t, false, func(c *Config) { c.AQL.MaxResults = 2 })
	if n := len(hitsOf(t, w.do("GET", q, "admin", ""))); n != 2 {
		t.Fatalf("capped admin %d results", n)
	}
	rec := w.do("GET", "/artifactory/api/search/pattern?pattern=libs-local:org/a/*", "admin", "")
	if !strings.Contains(rec.Body.String(), `"files":["org/a/lib-1.0.jar","org/a/lib-1.0.pom"]`) {
		t.Fatalf("capped pattern %s", rec.Body)
	}
}

// TestSearchPages reads past one store page: the caller reads only the
// last item, so the first page yields nothing it may see.
func TestSearchPages(t *testing.T) {
	w := searchWorld(t, false)
	ctx := context.Background()
	for n := range searchPage {
		if _, err := w.db.Items().Put(ctx, types.Item{
			RepoKey: "other-local", Path: types.RootPath, Name: fmt.Sprintf("page-%04d.bin", n),
			Created: time.Now(), Modified: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.put("/artifactory/libs-local/page-last.bin", "last")
	wantURIs(t, w.do("GET", "/artifactory/api/search/artifact?name=page-", "deployer", ""),
		"libs-local/page-last.bin")
	if n := len(hitsOf(t, w.do("GET", "/artifactory/api/search/artifact?name=page-", "admin",
		""))); n != searchPage+1 {
		t.Fatalf("admin %d results", n)
	}
}

// TestSearchStoreErrors: a store that fails answers 500 without detail.
func TestSearchStoreErrors(t *testing.T) {
	for name, wrap := range map[string]func(store.Store) store.Store{
		"search": func(s store.Store) store.Store { return failingSearchStore{s} },
		"repositories": func(s store.Store) store.Store {
			return faultyStore{Store: s.(*inmem.Store), fail: map[string]bool{"list": true}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := searchWorld(t, false, func(c *Config) { c.Store = wrap(c.Store) })
			for _, p := range []string{
				"artifact?name=lib", "prop?team", "checksum?md5=" + md5Hex("jar"),
				"badChecksum?type=md5", "creation?from=0", "dates?from=0&dateFields=created",
				"usage?notUsedSince=0", "pattern?pattern=libs-local:*", "gavc?a=lib",
				"versions?a=lib", "latestVersion?a=lib",
			} {
				rec := w.do("GET", "/artifactory/api/search/"+p, "admin", "")
				if rec.Code != http.StatusInternalServerError ||
					strings.Contains(rec.Body.String(), "boom") {
					t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
				}
			}
		})
	}
}

// failingSearchStore fails every item search.
type failingSearchStore struct{ store.Store }

func (s failingSearchStore) Items() store.ItemStore { return failingItems{s.Store.Items()} }

type failingItems struct{ store.ItemStore }

func (failingItems) Search(context.Context, *aql.Query, aql.Options) ([]aql.Hit, error) {
	return nil, errBoom
}

// searchStatusRows: quick, property, checksum and bad checksum searches
// answer anonymous callers while anonymous access is on; the others need a
// named user. A caller in no group gets 200 and only what it may read.
func searchStatusRows() []statusCase {
	const (
		artifact = "GET /artifactory/api/search/artifact"
		prop     = "GET /artifactory/api/search/prop"
		checksum = "GET /artifactory/api/search/checksum"
		bad      = "GET /artifactory/api/search/badChecksum"
		creation = "GET /artifactory/api/search/creation"
		dates    = "GET /artifactory/api/search/dates"
		usage    = "GET /artifactory/api/search/usage"
		pattern  = "GET /artifactory/api/search/pattern"
		s        = "/artifactory/api/search/"
	)
	sum := s + "checksum?sha1=" + sha1Hex("fixture")
	var rows []statusCase
	for _, op := range []struct{ route, path, invalid string }{
		{artifact, s + "artifact?name=get", s + "artifact"},
		{prop, s + "prop?k=v", s + "prop"},
		{checksum, sum, s + "checksum?sha1=x"},
		{bad, s + "badChecksum?type=sha1", s + "badChecksum?type=crc"},
	} {
		rows = append(rows,
			statusCase{op.route, op.path, "admin", "", 200},
			statusCase{op.route, op.path, "user", "", 200},
			statusCase{op.route, op.path, "anonymous", "", 200},
			statusCase{op.route, op.path, "", "", 401},
			statusCase{op.route, op.invalid, "admin", "", 400},
		)
	}
	for _, op := range []struct{ route, path, invalid string }{
		{creation, s + "creation?from=0", s + "creation?from=9&to=1"},
		{dates, s + "dates?from=0&dateFields=created", s + "dates?from=0&dateFields=x"},
		{usage, s + "usage?notUsedSince=0", s + "usage?notUsedSince=x"},
		{pattern, s + "pattern?pattern=libs-local:fixture/*", s + "pattern?pattern=libs-local:**"},
	} {
		rows = append(rows,
			statusCase{op.route, op.path, "admin", "", 200},
			statusCase{op.route, op.path, "user", "", 200},
			statusCase{op.route, op.path, "anonymous", "", 401},
			statusCase{op.route, op.path, "", "", 401},
			statusCase{op.route, op.invalid, "admin", "", 400},
		)
	}
	rows = append(rows, statusCase{creation, s + "creation?from=0&repos=missing", "admin", "", 404})
	return append(rows, moreSearchStatusRows()...)
}

// searchMatrix: the four searches anonymous may make answer anyone, with
// what each caller may read; the others answer any named user.
func searchMatrix() []matrixRow {
	const s = "/artifactory/api/search/"
	return append([]matrixRow{
		row("GET "+s+"artifact", s+"artifact?name=get", "", anyone, 200),
		row("GET "+s+"prop", s+"prop?k", "", anyone, 200),
		row("GET "+s+"checksum", s+"checksum?sha1="+sha1Hex("matrix"), "", anyone, 200),
		row("GET "+s+"badChecksum", s+"badChecksum?type=md5", "", anyone, 200),
		row("GET "+s+"creation", s+"creation?from=0", "", signedIn, 200),
		row("GET "+s+"dates", s+"dates?from=0&dateFields=created", "", signedIn, 200),
		row("GET "+s+"usage", s+"usage?notUsedSince=0", "", signedIn, 200),
		row("GET "+s+"pattern", s+"pattern?pattern=libs-local:work/*", "", signedIn, 200),
	}, moreSearchMatrix()...)
}
