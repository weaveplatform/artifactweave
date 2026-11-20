package access_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccProjectAdministratorRepositoryCreateAndUpdate(t *testing.T) {
	var db *inmem.Store
	h := tokenFixture(t, func(c *Config) {
		var ok bool
		db, ok = c.Store.(*inmem.Store)
		if !ok {
			t.Fatal("fixture store")
		}
	})
	ctx := t.Context()
	if err := db.Users().Create(ctx, accesstest.User{Name: "owner", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	projectRequest(
		t,
		h,
		http.MethodPost,
		"",
		"admin",
		`{"project_key":"alpha","display_name":"Alpha","admin_privileges":{"manage_resources":true,"manage_remote_repository":false,"create_local_repository":false,"create_remote_repository":true,"create_virtual_repository":true,"delete_local_repository":true,"delete_remote_repository":false,"delete_virtual_repository":true}}`,
		http.StatusCreated,
	)
	projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"beta","display_name":"Beta"}`, http.StatusCreated)
	projectRequest(t, h, http.MethodPut, "/alpha/users/owner", "admin", `{"roles":["Project Admin"]}`, http.StatusOK)
	const base = "/artifactory/api/repositories/"
	call := func(method, path, actor, body string, want int) {
		t.Helper()
		wantStatus(t, do(t, h, method, path, jsonAs(basic(actor, "pw")), body), want)
	}
	for _, key := range []string{"alpha-local", "beta-local"} {
		project := "alpha"
		if key == "beta-local" {
			project = "beta"
		}
		call(http.MethodPut, base+key, "admin", `{"rclass":"local","packageType":"generic","projectKey":"`+project+`"}`, http.StatusOK)
	}
	// Explicit false overrides the old true aggregate flag for creation.
	call(http.MethodPut, base+"denied-local", "owner", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, http.StatusForbidden)
	if _, err := db.Repos().Get(ctx, "denied-local"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("denied local creation published", err)
	}
	call(http.MethodPut, base+"alpha-remote", "owner", `{"rclass":"remote","packageType":"generic","projectKey":"alpha","url":"https://up.example"}`, http.StatusOK)
	call(http.MethodPut, base+"alpha-virtual", "owner", `{"rclass":"virtual","packageType":"generic","projectKey":"alpha"}`, http.StatusOK)
	call(http.MethodPut, base+"foreign", "owner", `{"rclass":"virtual","packageType":"generic","projectKey":"beta"}`, http.StatusForbidden)
	call(http.MethodPut, base+"unowned", "owner", `{"rclass":"virtual","packageType":"generic"}`, http.StatusForbidden)
	call(http.MethodPost, base+"alpha-local", "owner", `{"description":"owned update"}`, http.StatusOK)
	// Remote settings use their own current management flag, independently of
	// the create flag that allowed this repository to be created.
	call(http.MethodPost, base+"alpha-remote", "owner", `{"description":"refused"}`, http.StatusForbidden)
	call(http.MethodPost, base+"beta-local", "owner", `{"description":"refused"}`, http.StatusForbidden)
	call(http.MethodPost, base+"alpha-local", "owner", `{"projectKey":"beta"}`, http.StatusForbidden)
	call(
		http.MethodPut,
		"/artifactory/api/v2/repositories/batch",
		"owner",
		`[{"key":"batch-owned","rclass":"virtual","packageType":"generic","projectKey":"alpha"},{"key":"batch-foreign","rclass":"virtual","packageType":"generic","projectKey":"beta"}]`,
		http.StatusForbidden,
	)
	if _, err := db.Repos().Get(ctx, "batch-owned"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("denied batch creation partially published", err)
	}
	call(
		http.MethodPost,
		"/artifactory/api/v2/repositories/batch",
		"owner",
		`[{"key":"alpha-local","description":"must roll back"},{"key":"beta-local","description":"refused"}]`,
		http.StatusForbidden,
	)
	stored, err := db.Repos().Get(ctx, "alpha-local")
	if err != nil || stored.Description != "owned update" || stored.ProjectKey != "alpha" {
		t.Fatal("denied batch update or ownership move changed a repository", err)
	}
	call(http.MethodDelete, base+"alpha-remote", "owner", "", http.StatusForbidden)
	call(http.MethodDelete, base+"beta-local", "owner", "", http.StatusForbidden)
	// A forbidden key late in the batch must leave earlier allowed keys intact.
	call(http.MethodDelete, "/artifactory/api/v2/repositories/batch", "owner", `["alpha-local","beta-local"]`, http.StatusForbidden)
	if _, err := db.Repos().Get(ctx, "alpha-local"); err != nil {
		t.Fatal("denied batch deletion removed an earlier repository", err)
	}
	call(http.MethodDelete, base+"alpha-local", "owner", "", http.StatusOK)
	call(http.MethodDelete, base+"alpha-virtual", "owner", "", http.StatusOK)
	for _, key := range []string{"alpha-local", "alpha-virtual"} {
		if _, err := db.Repos().Get(ctx, key); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("allowed deletion of %s did not remove it: %v", key, err)
		}
	}
	projectRequest(t, h, http.MethodDelete, "/alpha/users/owner", "admin", "", http.StatusNoContent)
	call(http.MethodDelete, base+"alpha-remote", "owner", "", http.StatusForbidden)
	call(http.MethodPut, base+"after-revocation", "owner", `{"rclass":"virtual","packageType":"generic","projectKey":"alpha"}`, http.StatusForbidden)
}
