package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// accessWorld is a router over a store where dev (group devs) may read and
// write libs-local under com/** only, ann is an administrator through the
// admin-privilege group admins, and tokens can be issued.
func accessWorld(t *testing.T) (http.Handler, *token.Service) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	for _, g := range []types.Group{{Name: "devs"}, {Name: "admins", AdminPrivileges: true}} {
		if err := db.Groups().Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []types.User{
		{Name: "dev", PasswordHash: cheapHash, Groups: []string{"devs"}},
		{Name: "ann", PasswordHash: cheapHash, Groups: []string{"admins"}},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "dev-com", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{
					"devs": types.ActionRead | types.ActionWrite | types.ActionAnnotate,
				}},
				Targets: map[string]types.Target{"libs-local": {Include: []string{"com/**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	for key, files := range map[string][]string{
		"libs-local": {"com/x.txt", "org/y.txt"},
		"ext-local":  {"z.txt"},
	} {
		c := repo.Config{Key: key, Class: repo.Local}
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		for _, p := range files {
			if _, err := items.Deploy(ctx, item.Deploy{
				Target: item.Target{Repo: c, Path: p}, Body: strings.NewReader(p),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens: tokens,
	}), tokens
}

func TestPathLevelPermissions(t *testing.T) {
	h, _ := accessWorld(t)
	dev := basic("dev", "pw")
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/artifactory/libs-local/com/x.txt", http.StatusOK},
		{http.MethodGet, "/artifactory/libs-local/org/y.txt", http.StatusForbidden},
		{http.MethodGet, "/artifactory/libs-local/org/y.txt.sha256", http.StatusForbidden},
		{http.MethodGet, "/artifactory/libs-local/com/x.txt.sha256", http.StatusOK},
		{http.MethodGet, "/artifactory/api/storage/libs-local/com/x.txt", http.StatusOK},
		{http.MethodGet, "/artifactory/api/storage/libs-local/org/y.txt", http.StatusForbidden},
		{http.MethodGet, "/artifactory/ext-local/z.txt", http.StatusForbidden},
		{http.MethodDelete, "/artifactory/libs-local/com/x.txt", http.StatusForbidden},
		// Copy reads the source path and writes where it lands; a move also
		// deletes the source, which devs may not.
		{http.MethodPost, "/artifactory/api/copy/libs-local/com/x.txt?to=/libs-local/com/x2.txt", http.StatusOK},
		{http.MethodPost, "/artifactory/api/copy/libs-local/org/y.txt?to=/libs-local/com/y.txt", http.StatusForbidden},
		{http.MethodPost, "/artifactory/api/copy/libs-local/com/x.txt?to=/libs-local/org/x.txt", http.StatusForbidden},
		{http.MethodPost, "/artifactory/api/move/libs-local/com/x.txt?to=/libs-local/com/m.txt", http.StatusForbidden},
		// Properties need annotate at the path.
		{http.MethodPut, "/artifactory/api/storage/libs-local/com/x.txt?properties=a=1", http.StatusNoContent},
		{http.MethodPut, "/artifactory/api/storage/libs-local/org/y.txt?properties=a=1", http.StatusForbidden},
		// The permissions view is for administrators.
		{http.MethodGet, "/artifactory/api/storage/libs-local/com/x.txt?permissions", http.StatusForbidden},
	} {
		if rec := do(t, h, c.method, c.path, dev, ""); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body)
		}
	}
	// Deploys follow the include pattern too.
	if rec := do(
		t,
		h,
		http.MethodPut,
		"/artifactory/libs-local/com/new.txt",
		dev,
		"new",
	); rec.Code != http.StatusCreated {
		t.Errorf("deploy under com/: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		"/artifactory/libs-local/org/new.txt",
		dev,
		"new",
	); rec.Code != http.StatusForbidden {
		t.Errorf("deploy under org/: %d %s", rec.Code, rec.Body)
	}
	// Listings show the way down to what dev may read, and nothing else.
	rec := do(t, h, http.MethodGet, "/artifactory/libs-local/", dev, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "com/") ||
		strings.Contains(rec.Body.String(), "org/") {
		t.Errorf("folder listing: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/artifactory/api/storage/libs-local?list&deep=1", dev, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/com/x.txt") ||
		strings.Contains(rec.Body.String(), "/org/y.txt") {
		t.Errorf("storage list: %d %s", rec.Code, rec.Body)
	}
	// AQL rows are filtered by path.
	rec = do(t, h, http.MethodPost, "/artifactory/api/search/aql", dev,
		`items.find({"repo":"libs-local"}).include("repo","path","name")`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"x.txt"`) ||
		strings.Contains(rec.Body.String(), `"y.txt"`) {
		t.Errorf("aql: %d %s", rec.Code, rec.Body)
	}
	// A repository listing shows what dev may read in.
	rec = do(t, h, http.MethodGet, "/artifactory/api/repositories", dev, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "libs-local") ||
		strings.Contains(rec.Body.String(), "ext-local") {
		t.Errorf("repository list: %d %s", rec.Code, rec.Body)
	}
}

func TestScopedTokenIsolation(t *testing.T) {
	ctx := context.Background()
	h, tokens := accessWorld(t)
	sc, err := types.ParseScope("artifact:libs-local:r")
	if err != nil {
		t.Fatal(err)
	}
	// An administrator's token scoped to one repository reads anything in
	// it, nothing elsewhere, and no admin-only endpoint.
	issued, err := tokens.Issue(ctx, token.Request{Username: "admin", Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + issued.AccessToken}
	for _, c := range []struct {
		path string
		want int
	}{
		{"/artifactory/libs-local/org/y.txt", http.StatusOK},
		{"/artifactory/ext-local/z.txt", http.StatusForbidden},
		{"/artifactory/api/storageinfo", http.StatusForbidden},
	} {
		if rec := do(t, h, http.MethodGet, c.path, bearer, ""); rec.Code != c.want {
			t.Errorf("GET %s = %d, want %d: %s", c.path, rec.Code, c.want, rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/repositories", bearer, "")
	var list []struct{ Key string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil ||
		len(list) != 1 || list[0].Key != "libs-local" {
		t.Errorf("a scoped token lists only its repository: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminThroughGroup(t *testing.T) {
	h, _ := accessWorld(t)
	rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/ann-local",
		map[string]string{
			"Authorization": basic("ann", "pw")["Authorization"],
			"Content-Type":  "application/json",
		}, `{"rclass":"local","packageType":"generic"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("an admin-privilege group member creates a repository: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/ext-local/z.txt",
		basic("ann", "pw"),
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("and reads everything: %d %s", rec.Code, rec.Body)
	}
	// The token API's admin decisions come from the evaluator too: a group
	// administrator may mint a token for another user; dev may not.
	jsonAs := func(user string) map[string]string {
		return map[string]string{
			"Authorization": basic(user, "pw")["Authorization"],
			"Content-Type":  "application/json",
		}
	}
	rec = do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs("ann"), `{"username":"dev"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a group administrator mints a token for dev: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs("dev"), `{"username":"ann"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dev mints a token for ann: %d %s", rec.Code, rec.Body)
	}
}

func TestPermissionsViewEvaluatesPatterns(t *testing.T) {
	h, _ := accessWorld(t)
	admin := basic("admin", "pw")
	rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/storage/libs-local/com/x.txt?permissions",
		admin,
		"",
	)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"devs"`) {
		t.Errorf("devs hold com/x.txt: %d %s", rec.Code, rec.Body)
	}
	rec = do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/storage/libs-local/org/y.txt?permissions",
		admin,
		"",
	)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"devs"`) {
		t.Errorf("devs' com/** grant does not reach org/y.txt: %d %s", rec.Code, rec.Body)
	}
}
