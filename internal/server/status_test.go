package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
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
	"github.com/weaveplatform/artifactweave/test/server/support"
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
	// "admin-token" and "user-token" (an identity token of admin's or
	// user's, as a Bearer token), "expired" (an expired, refreshable access
	// token of admin's, as a Bearer token) or "forged" (that token with its
	// signature altered). Any other value is "<user>:<password>" as Basic,
	// or the name of a statusSecrets token "<name>-access" as Bearer.
	// A body names the fixture's other token values as {name}
	// (statusSecrets). A body of checksum:<sha1> is a checksum deploy, and
	// one of declared:<content> an upload declaring its SHA-1
	// (checksumDeploy).
	as   string
	body string
	want int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

// checksumDeploy turns a row body of "checksum:<sha1>" into the request of
// a checksum deploy of that SHA-1: the headers that ask for one, which a row
// has no field for, and no body. A body of "declared:<content>" is an upload
// of content with its SHA-1 declared, as the JFrog CLI sends one. A body of
// "explode:<name>" is an archive deploy of a zip holding <name>.
func checksumDeploy(headers map[string]string, body string) (map[string]string, string) {
	sum, byChecksum := strings.CutPrefix(body, "checksum:")
	content, declared := strings.CutPrefix(body, "declared:")
	entry, explode := strings.CutPrefix(body, "explode:")
	if !byChecksum && !declared && !explode {
		return headers, body
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if explode {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, _ := zw.Create(entry) // a buffer cannot fail to take a write
		_, _ = io.WriteString(f, entry)
		_ = zw.Close()
		headers["X-Explode-Archive"] = "true"
		return headers, buf.String()
	}
	if declared {
		headers["X-Checksum-Sha1"] = sha1Hex(content)
		return headers, content
	}
	headers["X-Checksum-Deploy"], headers["X-Checksum-Sha1"] = "true", sum
	return headers, ""
}

func statusTable() []statusCase {
	return slices.Concat(
		systemStatusRows(),
		sinkStatusRows(),
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
		// Version answers any caller but anonymous while anonymous access is
		// off, unlike ping and the probes (RTFACT-26414).
		{"GET /artifactory/api/system/version", "/artifactory/api/system/version", "", "", 401},
		{
			"GET /artifactory/api/system/version",
			"/artifactory/api/system/version",
			"anonymous",
			"",
			200,
		},
		{"GET /artifactory/api/system/version", "/artifactory/api/system/version", "user", "", 200},
		{
			"GET /artifactory/api/system/version",
			"/artifactory/api/system/version",
			"admin",
			"",
			200,
		},
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
		// Service id is admin only, as getserviceid-1.md documents: anonymous
		// is told to authenticate even while anonymous access is on.
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"admin",
			"",
			200,
		},
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"",
			"",
			401,
		},
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"user",
			"",
			403,
		},
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"anonymous",
			"",
			401,
		},
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"admin-token",
			"",
			200,
		},
		{
			"GET /artifactory/api/system/service_id",
			"/artifactory/api/system/service_id",
			"user-token",
			"",
			403,
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
		// The router's and Access's probes answer without credentials
		// (#403); wrong credentials are still refused.
		{"GET /access/api/v1/system/ping", "/access/api/v1/system/ping", "", "", 200},
		{"GET /access/api/v1/system/readiness", "/access/api/v1/system/readiness", "", "", 200},
		{"GET /access/api/v1/system/liveness", "/access/api/v1/system/liveness", "", "", 200},
		{"GET /router/api/v1/system/health", "/router/api/v1/system/health", "", "", 200},
		{"GET /router/api/v1/system/readiness", "/router/api/v1/system/readiness", "", "", 200},
		{
			"GET /router/api/v1/system/readiness",
			"/router/api/v1/system/readiness",
			"forged",
			"",
			401,
		},
		{"GET /router/api/v1/system/liveness", "/router/api/v1/system/liveness", "", "", 200},
	}
}

