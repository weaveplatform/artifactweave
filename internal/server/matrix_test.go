package server

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/internal/service/oidc"
	"github.com/weaveplatform/artifactweave/internal/service/roles"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The route × principal matrix (#84, the verification decision 0006 asks
// for): every implemented route, called by each of a fixed set of
// principals. A row names who may make its request with a rule written from
// the permission model (administrators, signedIn, holds, ...), and each
// principal's expected status follows from that rule and what the principal
// holds, never from what the handlers answer today.
// TestMatrixCoversImplemented fails while an implemented route has no row.

// matrixGrant is content a principal holds: actions on the paths of a
// repository under a prefix ("" is all of it; repo "*" is every repository).
type matrixGrant struct {
	repo, under string
	actions     types.Actions
}

// matrixPrincipal is one caller, and what the permission model says it is.
type matrixPrincipal struct {
	// name is the subtest name and a row's {p}.
	name string
	// user is the user it acts as, a row's {u}.
	user string
	// anonymous: no credentials; anonymousOn: anonymous access is on.
	anonymous, anonymousOn bool
	// bad: credentials that do not verify, which every route answers 401.
	bad bool
	// token: an access token with scope, as a Bearer credential,
	// authenticates it.
	token bool
	scope string
	// narrowed: a token scope narrower than the user's identity, which must
	// not become a broader credential (decision 0005: scopes only narrow).
	narrowed bool
	// registry: a registry token, which only the registry's routes accept
	// (decision 0005).
	registry bool
	// identity: the token's identity when its user need not be stored: an
	// OIDC exchange's, which a registry token keeps, or a transient user's
	// an administrator minted for a name that is no stored user.
	identity types.TokenIdentity
	// admin: acts as an administrator.
	admin  bool
	grants []matrixGrant
}

const (
	rwd        = types.ActionRead | types.ActionWrite | types.ActionDelete
	rawd       = rwd | types.ActionAnnotate
	allContent = rawd | types.ActionManage
)

// matrixPrincipals are the callers. The fixture gives each what its grants
// say: libs-local is repository A and libs-b repository B.
var matrixPrincipals = []matrixPrincipal{
	// An administrator holds every action everywhere.
	{name: "admin", user: "admin", admin: true, grants: []matrixGrant{{"*", "", allContent}}},
	// An administrator through a group with admin privileges, not the flag.
	{name: "gadmin", user: "gadmin", admin: true, grants: []matrixGrant{{"*", "", allContent}}},
	{name: "nobody", user: "nobody"},
	// Grants on artifactory-build-info are the build resource's (#165).
	{name: "reader", user: "reader", grants: slices.Concat([]matrixGrant{
		{"libs-local", "", types.ActionRead},
		{types.BuildInfoRepository, "", types.ActionRead},
		{dockerA, "", types.ActionRead},
	}, formatGrants("", types.ActionRead))},
	{name: "writer", user: "writer", grants: slices.Concat([]matrixGrant{
		{"libs-local", "work/", rwd},
		{types.BuildInfoRepository, "", types.ActionRead | types.ActionWrite},
		{dockerA, "work/", rwd},
	}, formatGrants("work/", rwd))},
	// member holds A and every build through its group, and reads B through
	// a direct grant.
	{name: "member", user: "member", grants: slices.Concat([]matrixGrant{
		{
			"libs-local",
			"",
			rawd,
		},
		{"libs-b", "", types.ActionRead},
		{types.BuildInfoRepository, "", rwd},
		{dockerA, "", rawd},
	}, formatGrants("", rawd))},
	// Anonymous with anonymous access on holds nothing: since 7.84.3
	// neither seeded permission grants it, Any Remote included (#383).
	{name: "anon-on", user: auth.AnonymousUser, anonymous: true, anonymousOn: true},
	{name: "anon-off", user: auth.AnonymousUser, anonymous: true},
	// member's token scoped to A: what member holds in A, nothing in B.
	{
		name: "tok-repo", user: "member", token: true, scope: "artifact:libs-local:*",
		narrowed: true, grants: []matrixGrant{{"libs-local", "", rawd}},
	},
	// member's token scoped to its group, which drops member's direct grant
	// on B.
	{
		name: "tok-groups", user: "member", token: true, scope: "applied-permissions/groups:team",
		narrowed: true, grants: slices.Concat([]matrixGrant{
			{"libs-local", "", rawd}, {types.BuildInfoRepository, "", rwd}, {dockerA, "", rawd},
		}, formatGrants("", rawd)),
	},
	// admin's token scoped to A: every action there, but no admin endpoint.
	{
		name: "tok-admin-repo", user: "admin", token: true, scope: "artifact:libs-local:*",
		narrowed: true, grants: []matrixGrant{{"libs-local", "", allContent}},
	},
	// A token an administrator minted for ghost, a name that is no stored
	// user, scoped to member's group: a transient user acting through team
	// alone, as tok-groups does (#406).
	{
		name: "tok-transient", user: "ghost", token: true, scope: "applied-permissions/groups:team",
		narrowed: true, identity: types.IdentityTransient, grants: slices.Concat([]matrixGrant{
			{"libs-local", "", rawd}, {types.BuildInfoRepository, "", rwd}, {dockerA, "", rawd},
		}, formatGrants("", rawd)),
	},
	// applied-permissions/admin for a name that is no stored user: an
	// administrator all the same (#406).
	{
		name: "tok-admin-scope", user: "ghost-admin", token: true,
		scope: "applied-permissions/admin", identity: types.IdentityTransient, admin: true,
		grants: []matrixGrant{{"*", "", allContent}},
	},
	// Issued an hour and a half ago for an hour (seedMatrixTokens).
	{name: "tok-expired", user: "admin", token: true, scope: "applied-permissions/user", bad: true},
	// Revoked once issued (seedMatrixTokens).
	{
		name: "tok-revoked", user: "member", token: true, scope: "applied-permissions/user",
		bad: true,
	},
	// member's registry token, granting nothing (seedMatrixTokens): every
	// route outside the registry answers it 401.
	{name: "tok-registry", user: "member", token: true, registry: true, narrowed: true},
	// The registry token of a transient user from an OIDC exchange, who is
	// not stored (#248): it answers exactly as tok-registry does.
	{
		name: "tok-registry-oidc", user: "ci-oidc", token: true, registry: true, narrowed: true,
		identity: types.IdentityTransient,
	},
}

// holds reports whether p holds every action in want at path in repo.
func (p matrixPrincipal) holds(repoKey, path string, want types.Actions) bool {
	var have types.Actions
	for _, g := range p.grants {
		if (g.repo == "*" || g.repo == repoKey) && strings.HasPrefix(path, g.under) {
			have |= g.actions
		}
	}
	return have.Has(want)
}

// sees reports whether p sees repoKey in listings and reads its
// configuration: it may read somewhere in it.
func (p matrixPrincipal) sees(repoKey string) bool {
	return slices.ContainsFunc(p.grants, func(g matrixGrant) bool {
		return (g.repo == "*" || g.repo == repoKey) && g.actions.Has(types.ActionRead)
	})
}

// A rule answers whether p may make a row's request: 0 when it may, or the
// status of the refusal.
type rule func(p matrixPrincipal) int

// refuse is a refusal: anonymous is told to authenticate, anyone else that
// it may not.
func refuse(p matrixPrincipal) int {
	if p.anonymous {
		return http.StatusUnauthorized
	}
	return http.StatusForbidden
}

// noCredentials: anyone, credentials or not, as publicRoutes lists.
func noCredentials(matrixPrincipal) int { return 0 }

// anyone: any caller, anonymous only while anonymous access is on; what it
// sees may be filtered.
func anyone(p matrixPrincipal) int {
	if p.anonymous && !p.anonymousOn {
		return http.StatusUnauthorized
	}
	return 0
}

// signedIn: any caller but anonymous, acting on itself or on what it may
// see.
func signedIn(p matrixPrincipal) int {
	if p.anonymous {
		return http.StatusUnauthorized
	}
	return 0
}

// aqlCaller: a signed-in caller. Anonymous is told to authenticate while
// anonymous access is off and refused while it is on, as Artifactory refuses
// anonymous AQL (research 22 §1.3).
func aqlCaller(p matrixPrincipal) int {
	if p.anonymous && p.anonymousOn {
		return http.StatusForbidden
	}
	return signedIn(p)
}

// wholeIdentity: a signed-in caller with its whole identity, a password or an
// identity-scoped token, for requests that mint a credential.
func wholeIdentity(p matrixPrincipal) int {
	if p.narrowed {
		return http.StatusForbidden
	}
	return signedIn(p)
}

// accessMinter: wholeIdentity through a token. A password creates no token
// through the Access token API while token.allow_basic_auth is off, its
// default, which the fixture keeps (#385); the legacy route takes one.
func accessMinter(p matrixPrincipal) int {
	if !p.token && !p.anonymous {
		return http.StatusUnauthorized
	}
	return wholeIdentity(p)
}

// ownRecord is r for a request on the caller's own user record, which a
// transient user does not have: nothing is stored for it, so the request
// finds no user (404).
func ownRecord(r rule) rule {
	return func(p matrixPrincipal) int {
		if s := r(p); s != 0 || p.identity != types.IdentityTransient {
			return s
		}
		return http.StatusNotFound
	}
}

// passwordLogin: a caller signed in with a password; the encrypted password
// stands for one, which a token does not prove (400).
func passwordLogin(p matrixPrincipal) int {
	if p.token {
		return http.StatusBadRequest
	}
	return signedIn(p)
}

func administrators(p matrixPrincipal) int {
	if p.admin {
		return 0
	}
	return refuse(p)
}

// holds: a caller holding every action in want at path in repoKey.
func holds(repoKey, path string, want types.Actions) rule {
	return func(p matrixPrincipal) int {
		if p.holds(repoKey, path, want) {
			return 0
		}
		return refuse(p)
	}
}

// holdsEither: a caller holding a or b at path in repoKey.
func holdsEither(repoKey, path string, a, b types.Actions) rule {
	return func(p matrixPrincipal) int {
		if p.holds(repoKey, path, a) || p.holds(repoKey, path, b) {
			return 0
		}
		return refuse(p)
	}
}

// sees: a caller that may read repoKey's configuration.
func sees(repoKey string) rule {
	return func(p matrixPrincipal) int {
		if p.sees(repoKey) {
			return 0
		}
		return refuse(p)
	}
}

// trashRule is r with Artifactory's trash refusal: 400, not 403, for a
// signed-in caller.
func trashRule(r rule) rule {
	return func(p matrixPrincipal) int {
		s := r(p)
		if s == http.StatusForbidden {
			return http.StatusBadRequest
		}
		return s
	}
}

// reusing: a checksum deploy, which needs deploy, and then reuses the
// content only for a caller that read allows to read a file holding it;
// any other caller gets the 404 of content the server lacks (#379).
func reusing(deploy, read rule) rule {
	return func(p matrixPrincipal) int {
		if s := deploy(p); s != 0 {
			return s
		}
		if read(p) != 0 {
			return http.StatusNotFound
		}
		return 0
	}
}

// uploadToken: the multipart calls after create carry an upload token or a
// part signature, which no principal has.
func uploadToken(matrixPrincipal) int { return http.StatusUnauthorized }

// imageOperation reports whether a registry path is an operation a
// registry token must grant: one on an image, or a catalog, which needs
// registry:catalog:*.
func imageOperation(path string) bool {
	return slices.ContainsFunc(
		[]string{"/blobs/", "/manifests/", "/tags/list", "/referrers/", "/_catalog"},
		func(op string) bool { return strings.Contains(path, op) },
	)
}

// matrixRow is one request each principal makes. {p} in its path or body is
// the principal's name, so state-changing rows touch only what the fixture
// seeded for that principal, such as its victim user v-{p}; {u} is the
// principal's user, and {token} an access token of v-{p}.
type matrixRow struct {
	route      string
	path, body string
	who        rule
	// ok is the answer to a principal the rule allows.
	ok int
	// fresh runs each principal against a new fixture, for a request that
	// changes what every principal's later requests see.
	fresh bool
	// seed adds what the row acts on for one principal.
	seed seeder
}

// A seeder adds what a row acts on for the principal named p.
type seeder func(t *testing.T, f *matrixFixture, p string)

func row(route, path, body string, who rule, ok int) matrixRow {
	return matrixRow{route: route, path: path, body: body, who: who, ok: ok}
}

// at is a row whose path is its route's, for a route without parameters.
func at(route, body string, who rule, ok int) matrixRow {
	_, path, _ := strings.Cut(route, " ")
	return row(route, path, body, who, ok)
}

func (r matrixRow) alone() matrixRow { r.fresh = true; return r }

func (r matrixRow) seeding(seed seeder) matrixRow { r.seed = seed; return r }

// deployed seeds a file at path in libs-local, {p} the principal's name.
func deployed(path string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		f.deploy(t, "libs-local", strings.ReplaceAll(path, "{p}", p), []byte(p))
	}
}

