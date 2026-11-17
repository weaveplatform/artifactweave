package chefhttp_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// grantTo lets user hold actions on the repositories keys alone: no "ANY"
// target, so the virtual repository is never named.
func grantTo(t *testing.T, f *fixture, name, user string, actions api.Actions, keys ...string) {
	t.Helper()
	targets := map[string]accesstest.Target{}
	for _, key := range keys {
		targets[key] = accesstest.Target{Include: []string{"**"}}
	}
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: name,
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{user: actions}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// answered asserts the status and a part of the body of got.
func answered(t *testing.T, got *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if got.Code != status || !strings.Contains(got.Body.String(), body) {
		t.Fatalf("got %d %s, want %d with %q", got.Code, got.Body, status, body)
	}
}

// A virtual repository walks nested virtual members (decision 0008, R2):
// each real member once, a cycle back to itself or to the outer virtual
// repository skipped, and a member reached twice kept at its first place.
// main answered 400 "invalid virtual repository member".
func TestAccChefVirtualWalksNestedMembersOnce(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	f.deploy(f.member("one", repo.Local, ""), "widget", "1.0.0", archive(t, "widget", "1.0.0"))
	f.deploy(f.member("other", repo.Local, ""), "widget", "2.0.0", archive(t, "widget", "2.0.0"))
	inner := f.member("inner", repo.Virtual, "")
	f.patch(inner.Key, func(c *repo.Config) { c.Repositories = []string{"other", "chef-local", "inner"} })
	f.patch(f.c.Key, func(c *repo.Config) {
		c.Repositories = []string{"inner", "chef-local", "one", "inner", "other"}
	})
	got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
	answered(t, got, 200, `/artifactory/api/chef/chef-local/api/v1/cookbooks/widget/versions/2_0_0","maintainer"`)
	if !strings.Contains(got.Body.String(), "versions/1_0_0") {
		t.Fatalf("nested record lacks 1.0.0: %s", got.Body)
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		got = f.do("GET", base+"/api/v1/cookbooks/widget/versions/"+strings.ReplaceAll(v, ".", "_")+"/download", "admin")
		if got.Code != 200 || got.Body.Len() == 0 {
			t.Fatalf("download %s: %d", v, got.Code)
		}
	}
	answered(t, f.do("GET", base+"/api/v1/cookbooks", "admin"), 200, `"total":1`)
}

// A virtual repository passes over a remote member whose upstream misses,
// one that fails, with a warning naming it [I], and one the caller may not
// read (decision 0008, R3), and lists the others. A caller who may read the
// members alone, with no "ANY" target, resolves through the virtual
// repository, and one who may read none gets Chef's not-found rather than
// 403 (R3).
func TestAccChefVirtualPassesOverMissingFailingAndUnreadableMembers(t *testing.T) {
	setup := func(t *testing.T, readError error) (*fixture, *bytes.Buffer) {
		logs := new(bytes.Buffer)
		s := upstream(t, archive(t, "widget", "1.0.0"))
		s.readError = readError
		f := newFixtureOptions(t, repo.Virtual,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = s }, nil,
			func(o *pkgroute.Options) { o.Logger = slog.New(slog.NewTextHandler(logs, nil)) })
		f.member("remote", repo.Remote, "https://example.com/chef")
		f.deploy(f.member("one", repo.Local, ""), "widget", "2.0.0", archive(t, "widget", "2.0.0"))
		f.patch(f.c.Key, func(c *repo.Config) { c.Repositories = []string{"remote", "one"} })
		return f, logs
	}
	f, _ := setup(t, nil)
	grantTo(t, f, "members", "reader", api.ActionRead, "one")
	grantTo(t, f, "nothing", "stranger", api.ActionRead, "elsewhere")
	got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
	answered(t, got, 200, "versions/1_0_0")
	got = f.do("GET", base+"/api/v1/cookbooks/widget", "reader")
	answered(t, got, 200, "versions/2_0_0")
	if strings.Contains(got.Body.String(), "1_0_0") {
		t.Fatalf("an unreadable member was listed: %s", got.Body)
	}
	answered(t, f.do("GET", base+"/api/v1/cookbooks/widget/versions/2_0_0", "reader"), 200, `"version":"2.0.0"`)
	answered(t, f.do("GET", base+"/api/v1/cookbooks/widget/versions/2_0_0/download", "reader"), 200, "")
	answered(t, f.do("GET", base+"/api/v1/cookbooks", "reader"), 200, `"cookbook_name":"widget"`)
	for _, path := range []string{
		"/api/v1/cookbooks/widget", "/api/v1/cookbooks/widget/versions/2_0_0",
		"/api/v1/cookbooks/widget/versions/2_0_0/download",
	} {
		answered(t, f.do("GET", base+path, "stranger"), 404, "Chef cookbook or version not found")
	}
	for _, tc := range []struct {
		name   string
		err    error
		warned bool
	}{{"miss", kernel.ErrNotFound, false}, {"fault", source.ErrUnavailable, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, logs := setup(t, tc.err)
			got := f.do("GET", base+"/api/v1/cookbooks/widget", "admin")
			answered(t, got, 200, "versions/2_0_0")
			if strings.Contains(got.Body.String(), "1_0_0") {
				t.Fatalf("the failing member was listed: %s", got.Body)
			}
			logged := strings.Contains(logs.String(), "virtual repository passed over a failing member") &&
				strings.Contains(logs.String(), "member=remote") &&
				strings.Contains(logs.String(), "virtual=chef-local")
			if logged != tc.warned {
				t.Fatalf("warning logged %v, want %v: %s", logged, tc.warned, logs)
			}
		})
	}
}

