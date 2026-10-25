package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildSearchWorld is an item world with builds: app runs 1 (twice) and 2,
// tool 5, hidden 1 and project proj's tool 6 all depend on the file "dep";
// app 1 produced a.bin and b-sources.jar, app 2 a.bin, and the first run of
// app 1 was promoted Released. a.bin carries the build properties of both
// numbers in libs-local and in other-local, and none in copy/. deployer
// reads app's runs.
func buildSearchWorld(t *testing.T, anonymous bool, tune ...func(*Config)) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous, tune...)
	ctx := context.Background()
	proj := repo.Config{Key: "proj-build-info", Class: repo.Local, PackageType: repo.BuildInfo}
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&proj)); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deployer-builds", Resources: map[types.ResourceType]types.Resource{
			types.ResourceBuild: {
				Actions: types.Grants{
					Users: map[string]types.Actions{"deployer": types.ActionRead},
				},
				Targets: map[string]types.Target{
					types.BuildInfoRepository: {Include: []string{"app/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	w.put("/artifactory/libs-local/copy/a.bin", "artifact a")
	w.put("/artifactory/libs-local/app/a.bin;build.name=app;build.number=1;build.number=2",
		"artifact a")
	w.put("/artifactory/libs-local/app/b-sources.jar;build.name=app;build.number=1", "artifact b")
	w.put("/artifactory/other-local/app/a.bin;build.name=app;build.number=1;build.number=2",
		"artifact a")
	file := func(name, content string) string {
		return fmt.Sprintf(`{"name":%q,"sha1":%q}`, name, sha1Hex(content))
	}
	dep := `{"id":"org:dep:1.0","sha1":"` + sha1Hex("dep") + `"}`
	svc := build.Service{Store: w.db, Blobs: w.blobs}
	started := func(n int) string {
		s, err := build.ParseStarted(testStarted)
		if err != nil {
			t.Fatal(err)
		}
		return build.FormatStarted(s.Add(time.Duration(n) * time.Second))
	}
	runs := map[string]types.Build{}
	both := file("a.bin", "artifact a") + "," + file("b-sources.jar", "artifact b")
	for n, r := range []struct{ key, project, name, number, artifacts string }{
		{"app1", "", "app", "1", both},
		{"app1b", "", "app", "1", both},
		{"app2", "", "app", "2", file("a.bin", "artifact a")},
		{"tool5", "", "tool", "5", ""},
		{"hidden1", "", "hidden", "1", ""},
		{"tool6", "proj", "tool", "6", ""},
	} {
		doc := fmt.Sprintf(`{"name":%q,"number":%q,"started":%q,"modules":[`+
			`{"id":"m","artifacts":[%s],"dependencies":[%s]}]}`,
			r.name, r.number, started(n), r.artifacts, dep)
		p, err := svc.Publish(ctx, r.project, []byte(doc), "admin", allowBuilds{})
		if err != nil {
			t.Fatal(err)
		}
		runs[r.key] = p.Run
	}
	if _, err := w.db.Builds().AddPromotion(ctx, types.BuildPromotion{
		BuildID: runs["app1"].ID, Status: "Released", Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestAccSearchDependency(t *testing.T) {
	w := buildSearchWorld(t, true)
	const q = "/artifactory/api/search/dependency?sha1="
	builds := func(sha1, as string) []string {
		t.Helper()
		var out []string
		for _, r := range hitsOf(t, w.do("GET", q+sha1, as, "")) {
			out = append(out, strings.TrimPrefix(fmt.Sprint(r["uri"]),
				"https://reg.example/artifactory/api/build/"))
		}
		return out
	}
	// A number with two runs is listed once.
	if got := builds(sha1Hex("dep"), "admin"); !slices.Equal(got,
		[]string{"app/1", "app/2", "tool/5", "hidden/1", "tool/6?project=proj"}) {
		t.Fatalf("admin %v", got)
	}
	if got := builds(strings.ToUpper(sha1Hex("dep")), "deployer"); !slices.Equal(got,
		[]string{"app/1", "app/2"}) {
		t.Fatalf("deployer %v", got)
	}
	if got := builds(sha1Hex("dep"), ""); got != nil {
		t.Fatalf("anonymous %v", got)
	}
	if got := builds(sha1Hex("artifact a"), "admin"); got != nil {
		t.Fatalf("an artifact is no dependency: %v", got)
	}
	vendor := w.do("GET", q+sha1Hex("dep"), "admin", "", "Accept", mediaDependencySearch)
	if ct := vendor.Header().Get("Content-Type"); ct != mediaDependencySearch {
		t.Fatalf("vendor content type %q", ct)
	}
	wantStatus(t, w.do("GET", q+"abc", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", "/artifactory/api/search/dependency", "admin", ""),
		http.StatusBadRequest)
}

func TestAccSearchBuildArtifacts(t *testing.T) {
	w := buildSearchWorld(t, true)
	const q = "/artifactory/api/search/buildArtifacts"
	const dl = "https://reg.example/artifactory/"
	json := []string{"Content-Type", "application/json"}
	artifacts := func(body, as string, headers ...string) []string {
		t.Helper()
		var out []string
		headers = append([]string{"Content-Type", "application/json"}, headers...)
		for _, r := range hitsOf(t, w.do("POST", q, as, body, headers...)) {
			out = append(out, strings.TrimPrefix(fmt.Sprint(r["downloadUri"]), dl))
		}
		return out
	}
	for _, tc := range []struct {
		body string
		want []string
	}{
		// One file per artifact, carrying the run's build properties: the
		// first found, so libs-local's a.bin, never copy/'s.
		{
			`{"buildName":"app","buildNumber":"1"}`,
			[]string{"libs-local/app/a.bin", "libs-local/app/b-sources.jar"},
		},
		{`{"buildName":"app","buildNumber":"LATEST"}`, []string{"libs-local/app/a.bin"}},
		// The status alone selects run 1; repos and mappings narrow its files.
		{
			`{"buildName":"app","buildNumber":" ","buildStatus":"Released",` +
				`"repos":["libs-local"," ",""],"mappings":[{"input":"(.+)-sources.jar"},{"input":""}]}`,
			[]string{"libs-local/app/b-sources.jar"},
		},
		// repos chooses among the files that hold an artifact.
		{`{"buildName":"app","buildNumber":"1","repos":["other-local"]}`, []string{"other-local/app/a.bin"}},
		{`{"buildName":"app","buildNumber":"1","repos":["virt"]}`, []string{
			"libs-local/app/a.bin", "libs-local/app/b-sources.jar",
		}},
	} {
		if got := artifacts(tc.body, "admin"); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.body, got, tc.want)
		}
	}
	// deployer reads app's runs, and libs-local only.
	if got := artifacts(`{"buildName":"app","buildNumber":"2"}`, "deployer",
		"Content-Type", "application/vnd.org.jfrog.artifactory.search.BuildArtifactsRequest+json",
		"Accept", mediaBuildArtifactsSearch); !slices.Equal(got, []string{"libs-local/app/a.bin"}) {
		t.Fatalf("deployer %v", got)
	}
	// Finding no file is a 404 naming what was asked for, whether the run is
	// missing or nothing it produced is left.
	const none = "Could not find any build artifacts for build "
	for body, want := range map[string]string{
		`{"buildName":"app","buildNumber":"9"}`:                            none + "'app' number '9'",
		`{"buildName":"none","buildNumber":"LATEST"}`:                      none + "'none' number 'LATEST'",
		`{"buildName":"tool","buildStatus":"Released"}`:                    none + "'tool' status 'Released'",
		`{"buildName":"app","buildNumber":"1","repos":["up"]}`:             none + "'app' number '1'",
		`{"buildName":"tool","buildNumber":"5"}`:                           none + "'tool' number '5'",
		`{"buildName":"app","buildNumber":"1","mappings":[{"input":"x"}]}`: none + "'app' number '1'",
	} {
		wantNotFound(t, w.do("POST", q, "admin", body, json...), want)
	}
	// The body is checked before the caller: a name, and a number or a
	// status but not both.
	for body, want := range map[string]string{
		`{"buildNumber":"1"}`:                                   "Cannot search without build name.",
		`{"buildName":" ","buildNumber":"1"}`:                   "Cannot search without build name.",
		`{"buildName":"app"}`:                                   "Cannot search without build number or build status.",
		`{"buildName":"app","buildNumber":"","buildStatus":""}`: "Cannot search without build number or build status.",
		`{"buildName":"app","buildNumber":"LATEST","buildStatus":"Released"}`: "Cannot search with both build " +
			"number and build status parameters, please omit build number if your are looking for latest " +
			"build by status or omit build status to search for specific build version.",
	} {
		for _, as := range []string{"admin", ""} {
			rec := w.do("POST", q, as, body, json...)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
				t.Errorf("%s as %q: %d %s, want 400 %q", body, as, rec.Code, rec.Body, want)
			}
		}
	}
	for body, want := range map[string]int{
		`{"buildName":"app","buildNumber":"1","mappings":[{"input":"("}]}`: http.StatusBadRequest,
		`[]`: http.StatusBadRequest,
	} {
		if rec := w.do("POST", q, "admin", body, json...); rec.Code != want {
			t.Errorf("%s: status %d, want %d: %s", body, rec.Code, want, rec.Body)
		}
	}
	wantStatus(t, w.do("POST", q, "deployer", `{"buildName":"tool","buildNumber":"5"}`, json...),
		http.StatusForbidden)
	// Anonymous callers are told to authenticate, even with anonymous
	// access on and read on the run, which the dependency search shows.
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "anonymous-builds", Resources: map[types.ResourceType]types.Resource{
			types.ResourceBuild: {
				Actions: types.Grants{
					Users: map[string]types.Actions{auth.AnonymousUser: types.ActionRead},
				},
				Targets: map[string]types.Target{
					types.BuildInfoRepository: {Include: []string{"app/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(hitsOf(t, w.do("GET", "/artifactory/api/search/dependency?sha1="+sha1Hex("dep"),
		"", ""))); n != 2 {
		t.Fatalf("anonymous reads %d of app's runs", n)
	}
	wantStatus(t, w.do("POST", q, "", `{"buildName":"app","buildNumber":"1"}`, json...),
		http.StatusUnauthorized)
	wantStatus(t, w.do("POST", q, "admin", "", "Content-Type", "text/plain"),
		http.StatusBadRequest)
}

// TestAccBuildSearchCaps: the AQL hard cap bounds the build searches too.
func TestAccBuildSearchCaps(t *testing.T) {
	w := buildSearchWorld(t, false, func(c *Config) { c.AQL.MaxResults = 1 })
	if n := len(hitsOf(t, w.do("GET", "/artifactory/api/search/dependency?sha1="+sha1Hex("dep"),
		"admin", ""))); n != 1 {
		t.Fatalf("dependency %d results", n)
	}
	if n := len(hitsOf(t, w.do("POST", "/artifactory/api/search/buildArtifacts", "admin",
		`{"buildName":"app","buildNumber":"1"}`, "Content-Type", "application/json"))); n != 1 {
		t.Fatalf("build artifacts %d results", n)
	}
}

// TestAccBuildSearchStoreErrors: a build store that fails answers 500.
func TestAccBuildSearchStoreErrors(t *testing.T) {
	for fail, req := range map[string][2]string{
		"dependencies": {"GET /artifactory/api/search/dependency?sha1=" + sha1Hex("dep"), ""},
		"runs":         {"POST /artifactory/api/search/buildArtifacts", `{"buildName":"app","buildNumber":"1"}`},
		"find":         {"POST /artifactory/api/search/buildArtifacts", `{"buildName":"app","buildNumber":"1"}`},
	} {
		w := buildSearchWorld(
			t,
			false,
			func(c *Config) { c.Store = buildSearchFaults{c.Store, fail} },
		)
		method, path, _ := strings.Cut(req[0], " ")
		rec := w.do(method, path, "admin", req[1], "Content-Type", "application/json")
		if rec.Code != http.StatusInternalServerError ||
			strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: %d %s", fail, rec.Code, rec.Body)
		}
	}
}

// buildSearchFaults fails the build or item store operation named fail.
type buildSearchFaults struct {
	store.Store
	fail string
}

func (f buildSearchFaults) Builds() store.BuildStore {
	return faultyBuildSearch{f.Store.Builds(), f.fail}
}

func (f buildSearchFaults) Items() store.ItemStore {
	return faultyChecksumSearch{f.Store.Items(), f.fail}
}

type faultyBuildSearch struct {
	store.BuildStore
	fail string
}

func (f faultyBuildSearch) FindDependencies(
	ctx context.Context,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	if f.fail == "dependencies" {
		return nil, errBoom
	}
	return f.BuildStore.FindDependencies(ctx, c)
}

func (f faultyBuildSearch) Runs(
	ctx context.Context,
	project, name string,
	p store.Page,
) ([]types.BuildRun, string, error) {
	if f.fail == "runs" {
		return nil, "", errBoom
	}
	return f.BuildStore.Runs(ctx, project, name, p)
}

type faultyChecksumSearch struct {
	store.ItemStore
	fail string
}

func (f faultyChecksumSearch) FindByChecksum(
	ctx context.Context,
	c types.Checksums,
	p store.Page,
) ([]types.Item, string, error) {
	if f.fail == "find" {
		return nil, "", errBoom
	}
	return f.ItemStore.FindByChecksum(ctx, c, p)
}