// onlyInB seeds content no repository but libs-b holds.
func onlyInB(t *testing.T, f *matrixFixture, _ string) {
	t.Helper()
	f.deploy(t, "libs-b", "b/only.txt", []byte("only in b"))
}

// trashed seeds a file at path in the trash can, deleted from repoKey.
func trashed(repoKey, path string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		path := strings.ReplaceAll(path, "{p}", p)
		c := f.deploy(t, repoKey, path, []byte(p))
		if _, err := f.items.Delete(
			context.Background(),
			item.Target{Repo: c, Path: path},
			"admin",
		); err != nil {
			t.Fatal(err)
		}
	}
}

// published seeds run 1 of the build name, {p} the principal's name.
func published(name string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		seedBuild(t, f.db, f.items.Blobs, strings.ReplaceAll(name, "{p}", p))
	}
}

// want is the status p must get. A registry token granting nothing is
// good for the registry alone, and an image operation there that would
// have answered it, or refused its user, tells it to fetch a token that
// grants the operation; one that answers everybody the same, as a blob
// delete's 405, still does.
func (r matrixRow) want(p matrixPrincipal) int {
	path, _, _ := strings.Cut(r.path, "?")
	if p.bad || p.registry && !docker.IsRegistryPath(path) {
		return http.StatusUnauthorized
	}
	s := r.who(p)
	if p.registry && imageOperation(path) && (s != 0 || r.ok < 300) {
		return http.StatusUnauthorized
	}
	if s != 0 {
		return s
	}
	return r.ok
}

