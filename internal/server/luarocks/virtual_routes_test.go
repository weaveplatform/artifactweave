package luarocks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "partial-virtual",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"partial": types.ActionRead}},
			Targets: map[string]types.Target{
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
	store.Store
	failKey *string
}

func (s virtualFaultStore) Repos() store.RepoStore {
	return virtualFaultRepos{RepoStore: s.Store.Repos(), failKey: s.failKey}
}

type virtualFaultRepos struct {
	store.RepoStore
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
	got = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if got.Code != 200 || bytes.Equal(got.Body.Bytes(), localSpec) {
		t.Fatalf("reordered member artifact: %d %s", got.Code, got.Body)
	}
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
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("missing deployment target: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualRejectsUnsupportedMembers(t *testing.T) {
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
	rec := f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported member: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported member artifact: %d %s", rec.Code, rec.Body)
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
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported federated member: %d %s", rec.Code, rec.Body)
	}
	body, ct := form(t, "rockspec_file", "unsupported-1.0-1.rockspec", spec("unsupported", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported federated deployment target: %d %s", rec.Code, rec.Body)
	}
}

func TestAccVirtualReadRejectsQueriesAndConcealsDeniedMembers(t *testing.T) {
	f := newVirtualFixture(t)
	for _, tc := range []struct {
		path, user string
		want       int
	}{
		{virtualBase + "manifest?upstream=https://elsewhere.example/", "admin", http.StatusBadRequest},
		{virtualBase + "not-a-rock", "admin", http.StatusNotFound},
		{virtualBase + "missing-1.0-1.rockspec", "admin", http.StatusNotFound},
		{virtualBase + "manifest", "nobody", http.StatusForbidden},
		{virtualBase + "visible-1.0-1.rockspec", "nobody", http.StatusForbidden},
	} {
		rec := f.do(http.MethodGet, tc.path, nil, "", tc.user)
		if rec.Code != tc.want {
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
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "virtual-publisher",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"publisher": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"combined": {Include: []string{"**"}}},
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
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "publisher")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "visible") {
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remote deployment target: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().
		Get(t.Context(), "rocks", false, "remote-target-1.0-1.rockspec"); err == nil {
		t.Fatal("virtual upload wrote to remote target")
	}
}

func TestAccVirtualManifestIsEmptyWithoutMembersAndFailsClosedOnBadRemote(t *testing.T) {
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
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("invalid member manifest: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, virtualBase+"visible-1.0-1.rockspec", nil, "", "admin")
	if rec.Code != http.StatusBadGateway {
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
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate member: %d %s", rec.Code, rec.Body)
	}
	virtual.Repositories = []string{"gone"}
	virtual.DefaultDeploymentRepo = "gone"
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	body, ct := form(t, "rockspec_file", "unsafe-1.0-1.rockspec", spec("unsafe", "1.0-1"))
	rec = f.do(http.MethodPost, virtualBase+"api/1/secret/upload", body, ct, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleted deployment member: %d %s", rec.Code, rec.Body)
	}
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
	if rec.Code != http.StatusBadRequest {
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
	rec = f.do(http.MethodGet, virtualBase+"manifest", nil, "", "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("changed member package type: %d %s", rec.Code, rec.Body)
	}
}
