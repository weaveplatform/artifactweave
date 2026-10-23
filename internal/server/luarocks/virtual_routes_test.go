package luarocks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const virtualBase = "/artifactory/api/luarocks/combined/"

func newVirtualFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newRemoteFixture(t)
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

func TestVirtualManifestAndArtifactResolution(t *testing.T) {
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

func TestVirtualUploadUsesLocalDeploymentTarget(t *testing.T) {
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

func TestVirtualRejectsUnsupportedMembers(t *testing.T) {
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
}