func matrixTable() []matrixRow {
	return slices.Concat(
		systemMatrix(),
		repositoryMatrix(),
		contentMatrix(),
		storageMatrix(),
		trashMatrix(),
		keypairMatrix(),
		roleMatrix(),
		permissionMatrix(),
		tokenMatrix(),
		securityMatrix(),
		accessIdentityMatrix(),
		oidcMatrix(),
		buildMatrix(),
		searchMatrix(),
		registryMatrix(),
		registryBlobMatrix(),
		registryManifestMatrix(),
		registryReferrersMatrix(),
		registryListingMatrix(),
		partMatrixRows(),
	)
}

// buildMatrix covers the Builds API (#165, #166). Builds are the build resource
// on artifactory-build-info: reader reads them, writer also deploys, and
// member's group team holds read, deploy and delete. A token scoped to
// artifacts alone reaches no build.
func buildMatrix() []matrixRow {
	const (
		b    = "/artifactory/api/build"
		arts = "GET /artifactory/api/builds/buildArtifacts/{buildName}/{buildNumber}/{buildRepo}"
		bi   = types.BuildInfoRepository
	)
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	mine := published("b-{p}")
	return []matrixRow{
		at("GET "+b, "", anyone, 200),
		at("PUT "+b, buildDoc("new-{p}", "1", testStarted), holds(bi, "", write), 200),
		row("GET "+b+"/{buildName}", b+"/b-{p}", "", holds(bi, "", read), 200).seeding(mine),
		row(
			"GET "+b+"/{buildName}/{buildNumber}", b+"/b-{p}/1", "", holds(bi, "", read), 200,
		).seeding(mine),
		row(
			"POST "+b+"/append/{buildName}/{buildNumber}", b+"/append/b-{p}/1", `[{"id":"m"}]`,
			holds(bi, "", write|del), 204,
		).seeding(mine),
		row(
			"POST "+b+"/rename/{buildName}", b+"/rename/b-{p}?to=renamed-{p}", "",
			holds(bi, "", write|del), 200,
		).seeding(mine),
		row(
			"DELETE "+b+"/{buildName}", b+"/b-{p}?buildNumbers=1", "", holds(bi, "", del), 200,
		).seeding(mine),
		at(
			"POST "+b+"/delete",
			`{"buildName":"b-{p}","buildNumbers":["1"]}`,
			holds(bi, "", del),
			200,
		).seeding(mine),
		row(
			arts,
			"/artifactory/api/builds/buildArtifacts/b-{p}/1/"+bi,
			"",
			holds(bi, "", read),
			200,
		).seeding(mine),
		row(
			"POST "+b+"/promote/{buildName}/{buildNumber}", b+"/promote/b-{p}/1",
			`{"status":"released"}`, holds(bi, "", write), 200,
		).seeding(mine),
		row(
			"POST "+b+"/retention/{buildName}", b+"/retention/b-{p}?async=false", `{"count":"1"}`,
			holds(bi, "", del), 204,
		).seeding(mine),
	}
}

// oidcMatrix covers the OIDC integration (#83): providers and mappings are
// administrators' alone; the exchange answers anyone, and a request without
// a subject token is a bad request whoever makes it. The fixture seeded each
// principal's provider pv-{p} with its mapping m-{p}.
func oidcMatrix() []matrixRow {
	const (
		cfg  = "/access/api/v1/oidc"
		maps = cfg + "/{provider_name}/identity_mappings"
		one  = maps + "/{mapping_name}"
		mine = cfg + "/pv-{p}/identity_mappings"
		spec = `"token_spec":{"username":"ci","scope":"applied-permissions/user"}`
	)
	return []matrixRow{
		at("GET "+cfg, "", administrators, 200),
		at("POST "+cfg, `{"name":"np-{p}","issuer_url":"https://ci.example"}`, administrators, 200),
		row("GET "+cfg+"/{name}", cfg+"/pv-{p}", "", administrators, 200),
		row(
			"PUT "+cfg+"/{name}",
			cfg+"/pv-{p}",
			`{"name":"pv-{p}","issuer_url":"https://ci2.example"}`,
			administrators,
			200,
		),
		row("DELETE "+cfg+"/{name}", cfg+"/pv-{p}", "", administrators, 204),
		row("GET "+maps, mine, "", administrators, 200),
		row("POST "+maps, mine, `{"name":"nm-{p}",`+spec+`}`, administrators, 200),
		row("GET "+one, mine+"/m-{p}", "", administrators, 200),
		row("PUT "+one, mine+"/m-{p}", `{"name":"m-{p}",`+spec+`}`, administrators, 200),
		row("DELETE "+one, mine+"/m-{p}", "", administrators, 204),
		at("POST "+cfg+"/token", `{}`, noCredentials, 400),
	}
}

func systemMatrix() []matrixRow {
	return []matrixRow{
		at("GET /artifactory/api/system/ping", "", noCredentials, 200),
		at("GET /artifactory/api/system/version", "", anyone, 200),
		at("GET /artifactory/api/system/license", "", administrators, 200),
		at("GET /artifactory/api/system/service_id", "", administrators, 200),
		at("GET /artifactory/api/v1/system/readiness", "", noCredentials, 200),
		at("GET /artifactory/api/v1/system/liveness", "", noCredentials, 200),
		at("GET /access/api/v1/system/version", "", noCredentials, 200),
		at(
			"PATCH /artifactory/api/system/configuration",
			"localRepositories:\n  patch-{p}:\n    type: generic\n", administrators, 200,
		),
		at("GET /artifactory/api/v1/uploads/config", "", anyone, 200),
		at("POST /artifactory/api/system/usage", `{"features":[]}`, noCredentials, 200),
		at("POST /jfconnect/api/v1/backoffice/metrics/log", `{"metrics":[]}`, noCredentials, 200),
	}
}

func repositoryMatrix() []matrixRow {
	const (
		r  = "/artifactory/api/repositories"
		v2 = "/artifactory/api/v2/repositories"
	)
	return []matrixRow{
		at("GET "+r, "", anyone, 200),
		at("GET "+r+"/configurations", "", administrators, 200),
		row("GET "+r+"/existence", r+"/existence?type=local", "", administrators, 200),
		row(
			"GET "+r+"/{remoteRepoName}/configuration", r+"/remote-shared/configuration", "",
			anyone, 200,
		),
		row("GET "+r+"/{repoKey}", r+"/libs-local", "", sees("libs-local"), 200),
		row("GET "+r+"/{repoKey}", r+"/libs-b", "", sees("libs-b"), 200),
		// Covered by the seeded Any Remote alone, which grants no one.
		row("GET "+r+"/{repoKey}", r+"/remote-private", "", sees("remote-private"), 200),
		row("PUT "+r+"/{repoKey}", r+"/new-{p}", `{"rclass":"local"}`, administrators, 200),
		// A virtual repository without members (#394).
		row("PUT "+r+"/{repoKey}", r+"/nv-{p}", `{"rclass":"virtual"}`, administrators, 200),
		row("POST "+r+"/{repoKey}", r+"/libs-local", `{"description":"{p}"}`, administrators, 200),
		row("DELETE "+r+"/{repoKey}", r+"/r-{p}", "", administrators, 200),
		row("GET "+v2+"/{repoKey}", v2+"/libs-local", "", sees("libs-local"), 200),
		row("GET "+v2+"/batch", v2+"/batch?names=libs-local,libs-b", "", anyone, 200),
		at("PUT "+v2+"/batch", `[{"key":"nb-{p}","rclass":"local"}]`, administrators, 201),
		at("POST "+v2+"/batch", `[{"key":"libs-local","description":"{p}"}]`, administrators, 200),
		at("DELETE "+v2+"/batch", `["r-{p}"]`, administrators, 200),
	}
}

