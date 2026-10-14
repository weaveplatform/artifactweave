package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/internal/service/roles"
)

func TestGlobalRolesAndEnvironmentsBodies(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	jsonAdmin := map[string]string{
		"Authorization": admin["Authorization"],
		"Content-Type":  "application/json",
	}

	rec := do(t, h, http.MethodGet, "/access/api/v1/environments", admin, "")
	var envs []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envs); err != nil || len(envs) != 2 ||
		envs[0]["name"] != "DEV" || envs[1]["name"] != "PROD" {
		t.Fatalf("built-in environments: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/access/api/v1/environments", jsonAdmin, `{"name":"QA"}`)
	if rec.Code != http.StatusCreated || decodeObject(t, rec.Body.String())["name"] != "QA" {
		t.Fatalf("create environment: %d %s", rec.Code, rec.Body)
	}

	// Environments are canonicalised; actions keep their order without
	// duplicates.
	rec = do(t, h, http.MethodPost, "/access/api/v1/roles", jsonAdmin,
		`{"name":"ci","type":"CUSTOM_GLOBAL","description":"CI","environments":["dev","qa","DEV"],`+
			`"actions":["READ_BUILD","READ_REPOSITORY","READ_BUILD"]}`)
	if rec.Code != http.StatusCreated || rec.Body.Len() != 0 {
		t.Fatalf("create role answers 201 with no body: %d %q", rec.Code, rec.Body)
	}
	var role struct {
		Name, Type, Description string
		Actions, Environments   []string
	}
	rec = do(t, h, http.MethodGet, "/access/api/v1/roles/ci", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &role); err != nil ||
		role.Type != "CUSTOM_GLOBAL" || role.Description != "CI" ||
		!slices.Equal(role.Environments, []string{"DEV", "QA"}) ||
		!slices.Equal(role.Actions, []string{"READ_BUILD", "READ_REPOSITORY"}) {
		t.Fatalf("role %d %s", rec.Code, rec.Body)
	}

	// Renaming QA renames it in the role too.
	rec = do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/environments/QA/rename",
		jsonAdmin,
		`{"new_name":"STAGE"}`,
	)
	if rec.Code != http.StatusOK || decodeObject(t, rec.Body.String())["name"] != "STAGE" {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/access/api/v1/roles/ci", admin, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &role)
	if !slices.Equal(role.Environments, []string{"DEV", "STAGE"}) {
		t.Fatalf("the rename did not reach the role: %s", rec.Body)
	}

	// A project's repository in STAGE keeps it from being deleted.
	rec = do(t, h, http.MethodPut, "/artifactory/api/repositories/staged", jsonAdmin,
		`{"rclass":"local","packageType":"generic","projectKey":"proj","environments":["STAGE"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create repository: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodDelete, "/access/api/v1/environments/STAGE", admin, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete an environment in use: %d %s", rec.Code, rec.Body)
	}

	// The list holds the built-ins first, ADMIN then PREDEFINED, then ci.
	var all []struct{ Name, Type string }
	rec = do(t, h, http.MethodGet, "/access/api/v1/roles", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil || len(all) != 7 ||
		all[0].Name != "Project Admin" || all[0].Type != "ADMIN" ||
		all[1].Type != "PREDEFINED" || all[6].Name != "ci" {
		t.Fatalf("roles %d %s", rec.Code, rec.Body)
	}
}

func TestGlobalRolesWithoutStore(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	if _, err := (accessService{}).GetAllGlobalRoles(
		ctx,
		accessgen.GetAllGlobalRolesRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Fatalf("a router without a store answers 501: %v", err)
	}
	if _, err := (accessService{}).GetAllGlobalRoles(
		context.Background(),
		accessgen.GetAllGlobalRolesRequestObject{},
	); !errors.Is(
		err,
		auth.ErrUnauthenticated,
	) {
		t.Fatalf("no principal: %v", err)
	}
}

func TestRoleErrorStatuses(t *testing.T) {
	s, ctx := accessService{}, context.Background()
	for err, want := range map[error]int{
		fmt.Errorf("x: %w", roles.ErrNotFound): http.StatusNotFound,
		fmt.Errorf("x: %w", roles.ErrConflict): http.StatusTeapot,
		fmt.Errorf("x: %w", roles.ErrInvalid):  http.StatusBadRequest,
		fmt.Errorf("x: %w", roles.ErrBuiltin):  http.StatusBadRequest,
		fmt.Errorf("x: %w", roles.ErrInUse):    http.StatusBadRequest,
		errors.New("disk on fire"):             http.StatusInternalServerError,
	} {
		var e *apierror.Error
		if !errors.As(s.roleError(ctx, "op", err, http.StatusTeapot), &e) || e.Status != want {
			t.Errorf("%v: %+v, want %d", err, e, want)
		}
	}
}