// A virtual repository's cookbook and version records are kept for its
// virtualRetrievalCachePeriodSecs (decision 0008, R8): 7.161.26 takes the
// period for Chef (VirtualRepositoryConfigurationImpl 91-93) and
// VirtualRepo.isCachedResourceExpired reads it. A member deploy leaves the
// kept record until it expires, -1 builds it on every read, and a rebuild
// that finds the cookbook in no member answers Chef's not-found and drops
// the kept record rather than serving it stale.
func TestAccChefVirtualRecordsAreKeptForTheRetrievalCachePeriod(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	cache := virtual.NewCache(virtual.CacheOptions{Now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}})
	f := newFixtureOptions(t, repo.Virtual, nil, nil, func(o *pkgroute.Options) { o.Cache = cache })
	one := f.member("one", repo.Local, "")
	first := f.deploy(one, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
	f.patch(f.c.Key, func(c *repo.Config) { c.Repositories = []string{"one"} })
	always := f.member("always", repo.Virtual, "")
	f.patch(always.Key, func(c *repo.Config) {
		c.Repositories = []string{"one"}
		c.VirtualRetrievalCachePeriodSecs = new(-1)
	})
	read := func(key, path string) string {
		t.Helper()
		got := f.do("GET", "/artifactory/api/chef/"+key+path, "admin")
		if got.Code != 200 {
			t.Fatalf("%s%s: %d %s", key, path, got.Code, got.Body)
		}
		return got.Body.String()
	}
	const record, latest = "/api/v1/cookbooks/widget", "/api/v1/cookbooks/widget/versions/latest"
	kept, keptLatest := read("chef-local", record), read("chef-local", latest)
	_ = read("always", record)
	second := f.deploy(one, "widget", "2.0.0", archive(t, "widget", "2.0.0"))
	if got := read("chef-local", record); got != kept {
		t.Fatalf("inside the period a member deploy showed through: %s", got)
	}
	if got := read("chef-local", latest); got != keptLatest {
		t.Fatalf("inside the period the latest version changed: %s", got)
	}
	for _, key := range []string{"always", "one"} {
		if got := read(key, record); !strings.Contains(got, "versions/2_0_0") {
			t.Fatalf("%s did not merge on the read: %s", key, got)
		}
	}
	advance(7201 * time.Second)
	if got := read("chef-local", record); !strings.Contains(got, "versions/2_0_0") {
		t.Fatalf("after the period the record was not built again: %s", got)
	}
	for _, it := range []item.Target{
		{Repo: one, Path: first.FullPath()}, {Repo: one, Path: second.FullPath()},
	} {
		if _, err := f.items.Delete(t.Context(), it, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	advance(7201 * time.Second)
	answered(t, f.do("GET", base+record, "admin"), 404, "Chef cookbook or version not found")
	f.deploy(one, "widget", "3.0.0", archive(t, "widget", "3.0.0"))
	if got := read("chef-local", record); !strings.Contains(got, "versions/3_0_0") {
		t.Fatalf("the dropped record was served again: %s", got)
	}
}

// shareTo posts a cookbook archive to the Chef repository key as user, as
// knife's share does.
func shareTo(f *fixture, key, user string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest("POST", "/artifactory/api/chef/"+key+"/api/v1/cookbooks/widget.tgz",
		bytes.NewReader(body))
	r.Header.Set("X-User", user)
	r.Header.Set("Content-Type", "application/x-binary")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, r)
	return w
}

// A share through a virtual repository lands in its default deployment
// repository, with Write asked there alone, and without one answers R6's
// 405 with Allow: GET before any permission is asked (decision 0008, R6)
// [I]. A delete through a virtual repository keeps its 405: R6's empty 404
// is WebDAV DELETE's, and no source shows the Chef handler forwarding one
// [I].
func TestAccChefSharesThroughAVirtualToItsDeploymentRepository(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	f.member("one", repo.Local, "")
	f.patch(f.c.Key, func(c *repo.Config) {
		c.Repositories = []string{"one"}
		c.DefaultDeploymentRepo = "one"
	})
	f.member("bare", repo.Virtual, "")
	f.patch("bare", func(c *repo.Config) { c.Repositories = []string{"one"} })
	grantTo(t, f, "writer", "writer", api.ActionRead|api.ActionWrite, "one")
	grantTo(t, f, "reader", "reader", api.ActionRead, "one")
	data := archive(t, "widget", "1.0.0")
	answered(t, shareTo(f, "chef-local", "reader", data), 403, "")
	got := shareTo(f, "chef-local", "writer", data)
	answered(t, got, 200, `/artifactory/api/chef/chef-local/api/v1/cookbooks/widget/versions/1_0_0/download"`)
	if _, err := f.db.Items().Get(t.Context(), "one", false, "widget/widget-1.0.0.tar.gz"); err != nil {
		t.Fatalf("the cookbook did not land in the deployment repository: %v", err)
	}
	answered(t, f.do("GET", base+"/api/v1/cookbooks/widget/versions/1_0_0", "writer"), 200, `"version":"1.0.0"`)
	got = shareTo(f, "bare", "", data)
	answered(t, got, 405,
		"No local repository was configured as local deployment repository for the (bare) virtual repository.")
	if got.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q", got.Header().Get("Allow"))
	}
	answered(t, f.do("DELETE", base+"/api/v1/cookbooks/widget/1_0_0", "admin"), 405,
		"cookbook delete requires a local or federated repository")
}