// contentMatrix covers items, copy and move, archives, AQL and multipart.
// writer's grant covers work/ only, so the fixture/ and libs-b rows show the
// path pattern and the repository boundary.
func contentMatrix() []matrixRow {
	const (
		put    = "PUT /artifactory/{repoKey}/{filePath}"
		get    = "GET /artifactory/{repoKey}/{filePath}"
		lib    = "/artifactory/libs-local/"
		cp     = "POST /artifactory/api/copy/{srcRepoKey}/{srcFilePath}"
		mv     = "POST /artifactory/api/move/{srcRepoKey}/{srcFilePath}"
		aql    = "POST /artifactory/api/search/aql"
		create = "POST " + uploadsCreate
	)
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	return []matrixRow{
		row(put, lib+"work/new-{p}.txt", "a", holds("libs-local", "work/", write), 201),
		row(put, lib+"other/new-{p}.txt", "a", holds("libs-local", "other/", write), 201),
		row(put, "/artifactory/libs-b/new-{p}.txt", "a", holds("libs-b", "", write), 201),
		// Checksum deploys into libs-local: of content libs-local's
		// work/get.txt holds, which every caller that may deploy there may
		// read; and of content only libs-b holds. Each caller gets a fixture
		// of its own for the second, or one caller's copy in libs-local would
		// make the content readable to the next.
		row(put, lib+"work/cs-{p}.bin", "checksum:"+sha1Hex("matrix"),
			holds("libs-local", "work/", write), 201),
		row(put, lib+"work/cs-{p}.bin", "checksum:"+sha1Hex("only in b"),
			reusing(holds("libs-local", "work/", write), holds("libs-b", "b/", read)), 201,
		).alone().seeding(onlyInB),
		row(get, lib+"work/get.txt", "", holds("libs-local", "work/", read), 200),
		row(get, lib+"fixture/get.txt", "", holds("libs-local", "fixture/", read), 200),
		row(get, "/artifactory/libs-b/b/get.txt", "", holds("libs-b", "b/", read), 200),
		row(
			"HEAD /artifactory/{repoKey}/{filePath}", lib+"work/get.txt", "",
			holds("libs-local", "work/", read), 200,
		),
		row(
			"DELETE /artifactory/{repoKey}/{filePath}", lib+"work/del-{p}.txt", "",
			holds("libs-local", "work/", del), 204,
		).seeding(deployed("work/del-{p}.txt")),
		row(
			cp, "/artifactory/api/copy/libs-local/work/get.txt?to=/libs-local/work/copy-{p}.txt",
			"", holds("libs-local", "work/", read|write), 200,
		),
		row(
			mv,
			"/artifactory/api/move/libs-local/work/move-{p}.txt?to=/libs-local/work/moved-{p}.txt",
			"", holds("libs-local", "work/", read|del|write), 200,
		).seeding(deployed("work/move-{p}.txt")),
		row(
			"GET /artifactory/api/archive/download/{repoKey}/{path}",
			"/artifactory/api/archive/download/libs-local/work?archiveType=zip", "",
			holds("libs-local", "work/", read), 200,
		),
		row(
			"GET /artifactory/{repoKey}/{archivePath}!/{entryPath}",
			lib+"work/archive.zip!/inner.txt", "", holds("libs-local", "work/", read), 200,
		),
		row(
			aql, "/artifactory/api/search/aql",
			`items.find({"repo":"libs-local"}).include("repo","path","name")`, aqlCaller, 200,
		),
		// jf rt s --offset's shape: .offset() beside "property".
		row(
			aql, "/artifactory/api/search/aql",
			`items.find({"repo":"libs-local"}).include("repo","path","name","property").offset(1)`,
			aqlCaller, 200,
		),
		row(
			create, uploadsCreate+"?repoKey=libs-local&repoPath=work/big-{p}.bin&partSizeMB=1", "",
			holds("libs-local", "work/", write), 200,
		),
		row("POST "+uploadsURLPart, uploadsURLPart+"?partNumber=1", "", uploadToken, 0),
		row(
			"PUT "+uploadsPart, uploadsPart+"?upload=x&partNumber=1&signature=y", "part",
			uploadToken, 0,
		),
		row("POST "+uploadsComplete, uploadsComplete+"?sha1=abc", "", uploadToken, 0),
		at("POST "+uploadsStatus, "", uploadToken, 0),
		at("POST "+uploadsAbort, "", uploadToken, 0),
	}
}

func storageMatrix() []matrixRow {
	const (
		get  = "GET /artifactory/api/storage/{repoKey}/{itemPath}"
		file = "/artifactory/api/storage/libs-local/work/get.txt"
		meta = "/artifactory/api/metadata/libs-local/work/get.txt"
		root = "/artifactory/api/storage/libs-local"

		rootMeta = "/artifactory/api/metadata/libs-local"
	)
	read, annotate := types.ActionRead, types.ActionAnnotate
	return []matrixRow{
		row(get, file, "", holds("libs-local", "work/", read), 200),
		// The effective permissions view is for administrators.
		row(get, file+"?permissions", "", administrators, 200),
		row(
			"PUT /artifactory/api/storage/{repoKey}/{itemPath}", file+"?properties=k={p}", "",
			holds("libs-local", "work/", annotate), 204,
		),
		row(
			"DELETE /artifactory/api/storage/{repoKey}/{itemPath}", file+"?properties=k", "",
			holds("libs-local", "work/", annotate), 204,
		),
		row(
			"PATCH /artifactory/api/metadata/{repoKey}/{itemPath}", meta, `{"props":{"k":"{p}"}}`,
			holds("libs-local", "work/", annotate), 204,
		),
		// The repository root's own properties need annotate on all of it.
		row(
			"PUT /artifactory/api/storage/{repoKey}/{itemPath}",
			root+"?properties=k={p}&recursive=0", "", holds("libs-local", "", annotate), 204,
		),
		row(
			"DELETE /artifactory/api/storage/{repoKey}/{itemPath}",
			root+"?properties=k&recursive=0",
			"",
			holds("libs-local", "", annotate),
			204,
		),
		row(
			"PATCH /artifactory/api/metadata/{repoKey}/{itemPath}",
			rootMeta+"?recursiveProperties=0", `{"props":{"k":"{p}"}}`,
			holds("libs-local", "", annotate), 204,
		),
		at("GET /artifactory/api/storageinfo", "", administrators, 200),
		at("POST /artifactory/api/storageinfo/calculate", "", administrators, 202),
		at("POST /artifactory/api/system/storage/optimize", "", administrators, 200),
		at("POST /artifactory/api/system/storage/gc", "", administrators, 200),
		row(
			"POST /artifactory/api/zap/{repoPath}", "/artifactory/api/zap/remote-private-cache", "",
			holdsEither("remote-private", "", types.ActionDelete, types.ActionManage), 200,
		),
	}
}

