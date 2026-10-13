package server

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The status-code table the definition of done requires: every implemented
// route with each status a client can get from it. A feature adds its rows
// in its own function listed in statusTable, and
// TestStatusTableCoversImplemented fails while a route in the implemented
// map has no row.

// statusCase is one request and the status it must get.
type statusCase struct {
	// route is the operation as the implemented map names it.
	route string
	// path is the request path, query included.
	path string
	// as is who calls: "" without credentials (anonymous access off),
	// "anonymous" without credentials (anonymous access on), "admin", or
	// "user" (a user in no group, so the seeded permissions grant nothing).
	as   string
	body string
	want int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

func statusTable() []statusCase {
	return slices.Concat(systemStatusRows(), repositoryStatusRows(), descriptorStatusRows())
}

func systemStatusRows() []statusCase {
	return []statusCase{
		{"GET /artifactory/api/system/version", "/artifactory/api/system/version", "", "", 200},
		{
			"GET /artifactory/api/system/license",
			"/artifactory/api/system/license",
			"admin",
			"",
			200,
		},
		{"GET /artifactory/api/system/license", "/artifactory/api/system/license", "", "", 401},
		{
			"GET /artifactory/api/v1/system/readiness",
			"/artifactory/api/v1/system/readiness",
			"",
			"",
			200,
		},
		{
			"GET /artifactory/api/v1/system/liveness",
			"/artifactory/api/v1/system/liveness",
			"",
			"",
			200,
		},
		{"GET /access/api/v1/system/version", "/access/api/v1/system/version", "", "", 200},
	}
}

// statusFixture is a seeded store with the admin (password "pw"), a user
// "user" (password "pw") in no group, and the repositories the rows use.
// Each row that changes state uses keys no other row touches, so the rows
// run in any order against one store.
func statusFixture(t *testing.T, anonymous bool) http.Handler {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	if _, err := auth.Seed(ctx, db, "pw", time.Now()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, types.User{Name: "user", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	shared := true
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "remote-shared", Class: repo.Remote, URL: "https://up.example", ShareConfiguration: &shared},
		{Key: "remote-private", Class: repo.Remote, URL: "https://up.example"},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"}},
		{Key: "del-one", Class: repo.Local},
		{Key: "del-batch", Class: repo.Local},
		{Key: "upd-batch", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{CompatVersion: "7.155.0", Store: db, Auth: a})
}

func repositoryStatusRows() []statusCase {
	const (
		list    = "GET /artifactory/api/repositories"
		configs = "GET /artifactory/api/repositories/configurations"
		exists  = "GET /artifactory/api/repositories/existence"
		shared  = "GET /artifactory/api/repositories/{remoteRepoName}/configuration"
		get     = "GET /artifactory/api/repositories/{repoKey}"
		create  = "PUT /artifactory/api/repositories/{repoKey}"
		update  = "POST /artifactory/api/repositories/{repoKey}"
		remove  = "DELETE /artifactory/api/repositories/{repoKey}"
		getV2   = "GET /artifactory/api/v2/repositories/{repoKey}"
		bGet    = "GET /artifactory/api/v2/repositories/batch"
		bCreate = "PUT /artifactory/api/v2/repositories/batch"
		bUpdate = "POST /artifactory/api/v2/repositories/batch"
		bDelete = "DELETE /artifactory/api/v2/repositories/batch"
		r       = "/artifactory/api/repositories"
		v2      = "/artifactory/api/v2/repositories"
	)
	return []statusCase{
		{list, r, "admin", "", 200},
		{list, r + "/?type=local", "admin", "", 200},
		{list, r, "user", "", 200},
		{list, r, "anonymous", "", 200},
		{list, r, "", "", 401},

		{configs, r + "/configurations", "admin", "", 200},
		{configs, r + "/configurations", "user", "", 403},
		{configs, r + "/configurations", "", "", 401},

		{exists, r + "/existence?type=local", "admin", "", 200},
		{exists, r + "/existence", "user", "", 403},
		{exists, r + "/existence", "", "", 401},

		{shared, r + "/remote-private/configuration", "admin", "", 200},
		{shared, r + "/remote-shared/configuration", "user", "", 200},
		{shared, r + "/remote-private/configuration", "user", "", 403},
		{shared, r + "/libs-local/configuration", "admin", "", 404},
		{shared, r + "/remote-shared/configuration", "", "", 401},

		{get, r + "/libs-local", "admin", "", 200},
		{get, r + "/remote-private", "anonymous", "", 200},
		{get, r + "/libs-local", "anonymous", "", 401},
		{get, r + "/libs-local", "user", "", 403},
		{get, r + "/missing", "admin", "", 404},
		{get, r + "/libs-local", "", "", 401},

		{create, r + "/new-local", "admin", `{"rclass":"local"}`, 200},
		{create, r + "/LIBS-LOCAL", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/no-url", "admin", `{"rclass":"remote"}`, 400},
		{create, r + "/new-virt", "admin", `{"rclass":"virtual","repositories":["ghost"]}`, 400},
		{create, r + "/user-local", "user", `{"rclass":"local"}`, 403},
		{create, r + "/anon-local", "", `{"rclass":"local"}`, 401},

		{update, r + "/libs-local", "admin", `{"description":"updated"}`, 200},
		{update, r + "/libs-local", "admin", `{"rclass":"remote"}`, 400},
		{update, r + "/missing", "admin", `{"description":"x"}`, 404},
		{update, r + "/libs-local", "user", `{"description":"x"}`, 403},
		{update, r + "/libs-local", "", `{"description":"x"}`, 401},

		{remove, r + "/del-one", "admin", "", 200},
		{remove, r + "/missing", "admin", "", 404},
		{remove, r + "/auto-trashcan", "admin", "", 400},
		{remove, r + "/libs-local", "user", "", 403},
		{remove, r + "/libs-local", "", "", 401},

		{getV2, v2 + "/libs-local", "admin", "", 200},
		{getV2, v2 + "/missing", "admin", "", 404},
		{getV2, v2 + "/libs-local", "user", "", 403},
		{getV2, v2 + "/libs-local", "", "", 401},

		{bGet, v2 + "/batch?names=libs-local,missing", "admin", "", 200},
		{bGet, v2 + "/batch?names=libs-local", "user", "", 200},
		{bGet, v2 + "/batch?names=,", "admin", "", 400},
		{bGet, v2 + "/batch", "admin", "", 400},
		{bGet, v2 + "/batch?names=libs-local", "", "", 401},

		{
			bCreate,
			v2 + "/batch",
			"admin",
			`[{"key":"b1","rclass":"local"},{"key":"b2","rclass":"local"}]`,
			201,
		},
		{bCreate, v2 + "/batch", "admin", `[{"key":"b3","rclass":"remote"}]`, 400},
		{bCreate, v2 + "/batch", "admin", `[{"key":"libs-local","rclass":"local"}]`, 409},
		{bCreate, v2 + "/batch", "admin", `[]`, 400},
		{bCreate, v2 + "/batch", "user", `[{"key":"b4","rclass":"local"}]`, 403},
		{bCreate, v2 + "/batch", "", `[{"key":"b5","rclass":"local"}]`, 401},

		{bUpdate, v2 + "/batch", "admin", `[{"key":"upd-batch","description":"x"}]`, 200},
		{bUpdate, v2 + "/batch", "admin", `[{"description":"no key"}]`, 400},
		{bUpdate, v2 + "/batch", "admin", `[{"key":"missing"}]`, 404},
		{bUpdate, v2 + "/batch", "user", `[{"key":"upd-batch"}]`, 403},
		{bUpdate, v2 + "/batch", "", `[{"key":"upd-batch"}]`, 401},

		{bDelete, v2 + "/batch", "admin", `["del-batch","missing"]`, 200},
		{bDelete, v2 + "/batch", "admin", `[]`, 400},
		{bDelete, v2 + "/batch", "admin", `["libs-local","auto-trashcan"]`, 400},
		{bDelete, v2 + "/batch", "user", `["libs-local"]`, 403},
		{bDelete, v2 + "/batch", "", `["libs-local"]`, 401},
	}
}

func TestStatusTable(t *testing.T) {
	off, on := statusFixture(t, false), statusFixture(t, true)
	for _, c := range statusTable() {
		h, headers := off, map[string]string(nil)
		switch c.as {
		case "anonymous":
			h = on
		case "admin", "user":
			headers = basic(c.as, "pw")
		}
		if c.body != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers["Content-Type"] = "application/json"
		}
		rec := do(t, h, c.method(), c.path, headers, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}

// implementedHandwritten lists the hand-written routes with real handlers;
// the guard below holds them to the same rule as the generated ones.
var implementedHandwritten = map[string]bool{
	"PATCH /artifactory/api/system/configuration": true,
}

func descriptorStatusRows() []statusCase {
	const patch = "PATCH /artifactory/api/system/configuration"
	const path = "/artifactory/api/system/configuration"
	return []statusCase{
		{patch, path, "admin", "localRepositories:\n  patch-local:\n    type: generic\n", 200},
		{patch, path, "admin", "localRepositories:\n  patch-bad:\n    nonsense: 1\n", 400},
		{patch, path, "", "localRepositories: {}", 401},
		{patch, path, "anonymous", "localRepositories: {}", 401},
		{patch, path, "user", "localRepositories: {}", 403},
	}
}

func TestStatusTableCoversImplemented(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range statusTable() {
		covered[c.route] = true
	}
	for route := range maps.Keys(implementedHandwritten) {
		if !covered[route] {
			t.Errorf("%s is implemented but has no row in the status-code table", route)
		}
	}
	for route := range implemented {
		if !covered[route] {
			t.Errorf("%s is implemented but has no row in the status-code table", route)
		}
	}
}
