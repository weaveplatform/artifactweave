package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// mavenWorld is an item world holding Maven-layout files (every test
// repository defaults to maven-2-default):
//   - libs-local org/acme/lib versions 1.0 (jar, sources jar, pom), 1.1, 1.10
//     and 2.0-SNAPSHOT (unique snapshots -1, -2 and -10), org/acme/other 1.0,
//     and org/acme/lib/notes.txt, which the layout does not read;
//   - other-local org/acme/lib 3.0;
//   - up's cache org/acme/lib 0.9;
//   - ivy-local (ivy-default) org.acme/lib 1.5 and its ivy.xml;
//   - flat-local (build-default, no base revision) and broken-local (a
//     stored layout that does not compile), both holding lib files.
func mavenWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	ctx := context.Background()
	if err := w.db.Configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigLayout, Key: "broken",
		Document: json.RawMessage(`{"artifactPathPattern":"[orgPath]/[nope]"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "ivy-local", Class: repo.Local, RepoLayoutRef: "ivy-default"},
		{Key: "flat-local", Class: repo.Local, RepoLayoutRef: "build-default"},
		{Key: "broken-local", Class: repo.Local, RepoLayoutRef: "broken"},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		"libs-local/org/acme/lib/1.0/lib-1.0.jar",
		"libs-local/org/acme/lib/1.0/lib-1.0-sources.jar",
		"libs-local/org/acme/lib/1.0/lib-1.0.pom",
		"libs-local/org/acme/lib/1.1/lib-1.1.jar",
		"libs-local/org/acme/lib/1.10/lib-1.10.jar",
		"libs-local/org/acme/lib/2.0-SNAPSHOT/lib-2.0-20260101.120000-1.jar",
		"libs-local/org/acme/lib/2.0-SNAPSHOT/lib-2.0-20260101.120000-2.jar",
		"libs-local/org/acme/lib/2.0-SNAPSHOT/lib-2.0-20260101.120000-10.jar",
		"libs-local/org/acme/other/1.0/other-1.0.jar",
		"libs-local/org/acme/lib/notes.txt",
		"other-local/org/acme/lib/3.0/lib-3.0.jar",
		"ivy-local/org.acme/lib/1.5/jars/lib-1.5.jar",
		"ivy-local/org.acme/lib/1.5/ivys/ivy-1.5.xml",
		"flat-local/org/acme/lib-1.0.jar",
		"broken-local/org/acme/lib/1.0/lib-1.0.jar",
	} {
		w.put("/artifactory/"+p, p)
	}
	if _, err := w.db.Items().Put(ctx, types.Item{
		RepoKey: "up", Cached: true, Path: "org/acme/lib/0.9", Name: "lib-0.9.jar",
		Created: time.Now(), Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

const lib = "libs-local/org/acme/lib/"

func TestAccSearchGavc(t *testing.T) {
	w := mavenWorld(t, false)
	const q = "/artifactory/api/search/gavc?"
	wantURIs(t, w.do("GET", q+"g=org.acme&a=lib&v=1.0&repos=libs-local", "admin", ""),
		lib+"1.0/lib-1.0.jar", lib+"1.0/lib-1.0-sources.jar", lib+"1.0/lib-1.0.pom")
	wantURIs(t, w.do("GET", q+"a=lib&c=sources", "admin", ""), lib+"1.0/lib-1.0-sources.jar")
	// A version is the folder's: every unique snapshot of 2.0-SNAPSHOT.
	wantURIs(t, w.do("GET", q+"v=2.0-SNAPSHOT", "admin", ""),
		lib+"2.0-SNAPSHOT/lib-2.0-20260101.120000-1.jar",
		lib+"2.0-SNAPSHOT/lib-2.0-20260101.120000-2.jar",
		lib+"2.0-SNAPSHOT/lib-2.0-20260101.120000-10.jar")
	wantURIs(t, w.do("GET", q+"g=org.*&a=l?b&v=1.1*&repos=libs-local", "admin", ""),
		lib+"1.1/lib-1.1.jar", lib+"1.10/lib-1.10.jar")
	// Each repository's own layout reads it: ivy's dotted organisation, and
	// its descriptor. Layouts without a base revision, and one that does not
	// compile, read nothing.
	wantURIs(
		t,
		w.do("GET", q+"g=org.acme&a=lib&v=1.5", "admin", ""),
		"ivy-local/org.acme/lib/1.5/jars/lib-1.5.jar",
		"ivy-local/org.acme/lib/1.5/ivys/ivy-1.5.xml",
	)
	wantURIs(t, w.do("GET", q+"a=lib&repos=flat-local,broken-local", "admin", ""))
	// A remote's key searches its cache; a virtual key, without specific,
	// finds nothing.
	wantURIs(
		t,
		w.do("GET", q+"a=lib&repos=up", "admin", ""),
		"up-cache/org/acme/lib/0.9/lib-0.9.jar",
	)
	wantURIs(t, w.do("GET", q+"a=lib&repos=virt", "admin", ""))
	wantURIs(t, w.do("GET", q+"a=lib&v=3.0", "deployer", ""))
	if n := len(uris(t, w.do("GET", q+"a=lib", "deployer", ""))); n != 8 {
		t.Fatalf("deployer reads %d lib files", n)
	}
	props := hitsOf(t, w.do("GET", q+"a=other", "admin", "", "X-Result-Detail", "info"))
	if len(props) != 1 || props[0]["downloadUri"] == nil {
		t.Fatalf("info %v", props)
	}
	vendor := w.do("GET", q+"a=other", "admin", "", "Accept", mediaGavcSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaGavcSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q, "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"g=&a=", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"a=lib", "", ""), http.StatusUnauthorized)
}

func TestAccSearchGavcSpecific(t *testing.T) {
	w := mavenWorld(t, false)
	const q = "/artifactory/api/search/gavc?specific=true&g=org.acme&a=lib&v=1.0"
	// A virtual key searches its members, and the results download through
	// it; ext, version and classifier come from the layout.
	res := hitsOf(t, w.do("GET", q+"&repos=virt", "admin", "", "X-Result-Detail", "info"))
	got, _ := json.Marshal(res)
	want := `[{"downloadUri":"https://reg.example/artifactory/virt/org/acme/lib/1.0/lib-1.0.jar",` +
		`"ext":"jar","version":"1.0"},` +
		`{"classifier":"sources",` +
		`"downloadUri":"https://reg.example/artifactory/virt/org/acme/lib/1.0/lib-1.0-sources.jar",` +
		`"ext":"jar","version":"1.0"},` +
		`{"downloadUri":"https://reg.example/artifactory/virt/org/acme/lib/1.0/lib-1.0.pom",` +
		`"ext":"pom","version":"1.0"}]`
	if string(got) != want {
		t.Fatalf("specific\n%s\nwant\n%s", got, want)
	}
	// The first key that reaches a file names it; without repos, its own.
	res = hitsOf(t, w.do("GET", q+"&c=sources&repos=libs-local,virt", "admin", ""))
	if len(res) != 1 || !strings.Contains(res[0]["downloadUri"].(string), "/libs-local/") {
		t.Fatalf("named first %v", res)
	}
	res = hitsOf(
		t,
		w.do("GET", "/artifactory/api/search/gavc?specific=true&a=lib&v=0.9", "admin", ""),
	)
	if len(res) != 1 ||
		res[0]["downloadUri"] != "https://reg.example/artifactory/up-cache/org/acme/lib/0.9/lib-0.9.jar" {
		t.Fatalf("own key %v", res)
	}
	res = hitsOf(t, w.do("GET", "/artifactory/api/search/gavc?specific=true&a=lib&v=0.9&repos=up",
		"admin", ""))
	if len(res) != 1 ||
		res[0]["downloadUri"] != "https://reg.example/artifactory/up/org/acme/lib/0.9/lib-0.9.jar" {
		t.Fatalf("remote key %v", res)
	}
}

// wantNotFound checks a 404 carries Artifactory's message.
func wantNotFound(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	wantStatus(t, rec, http.StatusNotFound)
	var body struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil ||
		len(body.Errors) != 1 || body.Errors[0].Message != message {
		t.Fatalf("404 body %s, want %q", rec.Body, message)
	}
}

func TestAccSearchVersions(t *testing.T) {
	w := mavenWorld(t, false)
	// A release candidate and a non-unique snapshot of one base version,
	// which Mercury order puts the other way round from Maven's.
	w.put("/artifactory/libs-local/org/acme/mix/1.0-rc1/mix-1.0-rc1.jar", "rc")
	w.put("/artifactory/libs-local/org/acme/mix/1.0-SNAPSHOT/mix-1.0-SNAPSHOT.jar", "snapshot")
	const q = "/artifactory/api/search/versions?"
	versions := func(query, as string) string {
		t.Helper()
		var out []string
		for _, r := range hitsOf(t, w.do("GET", q+query, as, "")) {
			v := fmt.Sprint(r["version"])
			if r["integration"] == true {
				v += "*"
			}
			out = append(out, v)
		}
		return strings.Join(out, " ")
	}
	// Distinct module versions, newest first in Mercury order.
	if got := versions(
		"g=org.acme&a=lib&repos=libs-local",
		"admin",
	); got != "2.0-SNAPSHOT* 1.10 1.1 1.0" {
		t.Fatalf("versions %q", got)
	}
	if got := versions("a=mix", "admin"); got != "1.0-rc1 1.0-SNAPSHOT*" {
		t.Fatalf("mercury %q", got)
	}
	// A pattern filters them; a module version lists its integration
	// versions by file version.
	if got := versions("a=lib&v=1.*&repos=libs-local&remote=1", "admin"); got != "1.10 1.1 1.0" {
		t.Fatalf("filtered %q", got)
	}
	if got := versions("a=lib&v=2.0-SNAPSHOT", "admin"); got !=
		"2.0-20260101.120000-10* 2.0-20260101.120000-2* 2.0-20260101.120000-1*" {
		t.Fatalf("integration %q", got)
	}
	// A virtual key searches its members.
	if got := versions("a=lib&repos=virt", "admin"); got != "3.0 2.0-SNAPSHOT* 1.10 1.1 1.0 0.9" {
		t.Fatalf("virtual %q", got)
	}
	if got := versions("a=lib", "deployer"); got != "2.0-SNAPSHOT* 1.10 1.1 1.0" {
		t.Fatalf("deployer %q", got)
	}
	// A pattern matching none of the versions there are lists nothing; no
	// versions at all is a 404, a release asked for as a module version
	// included, since it has no integration versions.
	if got := versions("a=lib&v=9.*", "admin"); got != "" {
		t.Fatalf("no match %q", got)
	}
	wantNotFound(t, w.do("GET", q+"a=none", "admin", ""), noVersions)
	wantNotFound(t, w.do("GET", q+"a=lib&v=1.0", "admin", ""), noVersions)
	vendor := w.do("GET", q+"a=lib", "admin", "", "Accept", mediaVersionsSearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaVersionsSearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q+"v=1.0", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"a=lib&remote=2", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"a=lib", "", ""), http.StatusUnauthorized)
}

func TestAccSearchLatestVersion(t *testing.T) {
	w := mavenWorld(t, true)
	// mix has a release candidate and a non-unique snapshot of 1.0; snap has
	// only a unique snapshot.
	w.put("/artifactory/libs-local/org/acme/mix/1.0-rc1/mix-1.0-rc1.jar", "rc")
	w.put("/artifactory/libs-local/org/acme/mix/1.0-SNAPSHOT/mix-1.0-SNAPSHOT.jar", "snapshot")
	w.put("/artifactory/libs-local/org/acme/snap/1.0-SNAPSHOT/snap-1.0-20260101.120000-1.jar", "s")
	const q = "/artifactory/api/search/latestVersion?"
	latest := func(query, as string, want string) {
		t.Helper()
		rec := w.do("GET", q+query, as, "")
		wantStatus(t, rec, http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain" || rec.Body.String() != want {
			t.Fatalf("%s: %s %q, want %q", query, ct, rec.Body, want)
		}
	}
	// The newest release in Mercury order; snapshots are not releases.
	latest("g=org.acme&a=lib&repos=libs-local", "admin", "1.10")
	latest("a=mix", "admin", "1.0-rc1")
	// A pattern takes the newest version of either kind it matches, by file
	// version, so *-SNAPSHOT matches a non-unique snapshot but no unique one.
	latest("a=lib&v=1.?", "admin", "1.5")
	latest("a=lib&v=1.?&repos=libs-local", "admin", "1.1")
	latest("a=lib&v=2.*&repos=libs-local", "admin", "2.0-20260101.120000-10")
	latest("a=mix&v=*-SNAPSHOT", "admin", "1.0-SNAPSHOT")
	// A module version asks for its newest integration version.
	latest("a=lib&v=2.0-SNAPSHOT", "admin", "2.0-20260101.120000-10")
	latest("a=mix&v=1.0-SNAPSHOT&remote=0", "admin", "1.0-SNAPSHOT")
	latest("a=lib&repos=virt", "admin", "3.0")
	latest("g=org.acme&a=lib", "deployer", "1.10")
	// Anonymous reads only up's cache.
	latest("a=lib", "", "0.9")

	// Each way of finding nothing has Artifactory's message.
	wantNotFound(t, w.do("GET", q+"a=none", "admin", ""), noVersions)
	wantNotFound(t, w.do("GET", q+"a=lib&repos=flat-local", "admin", ""), noVersions)
	wantNotFound(t, w.do("GET", q+"a=other&v=2.0-SNAPSHOT", "admin", ""), noVersions)
	wantNotFound(t, w.do("GET", q+"a=lib&v=1.0", "admin", ""), noVersions)
	wantNotFound(t, w.do("GET", q+"a=snap", "admin", ""), noLatestRelease)
	wantNotFound(t, w.do("GET", q+"a=lib&v=*-SNAPSHOT", "admin", ""), noLatestIntegration)
	wantNotFound(t, w.do("GET", q+"a=lib&v=9.*", "admin", ""), noLatestIntegration)
	wantStatus(t, w.do("GET", q, "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"a=lib&remote=9", "admin", ""), http.StatusBadRequest)
}

func TestAccUnder(t *testing.T) {
	if under("org/", "") != nil {
		t.Fatal("an empty prefix narrows nothing")
	}
	// Each prefix is a deeper path or a file in the prefix's folder.
	either, _ := under("org/acme/lib/", "lib").(aql.Or)
	if len(either) != 4 {
		t.Fatalf("under %#v", either)
	}
	at, _ := either[3].(aql.And)
	if dir, _ := at[0].(*aql.FieldCriterion); dir == nil || dir.Text != types.RootPath {
		t.Fatalf("a prefix without a slash is a name at the root: %#v", at)
	}
}

// TestAccLayoutSearchStoreErrors: a stored layout that cannot be read answers
// 500, unlike one that does not compile.
func TestAccLayoutSearchStoreErrors(t *testing.T) {
	w := searchWorld(t, false, func(c *Config) { c.Store = failingConfigs{c.Store} })
	for _, p := range []string{"gavc?a=lib", "versions?a=lib", "latestVersion?a=lib"} {
		rec := w.do("GET", "/artifactory/api/search/"+p, "admin", "")
		if rec.Code != http.StatusInternalServerError ||
			strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

// failingConfigs fails every configuration document read.
type failingConfigs struct{ store.Store }

func (f failingConfigs) Configs() store.ConfigStore { return failingConfigStore{f.Store.Configs()} }

type failingConfigStore struct{ store.ConfigStore }

func (failingConfigStore) Get(
	context.Context,
	types.ConfigKind,
	string,
) (types.ConfigDocument, error) {
	return types.ConfigDocument{}, errBoom
}