// trashViewer: a signed-in caller holding deploy or manage in some
// repository, whom 7.x shows the trash can (#439). Grants on
// artifactory-build-info are the build resource's, which shows nothing.
func trashViewer(p matrixPrincipal) int {
	if !p.anonymous && slices.ContainsFunc(p.grants, func(g matrixGrant) bool {
		return g.repo != types.BuildInfoRepository &&
			(g.actions.Has(types.ActionWrite) || g.actions.Has(types.ActionManage))
	}) {
		return 0
	}
	return refuse(p)
}

// trashMatrix covers the trash can.
func trashMatrix() []matrixRow {
	const (
		list = "GET /artifactory/api/storage/auto-trashcan"
		tc   = "/artifactory/api/storage/auto-trashcan"
	)
	write, manage := types.ActionWrite, types.ActionManage
	return []matrixRow{
		at(list, "", trashViewer, 200),
		// What was trashed from A shows to its deploy and manage holders,
		// and B's to no one but administrators (#439).
		row(
			list, tc+"/libs-local/work", "",
			holdsEither("libs-local", "work/", write, manage), 200,
		).seeding(trashed("libs-local", "work/listed-{p}.txt")),
		row(
			list, tc+"/libs-local/work/listed-file-{p}.txt", "",
			holdsEither("libs-local", "work/", write, manage), 200,
		).seeding(trashed("libs-local", "work/listed-file-{p}.txt")),
		row(
			list, tc+"/libs-b/work", "",
			holdsEither("libs-b", "work/", write, manage), 200,
		).seeding(trashed("libs-b", "work/b-listed-{p}.txt")),
		row(
			"DELETE /artifactory/api/trash/clean/{repoPath}",
			"/artifactory/api/trash/clean/libs-local/work/trash-{p}.txt", "",
			trashRule(holdsEither("libs-local", "work/", types.ActionDelete, types.ActionManage)),
			204,
		).seeding(trashed("libs-local", "work/trash-{p}.txt")),
		// Restoring needs manage on the destination and delete or manage
		// where the item was trashed from (#380).
		row(
			"POST /artifactory/api/trash/restore/{fromPath}",
			"/artifactory/api/trash/restore/libs-local/work/trash-{p}.txt?to=libs-local", "",
			trashRule(allOf(
				holds("libs-local", "", types.ActionManage),
				holdsEither("libs-local", "work/", types.ActionDelete, types.ActionManage),
			)), 202,
		).seeding(trashed("libs-local", "work/trash-{p}.txt")),
		// From B into A: tok-admin-repo manages A but holds nothing in B.
		row(
			"POST /artifactory/api/trash/restore/{fromPath}",
			"/artifactory/api/trash/restore/libs-b/work/b-trash-{p}.txt?to=libs-local", "",
			trashRule(allOf(
				holds("libs-local", "", types.ActionManage),
				holdsEither("libs-b", "work/", types.ActionDelete, types.ActionManage),
			)), 202,
		).seeding(trashed("libs-b", "work/b-trash-{p}.txt")),
		at("POST /artifactory/api/trash/empty", "", trashRule(administrators), 202),
	}
}

func keypairMatrix() []matrixRow {
	const (
		kp   = "/artifactory/api/security/keypair"
		cert = "/artifactory/api/system/security/certificates"
	)
	k := testKeys()
	return []matrixRow{
		at("GET "+kp, "", administrators, 200),
		at("POST "+kp, keypairBody("kp-new-{p}", "new-{p}", k.public), administrators, 201),
		at("PUT "+kp, keypairBody("kp-{p}", "renamed-{p}", k.public), administrators, 200),
		at("POST "+kp+"/verify", keypairBody("kp-{p}", "a-{p}", k.public), administrators, 200),
		row("GET "+kp+"/{keyPairName}", kp+"/kp-used", "", administrators, 200),
		row("DELETE "+kp+"/{keyPairName}", kp+"/kp-{p}", "", administrators, 200),
		// Package clients fetch the key that signs a repository they read.
		row(
			"GET "+kp+"/public/repositories/{repoKey}", kp+"/public/repositories/libs-local", "",
			sees("libs-local"), 200,
		),
		at("GET "+cert, "", administrators, 200),
		row(
			"POST "+cert+"/{certificateAlias}", cert+"/cert-new-{p}",
			k.bundle, administrators, 200,
		),
		row("DELETE "+cert+"/{certificateAlias}", cert+"/cert-{p}", "", administrators, 200),
	}
}

func roleMatrix() []matrixRow {
	const (
		envs  = "/access/api/v1/environments"
		roles = "/access/api/v1/roles"
	)
	return []matrixRow{
		at("GET "+envs, "", administrators, 200),
		at("POST "+envs, `{"name":"N-{p}"}`, administrators, 201),
		row(
			"POST "+envs+"/{environmentName}/rename", envs+"/E-{p}/rename", `{"new_name":"R-{p}"}`,
			administrators, 200,
		),
		row("DELETE "+envs+"/{environmentName}", envs+"/E-{p}", "", administrators, 204),
		at("GET "+roles, "", administrators, 200),
		at(
			"POST "+roles, `{"name":"nr-{p}","type":"CUSTOM_GLOBAL","actions":["READ_REPOSITORY"]}`,
			administrators, 201,
		),
		row("GET "+roles+"/{role}", roles+"/Viewer", "", administrators, 200),
		row(
			"PUT "+roles+"/{role}", roles+"/role-{p}",
			`{"name":"role-{p}","type":"CUSTOM_GLOBAL","actions":["READ_BUILD"]}`, administrators,
			200,
		),
		row("DELETE "+roles+"/{role}", roles+"/role-{p}", "", administrators, 204),
	}
}