// sinkStatusRows are the telemetry sinks: public, and limited to
// sinkBodyLimit bytes of body (#431).
func sinkStatusRows() []statusCase {
	const report = `{"productId":"jfrog-cli-go/2.60.0","features":[]}`
	oversized := strings.Repeat("x", sinkBodyLimit+1)
	var rows []statusCase
	for _, route := range []string{
		"POST /artifactory/api/system/usage",
		"POST /jfconnect/api/v1/backoffice/metrics/log",
	} {
		_, path, _ := strings.Cut(route, " ")
		rows = append(rows,
			statusCase{route, path, "", report, 200},
			statusCase{route, path, "anonymous", report, 200},
			statusCase{route, path, "admin", report, 200},
			statusCase{route, path, "", oversized, 413},
		)
	}
	return rows
}

// statusFixture is a seeded store with the admin (password "pw"), a user
// "user" (password "pw") in no group, a locked user "locked" (password
// "pw"), and the repositories the rows use.
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
	// A locked account, until securityStatusRows unlocks every user.
	if err := db.Users().Create(ctx, types.User{
		Name: "locked", PasswordHash: cheapHash, Status: types.UserLocked,
	}); err != nil {
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
	seedOverwriteStatus(t, db, items)
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
		// What jf rt s --offset 1 sends: .offset() beside "property".
		qOffset = `items.find({"repo":"libs-local"}).include("repo","path","name","property").offset(1)`
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
		// Checksum deploys: fixture/get.txt holds "fixture". A caller that
		// may read no file with the content gets the 404 of a miss (#379;
		// the matrix has callers that may deploy but not read).
		{put, lib + "/new/cs.bin", "admin", "checksum:" + sha1Hex("fixture"), 201},
		{put, lib + "/new/cs-miss.bin", "admin", "checksum:" + sha1Hex("never stored"), 404},
		{put, lib + "/new/cs-bad.bin", "admin", "checksum:zz", 400},
		{put, lib + "/new/cs.bin", "user", "checksum:" + sha1Hex("fixture"), 403},
		{put, lib + "/new/cs.bin", "anonymous", "checksum:" + sha1Hex("fixture"), 401},
		{put, lib + "/new/cs.bin", "", "checksum:" + sha1Hex("fixture"), 401},

		{get, lib + "/fixture/get.txt", "admin", "", 200},
		{get, lib + "/fixture/get.txt.sha256", "admin", "", 200},
		// Under client-checksums a sidecar serves the client's checksum:
		// get.txt was deployed with none, so 404 until the client sends one
		// (#399). The checksum deploy above recorded all of cs.bin's.
		{get, lib + "/fixture/get.txt.sha1", "admin", "", 404},
		{head, lib + "/fixture/get.txt.md5", "admin", "", 404},
		{get, lib + "/fixture/get.txt.sha1", "user", "", 403},
		{put, lib + "/fixture/get.txt.sha1", "admin", sha1Hex("fixture"), 201},
		{get, lib + "/fixture/get.txt.sha1", "admin", "", 200},
		{get, lib + "/new/cs.bin.md5", "admin", "", 200},
		{get, lib + "/fixture/", "admin", "", 200},
		{get, lib + "/fixture/absent", "admin", "", 404},
		{get, "/artifactory/missing/a.txt", "admin", "", 404},
		{get, lib + "/fixture/get.txt", "user", "", 403},
		{get, lib + "/fixture/get.txt", "anonymous", "", 401},
		{get, lib + "/fixture/get.txt", "", "", 401},
		// A trace is the resolution as text, answered 200 with the status
		// the download would answer inside it.
		{get, lib + "/fixture/get.txt?trace", "admin", "", 200},
		{get, lib + "/fixture/absent?trace", "admin", "", 200},
		{get, lib + "/fixture/get.txt?trace", "user", "", 200},
		{get, lib + "/fixture/get.txt?trace", "", "", 401},

		{head, lib + "/fixture/get.txt", "admin", "", 200},
		{head, lib + "/fixture/absent", "admin", "", 404},
		{head, lib + "/fixture/get.txt", "", "", 401},
		{head, lib + "/fixture/get.txt?trace", "admin", "", 200},

		{remove, lib + "/fixture/del.txt", "admin", "", 204},
		{remove, lib + "/fixture/absent", "admin", "", 404},
		{remove, "/artifactory/virt/x", "admin", "", 405},
		{remove, lib + "/fixture/get.txt", "user", "", 403},
		{remove, lib + "/fixture/get.txt", "anonymous", "", 401},
		{remove, lib + "/fixture/get.txt", "", "", 401},

		{aqlOp, "/artifactory/api/search/aql", "admin", q, 200},
		{aqlOp, "/artifactory/api/search/aql", "user", q, 200},
		{aqlOp, "/artifactory/api/search/aql", "admin", qOffset, 200},
		{aqlOp, "/artifactory/api/search/aql", "user", qOffset, 200},
		{aqlOp, "/artifactory/api/search/aql", "admin", "items.find(", 400},
		{aqlOp, "/artifactory/api/search/aql", "anonymous", q, 403},
		{aqlOp, "/artifactory/api/search/aql", "admin", "", 400},
		{aqlOp, "/artifactory/api/search/aql", "", q, 401},

		{upl, "/artifactory/api/v1/uploads/config", "admin", "", 200},
		{upl, "/artifactory/api/v1/uploads/config", "", "", 401},

		// A caller that may deploy but not delete replaces a file only with
		// its own content; any other is refused with Artifactory's text
		// (#565, TestOverwriteRefusalText).
		{put, lib + "/overwrite/taken.txt", overwriter, "changed", 403},
		{put, lib + "/overwrite/taken.txt", overwriter, "declared:taken", 201},
		{
			put,
			lib + "/overwrite/taken.txt",
			overwriter,
			"checksum:" + sha1Hex("other content"),
			403,
		},
		{put, lib + "/overwrite/fresh.txt", overwriter, "fresh", 201},
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
		{list, r + "?packageType=Generic", "admin", "", 200},
		{list, r + "?packageType=yum", "user", "", 200},
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
		// The seeded Any Remote grants anonymous nothing (#383).
		{get, r + "/remote-private", "anonymous", "", 401},
		{get, r + "/libs-local", "anonymous", "", 401},
		{get, r + "/libs-local", "user", "", 403},
		{get, r + "/missing", "admin", "", 404},
		{get, r + "/libs-local", "", "", 401},

		{create, r + "/new-local", "admin", `{"rclass":"local"}`, 200},
		{create, r + "/LIBS-LOCAL", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/no-url", "admin", `{"rclass":"remote"}`, 400},
		{create, r + "/new-virt", "admin", `{"rclass":"virtual","repositories":["ghost"]}`, 400},
		// A virtual repository may have no members; one of another package
		// type is refused unless the virtual repository is generic.
		{create, r + "/empty-virt", "admin", `{"rclass":"virtual"}`, 200},
		{
			create,
			r + "/npm-empty",
			"admin",
			`{"rclass":"virtual","packageType":"npm","repositories":[]}`,
			200,
		},
		{
			create,
			r + "/deploy-virt",
			"admin",
			`{"rclass":"virtual","defaultDeploymentRepo":"libs-local"}`,
			400,
		},
		{
			create,
			r + "/npm-virt",
			"admin",
			`{"rclass":"virtual","packageType":"npm","repositories":["libs-local"]}`,
			400,
		},
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
		// The repository key rules (#395).
		{create, r + "/" + strings.Repeat("k", 64), "admin", `{"rclass":"local"}`, 200},
		{create, r + "/" + strings.Repeat("l", 65), "admin", `{"rclass":"local"}`, 400},
		{
			create,
			r + "/" + strings.Repeat("r", 58),
			"admin",
			`{"rclass":"remote","url":"https://x"}`,
			200,
		},
		{
			create,
			r + "/" + strings.Repeat("s", 59),
			"admin",
			`{"rclass":"remote","url":"https://x"}`,
			400,
		},
		{
			create,
			r + "/" + strings.Repeat("d", 64),
			"admin",
			`{"rclass":"local","packageType":"docker"}`,
			400,
		},
		{create, r + "/1st-local", "admin", `{"rclass":"local"}`, 200},
		{create, r + "/a:b", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/a*b", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/release-bundles", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/Auto-Trashcan", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/jfrog-system-reserved-x", "admin", `{"rclass":"local"}`, 400},
		{create, r + "/Docker-Keys", "admin", `{"rclass":"local","packageType":"docker"}`, 400},
		{create, r + "/docker_keys", "admin", `{"rclass":"local","packageType":"docker"}`, 400},
		{create, r + "/docker-keys", "admin", `{"rclass":"local","packageType":"docker"}`, 200},
		{update, r + "/docker-keys", "admin", `{"key":"Docker-Keys"}`, 400},
		// Package types, fields and minimums as 7.x has them (#396).
		{create, r + "/hex-local", "admin", `{"rclass":"local","packageType":"hex"}`, 200},
		{
			create,
			r + "/ml-remote",
			"admin",
			`{"rclass":"remote","packageType":"machinelearning","url":"https://x"}`,
			400,
		},
		{
			create,
			r + "/tf-fed",
			"admin",
			`{"rclass":"federated","packageType":"terraform","terraformType":"provider"}`,
			200,
		},
		{
			create,
			r + "/list-remote",
			"admin",
			`{"rclass":"remote","url":"https://x","listRemoteFolderItems":"false"}`,
			200,
		},
		{
			create,
			r + "/fast-remote",
			"admin",
			`{"rclass":"remote","url":"https://x","retrievalCachePeriodSecs":29}`,
			400,
		},
		// releasebundles on local and federated repositories only (#492).
		{
			create,
			r + "/rb-release-bundles-v2",
			"admin",
			`{"rclass":"local","packageType":"releasebundles"}`,
			200,
		},
		{
			create,
			r + "/fed-release-bundles-v2",
			"admin",
			`{"rclass":"federated","packageType":"releasebundles"}`,
			200,
		},
		{
			create,
			r + "/rb-remote",
			"admin",
			`{"rclass":"remote","packageType":"releasebundles","url":"https://x"}`,
			400,
		},
		{
			create,
			r + "/rb-virtual",
			"admin",
			`{"rclass":"virtual","packageType":"releasebundles"}`,
			400,
		},

		{update, r + "/libs-local", "admin", `{"description":"updated"}`, 200},
		{update, r + "/libs-local", "admin", `{"projectKey":"proj","environments":["nope"]}`, 400},
		{update, r + "/libs-local", "admin", `{"rclass":"remote"}`, 400},
		// A key differs in case only, and the seeded build-info repository (#472).
		{update, r + "/libs-local", "admin", `{"key":"LIBS-LOCAL"}`, 400},
		{update, r + "/artifactory-build-info", "admin", `{"description":"builds"}`, 200},
		// A class or package type respelt in case is the stored one (#492).
		{update, r + "/libs-local", "admin", `{"rclass":"LOCAL","packageType":"Generic"}`, 200},
		{update, r + "/libs-local", "admin", `{"packageType":"Maven"}`, 400},
		{update, r + "/empty-virt", "admin", `{"description":"no members"}`, 200},
		{update, r + "/npm-empty", "admin", `{"repositories":["libs-local"]}`, 400},
		{update, r + "/missing", "admin", `{"description":"x"}`, 404},
		{update, r + "/libs-local", "user", `{"description":"x"}`, 403},
		{update, r + "/libs-local", "", `{"description":"x"}`, 401},

		{remove, r + "/del-one", "admin", "", 200},
		{remove, r + "/missing", "admin", "", 404},
		{remove, r + "/auto-trashcan", "admin", "", 400},
		{remove, r + "/jfrog-support-bundle", "admin", "", 400},
		{remove, r + "/artifactory-build-info", "admin", "", 403},
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
		{
			bCreate,
			v2 + "/batch",
			"admin",
			`[{"key":"b6","rclass":"virtual","repositories":[]}]`,
			201,
		},
		{
			bCreate,
			v2 + "/batch",
			"admin",
			`[{"key":"b7","rclass":"virtual","packageType":"npm","repositories":["libs-local"]}]`,
			400,
		},
		{bCreate, v2 + "/batch", "admin", `[{"key":"libs-local","rclass":"local"}]`, 409},
		{bCreate, v2 + "/batch", "admin", `[]`, 400},
		{
			bCreate,
			v2 + "/batch",
			"admin",
			`[{"key":"b8","rclass":"local"},{"key":"..","rclass":"local"}]`,
			400,
		},
		{
			bCreate,
			v2 + "/batch",
			"admin",
			`[{"key":"oci_keys","rclass":"local","packageType":"oci"}]`,
			400,
		},
		{bCreate, v2 + "/batch", "user", `[{"key":"b4","rclass":"local"}]`, 403},
		{bCreate, v2 + "/batch", "", `[{"key":"b5","rclass":"local"}]`, 401},

		{bUpdate, v2 + "/batch", "admin", `[{"key":"upd-batch","description":"x"}]`, 200},
		{bUpdate, v2 + "/batch", "admin", `[{"key":"upd-batch","packageType":"GENERIC"}]`, 200},
		{bUpdate, v2 + "/batch", "admin", `[{"description":"no key"}]`, 400},
		{
			bUpdate,
			v2 + "/batch",
			"admin",
			`[{"key":"npm-empty","repositories":["libs-local"]}]`,
			400,
		},
		{bUpdate, v2 + "/batch", "admin", `[{"key":"missing"}]`, 404},
		{bUpdate, v2 + "/batch", "admin", `[{"key":"UPD-BATCH"}]`, 404},
		{bUpdate, v2 + "/batch", "user", `[{"key":"upd-batch"}]`, 403},
		{bUpdate, v2 + "/batch", "", `[{"key":"upd-batch"}]`, 401},

		{bDelete, v2 + "/batch", "admin", `["del-batch","missing"]`, 200},
		{bDelete, v2 + "/batch", "admin", `[]`, 400},
		{bDelete, v2 + "/batch", "admin", `["libs-local","auto-trashcan"]`, 400},
		{bDelete, v2 + "/batch", "admin", `["libs-local","artifactory-build-info"]`, 403},
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
	// The fixture keeps token.allow_basic_auth off, its default, so the
	// Access token API takes no password; these come from the legacy route,
	// which does.
	live := map[string]string{}
	for _, u := range []string{"admin", "user"} {
		rec := do(t, off, http.MethodPost, "/artifactory/api/security/token",
			map[string]string{
				"Authorization": basic(u, "pw")["Authorization"],
				"Content-Type":  "application/x-www-form-urlencoded",
			}, "username="+u)
		tok, _ := decodeObject(t, rec.Body.String())["access_token"].(string)
		if rec.Code != http.StatusOK || tok == "" {
			t.Fatalf("%s's token: %d %s", u, rec.Code, rec.Body)
		}
		live[u+"-token"] = tok
	}
	for _, c := range statusTable() {
		h, headers := off, map[string]string(nil)
		switch c.as {
		case "anonymous":
			h = on
		case "admin", "user":
			headers = basic(c.as, "pw")
		case "admin-token", "user-token":
			headers = bearerAuth(live[c.as])
		case "expired":
			headers = bearerAuth(secrets["expired-access"])
		case "forged":
			headers = bearerAuth(secrets["forged-access"])
		default:
			if user, password, ok := strings.Cut(c.as, ":"); ok {
				headers = basic(user, password)
			} else if tok, ok := secrets[c.as+"-access"]; ok {
				headers = bearerAuth(tok)
			}
		}
		var body string
		headers, body = checksumDeploy(headers, fill.Replace(c.body))
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
		registryRoutes, support.NuGetRoutes(), support.NPMRoutes(),
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
		{patch, path, "admin", "backups:\n  daily:\n    enabled: true\n", 400},
		{patch, path, "admin", "localRepositories:\n  patch-null:\n    blackedOut: null\n", 400},
		{
			patch,
			path,
			"admin",
			"localRepositories:\n  patch-env:\n    type: generic\n    projectKey: proj\n    environments: [nope]\n",
			400,
		},
		// The artifactory_repository_layout resource repeats the key as name.
		{
			patch,
			path,
			"admin",
			"repoLayouts:\n  patch-layout:\n    name: patch-layout\n    artifactPathPattern: \"[module]/[baseRev]\"\n",
			200,
		},
		{
			patch,
			path,
			"admin",
			"repoLayouts:\n  patch-layout:\n    name: other\n    artifactPathPattern: \"[module]/[baseRev]\"\n",
			400,
		},
		// The repository key rules (#395).
		{patch, path, "admin", "localRepositories:\n  patch_oci:\n    type: oci\n", 400},
		{patch, path, "admin", "localRepositories:\n  jfrog-logs:\n    type: generic\n", 400},
		{
			patch,
			path,
			"admin",
			"remoteRepositories:\n  " + strings.Repeat(
				"p",
				59,
			) + ":\n    type: generic\n    url: https://x\n",
			400,
		},
		{patch, path, "", "localRepositories: {}", 401},
		{patch, path, "anonymous", "localRepositories: {}", 401},
		{patch, path, "user", "localRepositories: {}", 403},
	}
}

func TestStatusTableCoversImplemented(t *testing.T) {
	// NuGet and npm route assertions live in independently runnable packages.
	// Those packages guard their inventories against both tables.
	covered := routeUnion(support.NuGetRoutes(), support.NPMRoutes())
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
		{
			get,
			root + "/?list=&deep=1&depth=3&listFolders=1&mdTimestamps=1&includeRootPath=1",
			"admin",
			"",
			200,
		},
		{get, root + "?list&includeRootPath=2", "admin", "", 400},
		{get, root + "?list&mdTimestamps=yes", "admin", "", 400},
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
		{patch, meta, "admin", `{"props":{}}`, 204},
		{patch, meta, "admin", `{}`, 400},
		{patch, meta, "admin", `{"stats":{"downloadCount":3,"lastDownloaded":1700000000000}}`, 204},
		{patch, meta, "admin", `{"stats":{"downloads":3}}`, 400},
		{
			patch, "/artifactory/api/metadata/libs-local/fixture", "admin",
			`{"stats":{"downloadCount":3}}`, 400,
		},
		{patch, meta, "user", `{"stats":{"downloadCount":3}}`, 403},
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
		put = "PUT /artifactory/{repoKey}/{filePath}"
	)
	return []statusCase{
		{cp, c + "get.txt?to=/libs-local/copied/get.txt", "admin", "", 200},
		{cp, c + "get.txt", "admin", "", 400},
		{cp, c + "get.txt?to=/virt/x", "admin", "", 400},
		// A missing source is a 400 with a report (#400).
		{cp, c + "absent?to=/libs-local/x", "admin", "", 400},
		{cp, "/artifactory/api/copy/missing/x?to=/libs-local/x", "admin", "", 400},
		{cp, c + "get.txt?to=/libs-local/x", "user", "", 403},
		{cp, c + "get.txt?to=/libs-local/x", "anonymous", "", 401},
		{cp, c + "get.txt?to=/libs-local/x", "", "", 401},

		{mv, m + "move.txt?to=/libs-local/moved/move.txt", "admin", "", 200},
		{mv, m + "get.txt?to=/virt/x", "admin", "", 400},
		{mv, m + "absent?to=/libs-local/x", "admin", "", 400},
		{mv, "/artifactory/api/move/missing/x?to=/libs-local/x", "admin", "", 400},
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

		// An archive deploy answers 201 text/plain (#474).
		{put, lib + "exploded/bundle.zip", "admin", "explode:inner.txt", 201},
		{put, lib + "exploded/bundle.zip", "user", "explode:inner.txt", 403},
		{put, lib + "exploded/bundle.zip", "", "explode:inner.txt", 401},

		// Onto a file the caller may not delete, an archive is refused and
		// a copy fails in its report (#565).
		{
			put, "/artifactory/libs-local/overwrite/bundle.zip", overwriter,
			"explode:taken.txt", 403,
		},
		{
			cp,
			"/artifactory/api/copy/libs-local/overwrite/other.txt?to=/libs-local/overwrite/taken.txt",
			overwriter,
			"",
			200,
		},
	}
}
