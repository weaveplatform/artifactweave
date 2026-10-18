package server

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
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
	// "anonymous" without credentials (anonymous access on), "admin",
	// "user" (a user in no group, so the seeded permissions grant nothing),
	// "expired" (an expired, refreshable access token of admin's, as a
	// Bearer token) or "forged" (that token with its signature altered).
	// A body names the fixture's other token values as {name}
	// (statusSecrets).
	as   string
	body string
	want int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

func statusTable() []statusCase {
	return slices.Concat(
		systemStatusRows(),
		repositoryStatusRows(),
		descriptorStatusRows(),
		itemStatusRows(),
		storageStatusRows(),
		transferStatusRows(),
		trashStatusRows(),
		mirrorStatusRows(),
		multipartStatusRows(),
		roleStatusRows(),
		keypairStatusRows(),
		securityStatusRows(),
		tokenStatusRows(),
		permissionStatusRows(),
		accessIdentityStatusRows(),
		oidcStatusRows(),
		buildStatusRows(),
		searchStatusRows(),
		registryStatusRows(),
		registryBlobStatusRows(),
		registryManifestStatusRows(),
		registryReferrersStatusRows(),
		registryListingStatusRows(),
		partStatusRows(),
	)
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
		{"GET /artifactory/api/system/license", "/artifactory/api/system/license", "user", "", 403},
		{
			"GET /artifactory/api/system/license",
			"/artifactory/api/system/license",
			"anonymous",
			"",
			401,
		},
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
func statusFixture(t *testing.T, anonymous bool, tune ...func(*Config)) http.Handler {
	t.Helper()
	h, _ := newStatusFixture(t, anonymous, tune...)
	return h
}

// newStatusFixture is statusFixture with the token secrets the rows refer
// to, which exist only once the fixture's signing key does.
func newStatusFixture(
	t *testing.T,
	anonymous bool,
	tune ...func(*Config),
) (http.Handler, statusSecrets) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(ctx, types.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	shared := true
	for _, c := range append([]repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "remote-shared", Class: repo.Remote, URL: "https://up.example", ShareConfiguration: &shared},
		{Key: "remote-private", Class: repo.Remote, URL: "https://up.example"},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"}},
		{Key: "del-one", Class: repo.Local},
		{Key: "del-batch", Class: repo.Local},
		{Key: "upd-batch", Class: repo.Local},
	}, formatRepos()...) {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	seedKeys(t, db)
	seedPermissions(t, db)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	seedStatusBuilds(t, db, blobs)
	seedStatusSearches(t, db, blobs)
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fixture/get.txt", "fixture/del.txt", "fixture/move.txt"} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader("fixture"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: libs, Path: "fixture/archive.zip"},
		Body:   bytes.NewReader(zipBytes(t, map[string]string{"inner.txt": "inner"})),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Put(ctx, types.SettingFolderDownload,
		types.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tokens, secrets := seedTokens(t, db)
	seedRegistryStatus(t, db, blobs, secrets)
	seedRegistryManifests(t, db, blobs)
	seedRegistryDeletes(t, db, blobs)
	for _, p := range statusParts {
		if p.seed != nil {
			p.seed(t, db, blobs)
		}
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens:    tokens,
		Multipart: &multipart.Service{Store: db, Blobs: blobs, Dir: t.TempDir()},
	}
	for _, p := range statusParts {
		if p.tune != nil {
			p.tune(&cfg)
		}
	}
	for _, f := range tune {
		f(&cfg)
	}
	return New(cfg), secrets
}

// multipartStatusRows covers the JFrog CLI multipart upload routes. Only
// create authenticates as other routes do; the rest answer 401 without a
// valid upload token or part signature, whatever other credentials are sent.
func multipartStatusRows() []statusCase {
	const (
		create   = "POST " + uploadsCreate
		urlPart  = "POST " + uploadsURLPart
		part     = "PUT " + uploadsPart
		complete = "POST " + uploadsComplete
		status   = "POST " + uploadsStatus
		abort    = "POST " + uploadsAbort
		c        = uploadsCreate + "?repoKey=libs-local&repoPath=big/file.bin&partSizeMB=1"
	)
	return []statusCase{
		{create, c, "admin", "", 200},
		{create, uploadsCreate + "?repoKey=libs-local&partSizeMB=1", "admin", "", 400},
		{create, uploadsCreate + "?repoKey=libs-local&repoPath=f&partSizeMB=0", "admin", "", 400},
		{
			create,
			uploadsCreate + "?repoKey=libs-local&repoPath=a/../b&partSizeMB=1",
			"admin",
			"",
			400,
		},
		{create, uploadsCreate + "?repoKey=missing&repoPath=f&partSizeMB=1", "admin", "", 404},
		{
			create,
			uploadsCreate + "?repoKey=remote-private&repoPath=f&partSizeMB=1",
			"admin",
			"",
			405,
		},
		{create, c, "user", "", 403},
		{create, c, "anonymous", "", 401},
		{create, c, "", "", 401},
		{urlPart, uploadsURLPart + "?partNumber=1", "admin", "", 401},
		{urlPart, uploadsURLPart + "?partNumber=x", "", "", 400},
		{part, uploadsPart + "?upload=x&partNumber=1&signature=y", "", "part", 401},
		{part, uploadsPart + "?upload=x&partNumber=y&signature=y", "", "part", 401},
		{complete, uploadsComplete + "?sha1=abc", "", "", 401},
		{status, uploadsStatus, "", "", 401},
		{abort, uploadsAbort, "", "", 401},
	}
}

func mirrorStatusRows() []statusCase {
	const optimize = "POST /artifactory/api/system/storage/optimize"
	const path = "/artifactory/api/system/storage/optimize"
	return []statusCase{
		{optimize, path, "admin", "", 200},
		{optimize, path, "user", "", 403},
		{optimize, path, "anonymous", "", 401},
		{optimize, path, "", "", 401},
	}
}

func itemStatusRows() []statusCase {
	const (
		put    = "PUT /artifactory/{repoKey}/{filePath}"
		get    = "GET /artifactory/{repoKey}/{filePath}"
		head   = "HEAD /artifactory/{repoKey}/{filePath}"
		remove = "DELETE /artifactory/{repoKey}/{filePath}"
		aqlOp  = "POST /artifactory/api/search/aql"
		upl    = "GET /artifactory/api/v1/uploads/config"
		lib    = "/artifactory/libs-local"
		q      = `items.find({"repo":"libs-local"}).include("repo","path","name")`
	)
	return []statusCase{
		{put, lib + "/new/a.txt", "admin", "a", 201},
		{put, lib + "/new/dir/", "admin", "", 201},
		{put, lib + "/", "admin", "a", 400},
		{put, lib + "/fixture/get.txt/below", "admin", "a", 409},
		{put, "/artifactory/remote-private/a.txt", "admin", "a", 405},
		{put, "/artifactory/missing/a.txt", "admin", "a", 404},
		{put, lib + "/new/b.txt", "user", "a", 403},
		{put, lib + "/new/b.txt", "anonymous", "a", 401},
		{put, lib + "/new/b.txt", "", "a", 401},

		{get, lib + "/fixture/get.txt", "admin", "", 200},
		{get, lib + "/fixture/get.txt.sha256", "admin", "", 200},
		{get, lib + "/fixture/", "admin", "", 200},
		{get, lib + "/fixture/absent", "admin", "", 404},
		{get, "/artifactory/missing/a.txt", "admin", "", 404},
		{get, lib + "/fixture/get.txt", "user", "", 403},
		{get, lib + "/fixture/get.txt", "anonymous", "", 401},
		{get, lib + "/fixture/get.txt", "", "", 401},

		{head, lib + "/fixture/get.txt", "admin", "", 200},
		{head, lib + "/fixture/absent", "admin", "", 404},
		{head, lib + "/fixture/get.txt", "", "", 401},

		{remove, lib + "/fixture/del.txt", "admin", "", 204},
		{remove, lib + "/fixture/absent", "admin", "", 404},
		{remove, "/artifactory/virt/x", "admin", "", 405},
		{remove, lib + "/fixture/get.txt", "user", "", 403},
		{remove, lib + "/fixture/get.txt", "anonymous", "", 401},
		{remove, lib + "/fixture/get.txt", "", "", 401},

		{aqlOp, "/artifactory/api/search/aql", "admin", q, 200},
		{aqlOp, "/artifactory/api/search/aql", "user", q, 200},
		{aqlOp, "/artifactory/api/search/aql", "admin", "items.find(", 400},
		{aqlOp, "/artifactory/api/search/aql", "anonymous", q, 403},
		{aqlOp, "/artifactory/api/search/aql", "admin", "", 400},
		{aqlOp, "/artifactory/api/search/aql", "", q, 401},

		{upl, "/artifactory/api/v1/uploads/config", "admin", "", 200},
		{upl, "/artifactory/api/v1/uploads/config", "", "", 401},
	}
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
		{create, r + "/env-local", "admin", `{"rclass":"local","environments":["nope"]}`, 200},
		{
			create,
			r + "/env-proj",
			"admin",
			`{"rclass":"local","projectKey":"proj","environments":["dev"]}`,
			200,
		},
		{
			create,
			r + "/bad-env",
			"admin",
			`{"rclass":"local","projectKey":"proj","environments":["nope"]}`,
			400,
		},
		{create, r + "/user-local", "user", `{"rclass":"local"}`, 403},
		{create, r + "/anon-local", "", `{"rclass":"local"}`, 401},

		{update, r + "/libs-local", "admin", `{"description":"updated"}`, 200},
		{update, r + "/libs-local", "admin", `{"projectKey":"proj","environments":["nope"]}`, 400},
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
	off, secrets := newStatusFixture(t, false)
	on := statusFixture(t, true)
	var pairs []string
	for name, v := range secrets {
		pairs = append(pairs, "{"+name+"}", v)
	}
	fill := strings.NewReplacer(pairs...)
	for _, c := range statusTable() {
		h, headers := off, map[string]string(nil)
		switch c.as {
		case "anonymous":
			h = on
		case "admin", "user":
			headers = basic(c.as, "pw")
		case "expired":
			headers = bearerAuth(secrets["expired-access"])
		case "forged":
			headers = bearerAuth(secrets["forged-access"])
		}
		body := fill.Replace(c.body)
		if body != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			// A form: prefix sends the rest form-encoded, for the legacy
			// token routes, and manifest: as an OCI image manifest.
			headers["Content-Type"], body = contentTypeOf(body)
		}
		rec := do(t, h, c.method(), fill.Replace(c.path), headers, body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}

// implementedHandwritten lists the hand-written routes with real handlers,
// one map per feature, and each format's (statusParts); the guards below
// hold them to the same rule as the generated ones.
var implementedHandwritten = routeUnion(
	append([]map[string]bool{
		contentRoutes, searchRoutes, uploadRoutes, legacyTokenRoutes, descriptorRoutes,
		registryRoutes,
	}, partRoutes(func(p statusPart) map[string]bool { return p.handwritten })...)...,
)

// contentRoutes are the item, storage, transfer and archive routes.
var contentRoutes = map[string]bool{
	"PUT /artifactory/{repoKey}/{filePath}":                  true,
	"GET /artifactory/{repoKey}/{filePath}":                  true,
	"HEAD /artifactory/{repoKey}/{filePath}":                 true,
	"DELETE /artifactory/{repoKey}/{filePath}":               true,
	"GET /artifactory/api/storage/{repoKey}/{itemPath}":      true,
	"PUT /artifactory/api/storage/{repoKey}/{itemPath}":      true,
	"DELETE /artifactory/api/storage/{repoKey}/{itemPath}":   true,
	"PATCH /artifactory/api/metadata/{repoKey}/{itemPath}":   true,
	"POST /artifactory/api/copy/{srcRepoKey}/{srcFilePath}":  true,
	"POST /artifactory/api/move/{srcRepoKey}/{srcFilePath}":  true,
	"GET /artifactory/api/archive/download/{repoKey}/{path}": true,
	"GET /artifactory/{repoKey}/{archivePath}!/{entryPath}":  true,
	"POST /artifactory/api/zap/{repoPath}":                   true,
	"GET /artifactory/api/storage/auto-trashcan":             true,
}

// searchRoutes are the hand-written searches.
var searchRoutes = map[string]bool{
	"POST /artifactory/api/search/aql":            true,
	"GET /artifactory/api/versions/{repo}/{path}": true,
}

// uploadRoutes are the JFrog CLI's multipart upload routes.
var uploadRoutes = map[string]bool{
	"POST " + uploadsCreate:   true,
	"POST " + uploadsURLPart:  true,
	"PUT " + uploadsPart:      true,
	"POST " + uploadsComplete: true,
	"POST " + uploadsStatus:   true,
	"POST " + uploadsAbort:    true,
}

// legacyTokenRoutes are the Artifactory token API's hand-written routes.
var legacyTokenRoutes = map[string]bool{
	"POST /artifactory/api/security/token":        true,
	"POST /artifactory/api/security/token/revoke": true,
}

// descriptorRoutes is the YAML configuration patch.
var descriptorRoutes = map[string]bool{"PATCH /artifactory/api/system/configuration": true}

// routeUnion is every route in maps.
func routeUnion(maps ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, m := range maps {
		for route := range m {
			out[route] = true
		}
	}
	return out
}

func descriptorStatusRows() []statusCase {
	const patch = "PATCH /artifactory/api/system/configuration"
	const path = "/artifactory/api/system/configuration"
	return []statusCase{
		{patch, path, "admin", "localRepositories:\n  patch-local:\n    type: generic\n", 200},
		{patch, path, "admin", "localRepositories:\n  patch-bad:\n    nonsense: 1\n", 400},
		{
			patch,
			path,
			"admin",
			"localRepositories:\n  patch-env:\n    type: generic\n    projectKey: proj\n    environments: [nope]\n",
			400,
		},
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

func storageStatusRows() []statusCase {
	const (
		get   = "GET /artifactory/api/storage/{repoKey}/{itemPath}"
		put   = "PUT /artifactory/api/storage/{repoKey}/{itemPath}"
		del   = "DELETE /artifactory/api/storage/{repoKey}/{itemPath}"
		patch = "PATCH /artifactory/api/metadata/{repoKey}/{itemPath}"
		file  = "/artifactory/api/storage/libs-local/fixture/get.txt"
		meta  = "/artifactory/api/metadata/libs-local/fixture/get.txt"
		root  = "/artifactory/api/storage/libs-local"

		rootMeta = "/artifactory/api/metadata/libs-local"
	)
	return []statusCase{
		{get, file, "admin", "", 200},
		{get, "/artifactory/api/storage/libs-local", "admin", "", 200},
		{get, "/artifactory/api/storage/libs-local/fixture?list&deep=1", "admin", "", 200},
		{get, file + "?stats", "admin", "", 200},
		{get, file + "?lastModified", "admin", "", 200},
		{get, file + "?permissions", "admin", "", 200},
		{get, file + "?properties", "admin", "", 404},
		{get, file + "?properties&stats", "admin", "", 400},
		{get, file + "?deep=1", "admin", "", 400},
		{get, file + "?list", "admin", "", 400},
		{get, "/artifactory/api/storage/libs-local/absent", "admin", "", 404},
		{get, "/artifactory/api/storage/missing/x", "admin", "", 404},
		{get, file, "user", "", 403},
		{get, file, "anonymous", "", 401},
		{get, file, "", "", 401},

		{put, file + "?properties=a=1;b=2,3", "admin", "", 204},
		{get, file + "?properties", "admin", "", 200},
		{put, file + "?properties=k", "admin", "", 400},
		{put, "/artifactory/api/storage/virt/x?properties=k=v", "admin", "", 400},
		{put, "/artifactory/api/storage/remote-private/x?properties=k=v", "admin", "", 400},
		{put, "/artifactory/api/storage/libs-local/absent?properties=k=v", "admin", "", 404},
		{put, file + "?properties=k=v", "user", "", 403},
		{put, file + "?properties=k=v", "anonymous", "", 401},
		{put, file + "?properties=k=v", "", "", 401},

		{del, file + "?properties=a", "admin", "", 204},
		{del, file, "admin", "", 400},
		{del, "/artifactory/api/storage/virt/x?properties=k", "admin", "", 400},
		{del, "/artifactory/api/storage/libs-local/absent?properties=k", "admin", "", 404},
		{del, file + "?properties=k", "user", "", 403},
		{del, file + "?properties=k", "anonymous", "", 401},
		{del, file + "?properties=k", "", "", 401},

		{patch, meta, "admin", `{"props":{"c":"x","b":null}}`, 204},
		{patch, meta, "admin", `{"props":{}}`, 400},
		{patch, "/artifactory/api/metadata/virt/x", "admin", `{"props":{"k":"v"}}`, 400},
		{patch, "/artifactory/api/metadata/libs-local/absent", "admin", `{"props":{"k":"v"}}`, 404},
		{patch, meta, "user", `{"props":{"k":"v"}}`, 403},
		{patch, meta, "anonymous", `{"props":{"k":"v"}}`, 401},
		{patch, meta, "", `{"props":{"k":"v"}}`, 401},

		// The repository root holds properties of its own (#388).
		{get, root + "?properties", "admin", "", 404},
		{put, root + "?properties=team=core&recursive=0", "admin", "", 204},
		{get, root + "?properties", "admin", "", 200},
		{get, root + "?properties=absent", "admin", "", 404},
		{put, root + "?properties=k=v&recursive=0", "user", "", 403},
		{put, root + "?properties=k=v&recursive=0", "", "", 401},
		{put, "/artifactory/api/storage/virt?properties=k=v", "admin", "", 400},
		{del, root + "?properties=team&recursive=0", "admin", "", 204},
		{get, root + "?properties", "admin", "", 404},
		{del, root + "?properties=team&recursive=0", "admin", "", 204},
		{del, root + "?properties=k&recursive=0", "user", "", 403},
		{patch, rootMeta + "?recursiveProperties=0", "admin", `{"props":{"team":"core"}}`, 204},
		{get, root + "?properties", "admin", "", 200},
		{patch, rootMeta, "user", `{"props":{"k":"v"}}`, 403},
		{get, "/artifactory/api/storage/virt?properties", "admin", "", 404},
	}
}

func transferStatusRows() []statusCase {
	const (
		cp  = "POST /artifactory/api/copy/{srcRepoKey}/{srcFilePath}"
		mv  = "POST /artifactory/api/move/{srcRepoKey}/{srcFilePath}"
		ar  = "GET /artifactory/api/archive/download/{repoKey}/{path}"
		en  = "GET /artifactory/{repoKey}/{archivePath}!/{entryPath}"
		c   = "/artifactory/api/copy/libs-local/fixture/"
		m   = "/artifactory/api/move/libs-local/fixture/"
		dl  = "/artifactory/api/archive/download/libs-local/"
		lib = "/artifactory/libs-local/fixture/"
	)
	return []statusCase{
		{cp, c + "get.txt?to=/libs-local/copied/get.txt", "admin", "", 200},
		{cp, c + "get.txt", "admin", "", 400},
		{cp, c + "get.txt?to=/virt/x", "admin", "", 400},
		{cp, c + "absent?to=/libs-local/x", "admin", "", 404},
		{cp, c + "get.txt?to=/libs-local/x", "user", "", 403},
		{cp, c + "get.txt?to=/libs-local/x", "anonymous", "", 401},
		{cp, c + "get.txt?to=/libs-local/x", "", "", 401},

		{mv, m + "move.txt?to=/libs-local/moved/move.txt", "admin", "", 200},
		{mv, m + "get.txt?to=/virt/x", "admin", "", 400},
		{mv, m + "absent?to=/libs-local/x", "admin", "", 404},
		{mv, m + "get.txt?to=/libs-local/x", "user", "", 403},
		{mv, m + "get.txt?to=/libs-local/x", "", "", 401},

		{ar, dl + "fixture?archiveType=zip", "admin", "", 200},
		{ar, dl + "fixture?archiveType=rar", "admin", "", 400},
		{ar, dl + "absent?archiveType=zip", "admin", "", 404},
		{ar, dl + "fixture?archiveType=zip", "user", "", 403},
		{ar, dl + "fixture?archiveType=zip", "", "", 401},

		{en, lib + "archive.zip!/inner.txt", "admin", "", 200},
		{en, lib + "archive.zip!/absent", "admin", "", 404},
		{en, lib + "archive.zip!/inner.txt", "user", "", 403},
		{en, lib + "archive.zip!/inner.txt", "", "", 401},
	}
}