// permissionMatrix covers the three permission APIs. No principal but the
// administrators holds MANAGE, so the v2 views that also answer a manager
// list nothing for the others, and the details refuse them.
func permissionMatrix() []matrixRow {
	const (
		v2Body = `{"repo":{"repositories":["perm-local"],"actions":{"users":{"v-{p}":["read"]}}}}`
		v2Self = `{"repo":{"repositories":["perm-local"],"actions":{"users":{` +
			`"v-{p}":["read"],"{u}":["read"]}}}}`
		access = `{"name":"np-{p}","resources":{"artifact":{"actions":{"users":{"v-{p}":["READ"]}},` +
			`"targets":{"perm-local":{"include_patterns":["**"]}}}}}`
		section = `{"actions":{"users":{"v-{p}":["READ"]}},"targets":{"perm-local":{}}}`
	)
	return []matrixRow{
		at("GET "+permV1, "", administrators, 200),
		row("GET "+permV1+"/{permissionTargetName}", permV1+"/Anything", "", administrators, 200),
		row(
			"PUT "+permV1+"/{permissionTargetName}", permV1+"/np-{p}",
			`{"repositories":["perm-local"],"principals":{"users":{"v-{p}":["r"]}}}`,
			administrators, 200,
		),
		row(
			"DELETE "+permV1+"/{permissionTargetName}", permV1+"/perm-{p}",
			"", administrators, 200,
		),

		at("GET "+permV2, "", signedIn, 200),
		row("GET "+permV2+"/groups/{groupName}", permV2+"/groups/team", "", signedIn, 200),
		row("GET "+permV2+"/users/{userName}", permV2+"/users/v-{p}", "", signedIn, 200),
		row("GET "+permV2+"/{permissionTargetName}", permV2+"/Anything", "", administrators, 200),
		row("HEAD "+permV2+"/{permissionTargetName}", permV2+"/Anything", "", administrators, 200),
		row(
			"POST "+permV2+"/{permissionTargetName}", permV2+"/np-{p}",
			v2Body, administrators, 201,
		),
		row(
			"PUT "+permV2+"/{permissionTargetName}", permV2+"/perm-{p}", v2Body, administrators,
			200,
		),
		// A caller granting itself: an administrator may change its own
		// entry, which a holder of MANAGE may not (decision 0006).
		row(
			"PUT "+permV2+"/{permissionTargetName}", permV2+"/perm-{p}", v2Self, administrators,
			200,
		),
		row(
			"DELETE "+permV2+"/{permissionTargetName}", permV2+"/perm-{p}",
			"", administrators, 200,
		),

		at("GET "+permAcc, "", administrators, 200),
		at("POST "+permAcc, access, administrators, 201),
		row("GET "+permAcc+"/{permissionName}", permAcc+"/Anything", "", administrators, 200),
		row("DELETE "+permAcc+"/{permissionName}", permAcc+"/perm-{p}", "", administrators, 204),
		row(
			"GET "+permAcc+"/{permissionName}/{resourceType}", permAcc+"/Anything/artifact", "",
			administrators, 200,
		),
		row(
			"PUT "+permAcc+"/{permissionName}/{resourceType}", permAcc+"/perm-{p}/artifact",
			section, administrators, 200,
		),
		row(
			"PATCH "+permAcc+"/{permissionName}/{resourceType}", permAcc+"/perm-{p}/artifact",
			section, administrators, 200,
		),
		row(
			"DELETE "+permAcc+"/{permissionName}/{resourceType}", permAcc+"/perm-{p}/artifact", "",
			administrators, 204,
		),
	}
}

// tokenMatrix covers access tokens. The fixture seeded t-{p}, a token record
// of the principal's own user, and o-{p}, one of its victim v-{p}.
func tokenMatrix() []matrixRow {
	const (
		tokens = "/access/api/v1/tokens"
		expiry = "/access/api/v1/config/token/default_expiry"
		legacy = "/artifactory/api/security/token"
	)
	return []matrixRow{
		at("POST "+tokens, `{}`, accessMinter, 200),
		at("GET "+tokens, "", signedIn, 200),
		at("DELETE "+tokens+"/revoke", `{"token":"{token}"}`, administrators, 200),
		row("GET "+tokens+"/{tokenId}", tokens+"/t-{p}", "", signedIn, 200),
		row("GET "+tokens+"/{tokenId}", tokens+"/o-{p}", "", administrators, 200),
		row("DELETE "+tokens+"/{tokenId}", tokens+"/t-{p}", "", signedIn, 200),
		row("DELETE "+tokens+"/{tokenId}", tokens+"/o-{p}", "", administrators, 200),
		at("GET "+expiry, "", administrators, 200),
		at("PUT "+expiry, `{"default_token_expiration":7200}`, administrators, 200),
		// The legacy list and revoke are admin only, even for the caller's
		// own token (#382).
		at("GET "+legacy, "", administrators, 200),
		at("POST "+legacy, "form:username={u}", wholeIdentity, 200),
		at("POST "+legacy+"/revoke", "form:token_id=t-{p}", administrators, 200),
		at("POST "+legacy+"/revoke", "form:token_id=o-{p}", administrators, 200),
		at("GET /artifactory/api/security/encryptedPassword", "", passwordLogin, 200),
	}
}

// securityMatrix covers the legacy security API, administrators' alone but
// for a caller's own API key, now retired.
func securityMatrix() []matrixRow {
	const newUser = `{"name":"n-{p}","email":"n@example.com","password":"Matrix-pass-1"}`
	return []matrixRow{
		at("GET "+secUsers, "", administrators, 200),
		row("GET "+secUsers+"/{userName}", secUsers+"/v-{p}", "", administrators, 200),
		row("PUT "+secUsers+"/{userName}", secUsers+"/n-{p}", newUser, administrators, 201),
		row(
			"POST "+secUsers+"/{userName}", secUsers+"/v-{p}", `{"email":"t@example.com"}`,
			administrators, 200,
		),
		row("DELETE "+secUsers+"/{userName}", secUsers+"/v-{p}", "", administrators, 200),
		at("GET "+secGroups, "", administrators, 200),
		row("GET "+secGroups+"/{groupName}", secGroups+"/team", "", administrators, 200),
		row(
			"PUT "+secGroups+"/{groupName}", secGroups+"/ng-{p}", `{"name":"ng-{p}"}`,
			administrators, 201,
		),
		row(
			"POST "+secGroups+"/{groupName}", secGroups+"/g-{p}", `{"description":"d"}`,
			administrators, 200,
		),
		row("DELETE "+secGroups+"/{groupName}", secGroups+"/g-{p}", "", administrators, 200),
		at("GET /artifactory/api/security/lockedUsers", "", administrators, 200),
		at("POST /artifactory/api/security/unlockAllUsers", "", administrators, 200),
		at("POST /artifactory/api/security/unlockUsers", `["v-{p}"]`, administrators, 200),
		row(
			"POST /artifactory/api/security/unlockUsers/{userName}",
			"/artifactory/api/security/unlockUsers/v-{p}", "", administrators, 200,
		),
		at("GET "+secLockPol, "", administrators, 200),
		at("PUT "+secLockPol, `{"enabled":"false","loginAttempts":"3"}`, administrators, 200),
		at("GET "+secExpiry, "", administrators, 200),
		at("PUT "+secExpiry, `{"enabled":"false","passwordMaxAge":"30"}`, administrators, 200),
		at("POST "+secExpire, `["v-{p}"]`, administrators, 200),
		row("POST "+secUnexpire+"{userName}", secUnexpire+"v-{p}", "", administrators, 200),
		// API keys are retired: creating one is refused, and there is none
		// to read; revoking one's own succeeds.
		at("POST "+secAPIKey, "", signedIn, 400),
		at("PUT "+secAPIKey, "", signedIn, 400),
		at("GET "+secAPIKey, "", signedIn, 404),
		at("DELETE "+secAPIKey, "", signedIn, 200),
		row("DELETE "+secAPIKey+"/{username}", secAPIKey+"/v-{p}", "", administrators, 200),
	}
}

