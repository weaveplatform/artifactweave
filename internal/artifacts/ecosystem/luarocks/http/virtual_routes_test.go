package luarockshttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

const virtualBase = "/artifactory/api/luarocks/combined/"

func newVirtualFixture(
	t *testing.T,
	tune ...func(*pkgroute.Options, *item.Service),
) *remoteFixture {
	t.Helper()
	f := newRemoteFixture(t, tune...)
	f.seed()
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.LuaRocks})
	if err := f.db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	virtual := *repo.Defaults(&repo.Config{
		Key: "combined", Class: repo.Virtual, PackageType: repo.LuaRocks,
		Repositories: []string{"local", "rocks"}, DefaultDeploymentRepo: "local",
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "partial-virtual",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"partial": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				"combined": {Include: []string{"**"}},
				"local":    {Include: []string{"local-*"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

type virtualFaultStore struct {
	pkgroute.Store
	failKey *string
}

func (s virtualFaultStore) Repos() port.RepoStore {
	return virtualFaultRepos{RepoStore: s.Store.Repos(), failKey: s.failKey}
}

type virtualFaultRepos struct {
	port.RepoStore
	failKey *string
}

func (s virtualFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == *s.failKey {
		return repo.Config{}, errors.New("member repository store unavailable")
	}
	return s.RepoStore.Get(ctx, key)
}

func uploadSpecTo(t *testing.T, f *fixture, key, name, version string, content []byte) int64 {
	t.Helper()
	filename := name + "-" + version + ".rockspec"
	body, contentType := form(t, "rockspec_file", filename, content)
	rec := f.do(http.MethodPost, "/artifactory/api/luarocks/"+key+"/api/1/secret/upload",
		body, contentType, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s: %d %s", filename, rec.Code, rec.Body)
	}
	var response struct {
		ModuleURL string `json:"module_url"`
		Version   struct {
			ID int64 `json:"id"`
		} `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Version.ID == 0 {
		t.Fatalf("upload response: %v %s", err, rec.Body)
	}
	if !strings.Contains(response.ModuleURL, "/"+key+"/") {
		t.Fatalf("upload response URL points outside %s: %s", key, response.ModuleURL)
	}
	return response.Version.ID
}

func TestAccVirtualManifestAndArtifactResolution(t *testing.T) {
	f := newVirtualFixture(t)
	localSpec := append(spec("visible", "1.0-1"), []byte("-- local\n")...)
	uploadSpecTo(t, f.fixture, "local", "visible", "1.0-1", localSpec)
	uploadSpecTo(t, f.fixture, "local", "local-only", "2.0-1", spec("local-only", "2.0-1"))
	for _, name := range []string{"manifest", "manifest-5.4"} {
		rec := f.do(http.MethodGet, virtualBase+name, nil, "", "admin")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "local-only") ||
			!strings.Contains(
				rec.Body.String(),
				"visible",
			) || !strings.Contains(rec.Body.String(), "hidden") {
			t.Fatalf("merged %s: %d %s", name, rec.Code, rec.Body)
		}
		partial := f.do(http.MethodGet, virtualBase+name, nil, "", "partial")
		if partial.Code != 200 || !strings.Contains(partial.Body.String(), "local-only") ||
			!strings.Contains(
				partial.Body.String(),
				"visible",
			) || strings.Contains(partial.Body.String(), "hidden") {
			t.Fatalf("filtered %s: %d %s", name, partial.Code, partial.Body)
		}
	}
	if rec := f.do(
		http.MethodGet,
		virtualBase+"hidden-1.0-1.rockspec",
		nil,
		"",
		"partial",
	); rec.Code != http.StatusNotFound &&
		rec.Code != http.StatusForbidden {
		t.Fatalf("denied member file: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/hidden-1.0-1.rockspec"); got != 0 {
		t.Fatalf("denied member reached upstream: %d", got)
	}
	got := f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), localSpec) {
		t.Fatalf("first member artifact: %d %s", got.Code, got.Body)
	}
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"rocks", "local"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	// R1 searches local members before remote ones whatever the
	// configured order, unless only the remote sets priorityResolution.
	got = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), localSpec) {
		t.Fatalf("reordered member artifact: %d %s", got.Code, got.Body)
	}
	patchRepo(t, f.fixture, "rocks", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	got = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if got.Code != 200 || bytes.Equal(got.Body.Bytes(), localSpec) {
		t.Fatalf("priority remote member artifact: %d %s", got.Code, got.Body)
	}
	patchRepo(t, f.fixture, "rocks", func(c *repo.Config) { c.PriorityResolution = repo.Flex(false) })
	got = f.do(http.MethodGet, virtualBase+"local-only-2.0-1.rockspec", nil, "", "admin")
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), spec("local-only", "2.0-1")) {
		t.Fatalf("fallback from remote to local: %d %s", got.Code, got.Body)
	}
	f.up.Set("/rocks/manifest-5.4", fakeupstream.Resource{Body: []byte(`commands = {}
modules = {}
repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" } }, ["2.0-1"] = { { arch = "rockspec" } } } }
`)})
	f.clock.Advance(11 * time.Second)
	got = f.do(http.MethodGet, virtualBase+"manifest-5.4", nil, "", "admin")
	if got.Code != 200 || !strings.Contains(got.Body.String(), "2.0-1") {
		t.Fatalf("refreshed remote member: %d %s", got.Code, got.Body)
	}
}

// TestAccVirtualWalksNestedMembersForMemberReaders: a virtual repository
// over [remote, nested virtual -> local] reads the local member through the
// nested one, and a nested virtual repository that lists the outer one
// back, or one that lists itself, is skipped (R2) without a 400 or 500. A
// caller with Read on the members alone, and none on the virtual
// repository, resolves through it (R3); one who may read only the local
// member sees only its rocks, and the remote's are not found. A remote
// member whose upstream fails is passed over [I].
func TestAccVirtualWalksNestedMembersForMemberReaders(t *testing.T) {
	f := newVirtualFixture(t)
	uploadSpecTo(t, f.fixture, "local", "local-only", "2.0-1", spec("local-only", "2.0-1"))
	nested := *repo.Defaults(&repo.Config{
		Key: "nested", Class: repo.Virtual, PackageType: repo.LuaRocks,
		Repositories: []string{"local", "combined", "nested"},
	})
	if err := f.db.Repos().Create(t.Context(), nested); err != nil {
		t.Fatal(err)
	}
	patchRepo(t, f.fixture, "combined", func(c *repo.Config) {
		c.Repositories = []string{"rocks", "nested", "combined"}
	})
	for user, targets := range map[string]map[string]accesstest.Target{
		"members":    {"local": {Include: []string{"**"}}, "rocks": {Include: []string{"**"}}},
		"local-only": {"local": {Include: []string{"**"}}},
	} {
		if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
			Name: user,
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{user: api.ActionRead}},
				Targets: targets,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"combined", "nested"} {
		base := "/artifactory/api/luarocks/" + key + "/"
		for _, user := range []string{"admin", "members"} {
			wantBody(t, f.do(http.MethodGet, base+"manifest", nil, "", user),
				[]string{`["local-only"]`, `["visible"]`, `["hidden"]`}, nil)
			for path, want := range map[string][]byte{
				"local-only-2.0-1.rockspec": spec("local-only", "2.0-1"),
				"visible-1.0-1.src.rock":    rock(t, "visible", "1.0-1"),
			} {
				rec := f.do(http.MethodGet, base+path, nil, "", user)
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
					t.Fatalf("%s%s as %s: %d %s", base, path, user, rec.Code, rec.Body)
				}
			}
		}
	}
	wantBody(t, f.do(http.MethodGet, virtualBase+"manifest", nil, "", "local-only"),
		[]string{`["local-only"]`}, []string{"visible", "hidden"})
	wantError(t, f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "local-only"),
		http.StatusNotFound, "Could not find resource")

	broken := *repo.Defaults(&repo.Config{
		Key: "broken", Class: repo.Remote, PackageType: repo.LuaRocks, URL: f.up.URL + "/broken/",
	})
	if err := f.db.Repos().Create(t.Context(), broken); err != nil {
		t.Fatal(err)
	}
	f.up.Set("/broken/manifest", fakeupstream.Resource{Status: http.StatusInternalServerError})
	f.up.Set("/broken/visible-1.0-1.rockspec",
		fakeupstream.Resource{Status: http.StatusInternalServerError})
	patchRepo(t, f.fixture, "combined", func(c *repo.Config) {
		c.Repositories = []string{"broken", "nested"}
	})
	wantBody(t, f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin"),
		[]string{`["local-only"]`}, []string{"visible"})
	if n := f.up.Count("", "/broken/manifest"); n == 0 {
		t.Fatal("the failing remote member was never asked")
	}
	rec := f.do(http.MethodGet, virtualBase+"local-only-2.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("local rock past a failing remote: %d %s", rec.Code, rec.Body)
	}
	wantError(t, f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin"),
		http.StatusNotFound, "Could not find resource")
}

// wantNoDeployTarget checks rec is R6's answer to a deploy to the virtual
// repository "combined" without a local or federated deployment target.
func wantNoDeployTarget(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	wantError(t, rec, http.StatusMethodNotAllowed, "No local repository was configured as "+
		"local deployment repository for the (combined) virtual repository.")
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow: %q", allow)
	}
}

func TestAccVirtualUploadUsesLocalDeploymentTarget(t *testing.T) {
	f := newVirtualFixture(t)
	versionID := uploadSpecTo(
		t,
		f.fixture,
		"combined",
		"published",
		"1.0-1",
		spec("published", "1.0-1"),
	)
	if _, err := f.db.Items().
		Get(t.Context(), "local", false, "published-1.0-1.rockspec"); err != nil {
		t.Fatalf("missing local target: %v", err)
	}
	if _, err := f.db.Items().
		Get(t.Context(), "rocks", false, "published-1.0-1.rockspec"); err == nil {
		t.Fatal("virtual upload wrote to remote member")
	}
	body, ct := form(t, "rock_file", "published-1.0-1.src.rock", rock(t, "published", "1.0-1"))
	rec := f.do(
		http.MethodPost,
		fmt.Sprintf("%sapi/1/secret/upload_rock/%d", virtualBase, versionID),
		body,
		ct,
		"admin",
	)
	if rec.Code != 200 {
		t.Fatalf("upload rock: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().
		Get(t.Context(), "local", false, "published-1.0-1.src.rock"); err != nil {
		t.Fatalf("missing local rock: %v", err)
	}
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.DefaultDeploymentRepo = ""
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	body, ct = form(t, "rockspec_file", "no-target-1.0-1.rockspec", spec("no-target", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	wantNoDeployTarget(t, rec)
}

// TestAccVirtualPassesOverUnsupportedMembersAndDeploysToAFederatedTarget:
// a member of another package type, which only data saved before the save
// checks can name (R11), is passed over, and a federated member is read
// and deployed to as a local one is (decision 0008, R6).
func TestAccVirtualPassesOverUnsupportedMembersAndDeploysToAFederatedTarget(t *testing.T) {
	f := newVirtualFixture(t)
	other := *repo.Defaults(&repo.Config{Key: "other", Class: repo.Local, PackageType: repo.NPM})
	if err := f.db.Repos().Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"other", "rocks"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin"),
		[]string{`["visible"]`, `["hidden"]`}, nil)
	rec := f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), spec("visible", "1.0-1")) {
		t.Fatalf("rock past a member of another type: %d %s", rec.Code, rec.Body)
	}
	peer := *repo.Defaults(&repo.Config{Key: "peer", Class: repo.Federated, PackageType: repo.LuaRocks})
	if err := f.db.Repos().Create(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"peer"}
	virtual.DefaultDeploymentRepo = "peer"
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin"),
		nil, []string{"visible"})
	id := uploadSpecTo(t, f.fixture, "combined", "federated", "1.0-1", spec("federated", "1.0-1"))
	if _, err := f.db.Items().Get(t.Context(), "peer", false, "federated-1.0-1.rockspec"); err != nil {
		t.Fatalf("rockspec through the virtual repository: %v", err)
	}
	body, ct := form(t, "rock_file", "federated-1.0-1.src.rock", rock(t, "federated", "1.0-1"))
	rec = f.do(http.MethodPost, fmt.Sprintf("%sapi/1/secret/upload_rock/%d", virtualBase, id),
		body, ct, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("rock through the virtual repository: %d %s", rec.Code, rec.Body)
	}
	wantBody(t, f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin"),
		[]string{`["federated"]`, `"src"`}, nil)
	rec = f.do(http.MethodGet, virtualBase+"federated-1.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), spec("federated", "1.0-1")) {
		t.Fatalf("federated member's rockspec: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualReadRejectsQueriesAndConcealsDeniedMembers(t *testing.T) {
	f := newVirtualFixture(t)
	for _, tc := range []struct {
		path, user string
		expected   int
	}{
		{virtualBase + "manifest?upstream=https://elsewhere.example/", "admin", http.StatusBadRequest},
		{virtualBase + "not-a-rock", "admin", http.StatusNotFound},
		{virtualBase + "missing-1.0-1.rockspec", "admin", http.StatusNotFound},
		{virtualBase + "manifest", "nobody", http.StatusForbidden},
		// A caller who may read no member finds no rock (R3).
		{virtualBase + "visible-1.0-1.rockspec", "nobody", http.StatusNotFound},
	} {
		rec := f.do(http.MethodGet, tc.path, nil, "", tc.user)
		if rec.Code != tc.expected {
			t.Fatalf("%s as %s: %d %s", tc.path, tc.user, rec.Code, rec.Body)
		}
	}
	if got := f.up.Count(http.MethodGet, "/rocks/missing-1.0-1.rockspec"); got != 0 {
		t.Fatalf("missing rock fetched before manifest authority: %d", got)
	}
	rec := f.do(http.MethodGet, virtualBase+"visible-1.0-1.src.rock", nil, "", "partial")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), rock(t, "visible", "1.0-1")) {
		t.Fatalf("authorized remote member rock: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodHead,
		virtualBase+"manifest-5.4",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusOK ||
		rec.Body.Len() != 0 {
		t.Fatalf("virtual manifest HEAD: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualDeploymentRequiresValidLocalTargetAndBothGrants(t *testing.T) {
	f := newVirtualFixture(t)
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "virtual-publisher",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"publisher": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{"combined": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	body, ct := form(t, "rockspec_file", "denied-1.0-1.rockspec", spec("denied", "1.0-1"))
	rec := f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "partial")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only publisher: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().
		Get(t.Context(), "local", false, "denied-1.0-1.rockspec"); err == nil {
		t.Fatal("denied upload wrote to target")
	}
	body, ct = form(t, "rockspec_file", "denied-1.0-1.rockspec", spec("denied", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "publisher")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("publisher without target grant: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet,
		virtualBase+"api/1/secret/check_rockspec?package=visible&version=1.0-1",
		nil, "", "publisher")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("target read denied: %d %s", rec.Code, rec.Body)
	}
	// A grant naming the virtual repository alone, which no 7.x permission
	// target can express, reads no member, so it reads no manifest.
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "publisher")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "visible") {
		t.Fatalf("member without grant appeared in virtual index: %d %s", rec.Code, rec.Body)
	}
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.DefaultDeploymentRepo = "rocks"
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	body, ct = form(
		t,
		"rockspec_file",
		"remote-target-1.0-1.rockspec",
		spec("remote-target", "1.0-1"),
	)
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	wantNoDeployTarget(t, rec)
	if _, err := f.db.Items().
		Get(t.Context(), "rocks", false, "remote-target-1.0-1.rockspec"); err == nil {
		t.Fatal("virtual upload wrote to remote target")
	}
}

// TestAccVirtualManifestIsEmptyWithoutMembersAndPassesOverABadRemote: a
// virtual repository without members lists nothing, and a remote member
// whose upstream manifest fails LuaRocks' checks is passed over as a
// failing upstream is [I], so its rocks are not found.
func TestAccVirtualManifestIsEmptyWithoutMembersAndPassesOverABadRemote(t *testing.T) {
	f := newVirtualFixture(t)
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{}
	virtual.DefaultDeploymentRepo = ""
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "visible") {
		t.Fatalf("empty virtual manifest: %d %s", rec.Code, rec.Body)
	}
	virtual.Repositories = []string{"rocks"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	f.up.Set(
		"/rocks/manifest",
		fakeupstream.Resource{Body: []byte(`repository = os.execute("bad")`)},
	)
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "visible") {
		t.Fatalf("invalid member manifest: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rock with invalid member manifest: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualRevalidatesMemberConfigurationAtRequestTime(t *testing.T) {
	f := newVirtualFixture(t)
	virtual, err := f.db.Repos().Get(t.Context(), "combined")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"gone", "rocks"}
	virtual.DefaultDeploymentRepo = ""
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "visible") {
		t.Fatalf("missing member did not fall through: %d %s", rec.Code, rec.Body)
	}
	virtual.Repositories = []string{"rocks", "rocks"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	// A member listed twice keeps its first place (R2).
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `["visible"]`) != 1 {
		t.Fatalf("duplicate member: %d %s", rec.Code, rec.Body)
	}
	virtual.Repositories = []string{"gone"}
	virtual.DefaultDeploymentRepo = "gone"
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	body, ct := form(t, "rockspec_file", "unsafe-1.0-1.rockspec", spec("unsafe", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	wantNoDeployTarget(t, rec)
	nested := *repo.Defaults(&repo.Config{
		Key: "nested", Class: repo.Virtual, PackageType: repo.LuaRocks,
		Repositories: []string{"rocks"},
	})
	if err := f.db.Repos().Create(t.Context(), nested); err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"nested"}
	virtual.DefaultDeploymentRepo = ""
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `["visible"]`) {
		t.Fatalf("nested virtual member: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualStoreFaultsFailClosed(t *testing.T) {
	var failKey string
	f := newVirtualFixture(t, func(options *pkgroute.Options, _ *item.Service) {
		options.Store = virtualFaultStore{Store: options.Store, failKey: &failKey}
	})
	failKey = "rocks"
	rec := f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("member lookup fault: %d %s", rec.Code, rec.Body)
	}
	failKey = "local"
	body, ct := form(t, "rockspec_file", "unavailable-1.0-1.rockspec",
		spec("unavailable", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("target lookup fault: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().Get(t.Context(), "local", false,
		"unavailable-1.0-1.rockspec"); err == nil {
		t.Fatal("upload wrote through a failed target lookup")
	}
}

func TestAccVirtualRejectsChangedDeploymentTargetType(t *testing.T) {
	f := newVirtualFixture(t)
	local, err := f.db.Repos().Get(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	local.PackageType = repo.NPM
	if err := f.db.Repos().Update(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	body, ct := form(t, "rockspec_file", "wrong-type-1.0-1.rockspec",
		spec("wrong-type", "1.0-1"))
	rec := f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("changed deployment target type: %d %s", rec.Code, rec.Body)
	}
	// The member is passed over for the manifest (R11).
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `["visible"]`) {
		t.Fatalf("changed member package type: %d %s", rec.Code, rec.Body)
	}
}