func accessIdentityMatrix() []matrixRow {
	const (
		users   = "/access/api/v2/users"
		groups  = "/access/api/v2/groups"
		basicSw = "/access/api/v1/config/security/authentication/basic_authentication_enabled"
		policy  = "/access/api/v1/config/security/password_policy"
		lockPol = "/access/api/v1/config/security/user_lock_policy"
		newUser = `{"username":"n-{p}","email":"n@example.com","password":"Matrix-pass-1"}`
	)
	return []matrixRow{
		at("GET "+users, "", administrators, 200),
		at("POST "+users, newUser, administrators, 201),
		// Expiring every password locks out every later principal.
		at("POST "+users+"/expire_password_for_all_users", "", administrators, 200).alone(),
		at("POST "+users+"/unexpire_password_for_all_users", "", administrators, 200),
		row("GET "+users+"/{username}", users+"/{u}", "", ownRecord(signedIn), 200),
		row("GET "+users+"/{username}", users+"/v-{p}", "", administrators, 200),
		row(
			"PATCH "+users+"/{username}", users+"/v-{p}", `{"email":"new@example.com"}`,
			administrators, 200,
		),
		row("DELETE "+users+"/{username}", users+"/v-{p}", "", administrators, 204),
		row(
			"PATCH "+users+"/{username}/groups", users+"/v-{p}/groups", `{"add":["g-{p}"]}`,
			administrators, 200,
		),
		// Setting one's own password turns any credential into a password.
		row(
			"PUT "+users+"/{username}/password", users+"/{u}/password",
			`{"password":"Matrix-{p}-1"}`, ownRecord(wholeIdentity), 204,
		),
		row(
			"PUT "+users+"/{username}/password", users+"/v-{p}/password",
			`{"password":"Matrix-{p}-2"}`, administrators, 204,
		),
		row(
			"POST "+users+"/{username}/password/expire", users+"/v-{p}/password/expire", "",
			administrators, 204,
		),
		row("POST "+users+"/{username}/unlock", users+"/v-{p}/unlock", "", administrators, 204),
		at("GET "+groups, "", administrators, 200),
		at("POST "+groups, `{"name":"ng-{p}"}`, administrators, 201),
		row("GET "+groups+"/{name}", groups+"/team", "", administrators, 200),
		row("PATCH "+groups+"/{name}", groups+"/g-{p}", `{"description":"d"}`, administrators, 200),
		row("DELETE "+groups+"/{name}", groups+"/g-{p}", "", administrators, 204),
		row(
			"PATCH "+groups+"/{name}/members", groups+"/g-{p}/members", `{"add":["v-{p}"]}`,
			administrators, 200,
		),
		at("GET "+basicSw, "", administrators, 200),
		at("PUT "+basicSw, `{"enabled":true}`, administrators, 200),
		at("PUT "+policy, `{}`, administrators, 204),
		at("PATCH "+policy, `{}`, administrators, 204),
		at("GET "+lockPol, "", administrators, 200),
		// Everything off, as the defaults are, so later principals are
		// neither locked nor expired.
		at("PUT "+lockPol, `{}`, administrators, 200),
	}
}

func TestRoutePrincipalMatrix(t *testing.T) {
	for _, r := range matrixTable() {
		t.Run(r.route, func(t *testing.T) {
			t.Parallel()
			var f *matrixFixture
			for _, p := range matrixPrincipals {
				if f == nil || r.fresh {
					f = newMatrixFixture(t)
					for _, q := range matrixPrincipals {
						if r.seed != nil {
							r.seed(t, f, q.name)
						}
					}
				}
				if got, body := f.call(t, r, p); got != r.want(p) {
					t.Errorf("%s %s as %s: status %d, want %d: %s",
						r.method(), r.path, p.name, got, r.want(p), body)
				}
			}
		})
	}
}

func (r matrixRow) method() string { m, _, _ := strings.Cut(r.route, " "); return m }

// TestMatrixCoversImplemented is the matrix's guard, as
// TestStatusTableCoversImplemented is the status table's.
func TestMatrixCoversImplemented(t *testing.T) {
	covered := map[string]bool{}
	for _, r := range matrixTable() {
		covered[r.route] = true
	}
	for route := range implementedHandwritten {
		if !covered[route] {
			t.Errorf("%s is implemented but has no row in the route × principal matrix", route)
		}
	}
	for route := range implemented {
		if !covered[route] {
			t.Errorf("%s is implemented but has no row in the route × principal matrix", route)
		}
	}
}

// matrixFixture is a seeded server for one row: the principals' users,
// groups, grants and tokens, and for each principal name p the things the
// state-changing rows act on, so that no principal's request changes what
// another's finds.
type matrixFixture struct {
	h     http.Handler
	db    store.Store
	items *item.Service
	// secrets holds each token principal's Bearer token, and under
	// "v-"+p an access token of v-{p}.
	secrets map[string]string
	// uploads holds, by principal name, a registry upload session its user
	// opened, a row's {upload}.
	uploads map[string]string
}

// call makes r's request as p, with anonymous access on or off as p needs.
func (f *matrixFixture) call(t *testing.T, r matrixRow, p matrixPrincipal) (int, string) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.Settings().Put(ctx, types.SettingAnonymousAccess, p.anonymousOn); err != nil {
		t.Fatal(err)
	}
	fill := strings.NewReplacer(
		"{p}", p.name, "{u}", p.user, "{token}", f.secrets["v-"+p.name],
		"{upload}", f.uploads[p.name],
	)
	headers := map[string]string{}
	switch {
	case p.token:
		headers = bearerAuth(f.secrets[p.name])
	case !p.anonymous:
		headers = basic(p.user, "pw")
	}
	headers, body := checksumDeploy(headers, fill.Replace(r.body))
	if body != "" {
		headers["Content-Type"], body = contentTypeOf(body)
	}
	rec := do(t, f.h, r.method(), fill.Replace(r.path), headers, body)
	return rec.Code, rec.Body.String()
}

// deploy deploys body at path in the repository key and returns its
// configuration.
func (f *matrixFixture) deploy(t *testing.T, key, path string, body []byte) repo.Config {
	t.Helper()
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	seedKeys(t, db)
	names := make([]string, 0, len(matrixPrincipals))
	for _, p := range matrixPrincipals {
		names = append(names, p.name)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{"nobody", "reader", "writer", "member", "gadmin"} {
		must(db.Users().Create(ctx, types.User{Name: u, PasswordHash: cheapHash}))
	}
	for _, p := range names {
		must(db.Users().Create(ctx, types.User{Name: "v-" + p, PasswordHash: cheapHash}))
		must(db.Groups().Create(ctx, types.Group{Name: "g-" + p}))
	}
	must(db.Groups().Create(ctx, types.Group{Name: "team", Members: []string{"member"}}))
	must(db.Groups().Create(ctx, types.Group{
		Name: "ops", AdminPrivileges: true, Members: []string{"gadmin"},
	}))

	shared := true
	repos := []repo.Config{
		{Key: "libs-local", Class: repo.Local, PrimaryKeyPairRef: "kp-used"},
		{Key: "libs-b", Class: repo.Local},
		{Key: "perm-local", Class: repo.Local},
		{
			Key:                "remote-shared",
			Class:              repo.Remote,
			URL:                "https://up.example",
			ShareConfiguration: &shared,
		},
		{Key: "remote-private", Class: repo.Remote, URL: "https://up.example"},
	}
	for _, p := range names {
		repos = append(repos, repo.Config{Key: "r-" + p, Class: repo.Local})
	}
	repos = append(repos, matrixRegistryRepos()...)
	repos = append(repos, formatRepos()...)
	for _, c := range repos {
		must(db.Repos().Create(ctx, *repo.Defaults(&c)))
	}

	grant := func(name string, g types.Grants, target string, include ...string) {
		must(db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: g, Targets: map[string]types.Target{target: {Include: include}},
			}},
		}))
	}
	users := func(name string, a types.Actions) types.Grants {
		return types.Grants{Users: map[string]types.Actions{name: a}}
	}
	// The permissions behind matrixPrincipals' grants.
	grant("matrix-reader", users("reader", types.ActionRead), "libs-local", "**")
	grant("matrix-writer", users("writer", rwd), "libs-local", "work/**")
	grant(
		"matrix-team",
		types.Grants{Groups: map[string]types.Actions{"team": rawd}},
		"libs-local",
		"**",
	)
	grant("matrix-member", users("member", types.ActionRead), "libs-b", "**")
	for _, p := range names {
		grant("perm-"+p, users("v-"+p, types.ActionRead), "perm-local", "**")
	}
	// The registry's repositories have permission targets of their own, so
	// the grants above stay as they are.
	grant("matrix-docker-reader", users("reader", types.ActionRead), dockerA, "**")
	grant("matrix-docker-writer", users("writer", rwd), dockerA, "work/**")
	grant(
		"matrix-docker-team",
		types.Grants{Groups: map[string]types.Actions{"team": rawd}},
		dockerA,
		"**",
	)
	// The formats' repositories have one permission per principal, so
	// their grants follow the same pattern as libs-local's.
	formatGrant := func(name string, g types.Grants, under string) {
		must(db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: g, Targets: formatTargets(under),
			}},
		}))
	}
	formatGrant("matrix-formats-reader", users("reader", types.ActionRead), "")
	formatGrant("matrix-formats-writer", users("writer", rwd), "work/")
	formatGrant(
		"matrix-formats-team",
		types.Grants{Groups: map[string]types.Actions{"team": rawd}},
		"",
	)
	buildGrant := func(name string, g types.Grants) {
		must(db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {
				Actions: g, Targets: map[string]types.Target{
					types.BuildInfoRepository: {Include: []string{"**"}},
				},
			}},
		}))
	}
	buildGrant("matrix-build-reader", users("reader", types.ActionRead))
	buildGrant("matrix-build-writer", users("writer", types.ActionRead|types.ActionWrite))
	buildGrant("matrix-build-team", types.Grants{Groups: map[string]types.Actions{"team": rwd}})

	blobs, err := fs.New(t.TempDir())
	must(err)
	f := &matrixFixture{
		db: db, items: &item.Service{Store: db, Blobs: blobs}, uploads: map[string]string{},
	}
	f.deploy(t, "libs-local", "work/get.txt", []byte("matrix"))
	f.deploy(t, "libs-local", "fixture/get.txt", []byte("matrix"))
	f.deploy(
		t,
		"libs-local",
		"work/archive.zip",
		zipBytes(t, map[string]string{"inner.txt": "inner"}),
	)
	f.deploy(t, "libs-b", "b/get.txt", []byte("matrix"))
	seedMatrixRegistry(t, f)
	seedMatrixManifests(t, f)
	for _, p := range matrixParts {
		if p.seed != nil {
			p.seed(t, f)
		}
	}
	k := testKeys()
	rs := roles.Service{Store: db}
	for _, p := range names {
		must(db.Keys().CreateKeypair(ctx, types.Keypair{
			Name: "kp-" + p, Type: types.KeypairRSA, Alias: "a-" + p,
			PrivateKey: k.private, PublicKey: k.public,
		}))
		must(db.Keys().PutCertificate(ctx, types.Certificate{Alias: "cert-" + p, PEM: k.bundle}))
		must(rs.CreateEnvironment(ctx, "E-"+p))
		must(rs.CreateRole(ctx, types.Role{
			Name: "role-" + p, Type: types.RoleCustomGlobal, Actions: []string{"READ_REPOSITORY"},
		}))
	}
	must(
		db.Settings().
			Put(ctx, types.SettingFolderDownload, types.FolderDownloadSettings{Enabled: true}),
	)
	idp := oidc.Service{Store: db}
	for _, p := range names {
		name, issuer, mapping, user, scope := "pv-"+p, "https://ci.example", "m-"+p, "ci",
			"applied-permissions/user"
		_, err := idp.CreateProvider(ctx, oidc.Provider{Name: &name, IssuerURL: &issuer})
		must(err)
		_, err = idp.CreateMapping(ctx, name, oidc.Mapping{
			Name: &mapping, TokenSpec: &oidc.TokenSpec{Username: &user, Scope: &scope},
		})
		must(err)
	}

	tokens, secrets := seedMatrixTokens(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	must(err)
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens:    tokens,
		Multipart: &multipart.Service{Store: db, Blobs: blobs, Dir: t.TempDir()},
	}
	for _, p := range matrixParts {
		if p.tune != nil {
			p.tune(&cfg)
		}
	}
	f.h = New(cfg)
	f.secrets = secrets
	return f
}

// seedMatrixTokens issues the token principals' tokens, and for each
// principal an access token of v-{p} and the token records t-{p}, its own
// user's, and o-{p}, v-{p}'s.
func seedMatrixTokens(t *testing.T, db store.Store) (*token.Service, map[string]string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}))
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	must(err)
	// A service an hour and a half behind, over the same key, issues
	// hour-long tokens that have expired by now.
	past, err := token.New(ctx, db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-90 * time.Minute) },
	})
	must(err)
	issue := func(svc *token.Service, user, scope string, id types.TokenIdentity) token.Issued {
		t.Helper()
		s, err := types.ParseScope(scope)
		must(err)
		hour := time.Hour
		issued, err := svc.Issue(ctx, token.Request{
			Username: user, Scope: s, ExpiresIn: &hour, ByAdmin: true, ForceRevocable: true,
			Identity: id,
		})
		must(err)
		return issued
	}
	secrets := map[string]string{}
	now := time.Now().Truncate(time.Second)
	for _, p := range matrixPrincipals {
		switch {
		case p.name == "tok-expired":
			secrets[p.name] = issue(past, p.user, p.scope, p.identity).AccessToken
		case p.registry:
			issued, err := tokens.IssueRegistry(ctx, token.RegistryRequest{
				Username: p.user, ExpiresIn: time.Hour, Identity: p.identity,
			})
			must(err)
			secrets[p.name] = issued.AccessToken
		case p.name == "tok-revoked":
			revoked := issue(tokens, p.user, p.scope, p.identity)
			must(tokens.Revoke(ctx, revoked.Record.ID))
			secrets[p.name] = revoked.AccessToken
		case p.token:
			secrets[p.name] = issue(tokens, p.user, p.scope, p.identity).AccessToken
		}
		secrets["v-"+p.name] = issue(
			tokens, "v-"+p.name, "applied-permissions/user", types.IdentityStored,
		).AccessToken
		for id, user := range map[string]string{"t-" + p.name: p.user, "o-" + p.name: "v-" + p.name} {
			must(db.Tokens().Create(ctx, types.Token{
				ID: id, Subject: subjectOf(tokens, user), Revocable: true,
				Scope: types.UserScope(), IssuedAt: now, AccessDigest: []byte(id),
			}))
		}
	}
	return tokens, secrets
}
