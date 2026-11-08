package maven_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const dockerA = "docker-a"

func formatGrants(under string, actions types.Actions) []matrixGrant {
	return []matrixGrant{
		{"maven-local", under, actions},
		{"maven-remote", under, actions},
		{"maven-virtual", under, actions},
		{"maven-releases", under, actions},
	}
}

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
	admin bool
	// resources: holds the Manage Resources role, which manages every
	// permission and reaches nothing else (decision 0006).
	resources bool
	grants    []matrixGrant
	// excludes are the path suffixes its grants leave out, as a permission
	// target's exclude pattern **/*<suffix> does.
	excludes []string
	// system: the system areas a token scope lets it read
	// (system:<area>:r), which grant no content.
	system []string
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
	// Manage Resources holders, through the user's flag and through a
	// group's: they manage every permission, and hold no content and no
	// other admin route.
	{name: "resmgr", user: "resmgr", resources: true},
	{name: "gresmgr", user: "gresmgr", resources: true},
	// resmgr's token scoped to A, where it holds nothing: a scope of
	// resource entries alone keeps no role.
	{
		name: "tok-resmgr-repo", user: "resmgr", token: true, scope: "artifact:libs-local:*",
		narrowed: true,
	},
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
	// charter reads, deploys and deletes Helm charts in helm-local but not
	// their .prov files, which its permission excludes (helmMatrixPart's
	// seed): a chart's delete, copy or move that takes its .prov along is
	// refused it (decision 0039).
	{
		name: "charter", user: "charter", grants: []matrixGrant{{"helm-local", "", rwd}},
		excludes: []string{".prov"},
	},
	// deployer reads and deploys under A's work/ but may not delete there:
	// the CI user whose jf rt u, run again, replaces files with their own
	// content (#400).
	{name: "deployer", user: "deployer", grants: []matrixGrant{
		{"libs-local", "work/", types.ActionRead | types.ActionWrite},
	}},
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
	// member's token scoped to the storage summary alone, which 7.x lets
	// read it (getstoragesummaryinfo): no content and no other system area.
	{
		name: "tok-storage", user: "member", token: true, scope: "system:info/storage:r",
		narrowed: true, system: []string{"info/storage"},
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
	// A transient user from an OIDC exchange whose mapping names it and
	// member's group: it acts through team, as tok-transient does, but its
	// scope keeps its identity, so it mints tokens that act as it does
	// (#456).
	{
		name: "tok-transient-user", user: "ci-user", token: true,
		scope:    "applied-permissions/groups:team applied-permissions/user",
		identity: types.IdentityTransient, grants: slices.Concat([]matrixGrant{
			{"libs-local", "", rawd}, {types.BuildInfoRepository, "", rwd}, {dockerA, "", rawd},
		}, formatGrants("", rawd)),
	},
}

func (p matrixPrincipal) holds(repoKey, path string, want types.Actions) bool {
	if slices.ContainsFunc(p.excludes, func(s string) bool { return strings.HasSuffix(path, s) }) {
		return false
	}
	var have types.Actions
	for _, g := range p.grants {
		if (g.repo == "*" || g.repo == repoKey) && strings.HasPrefix(path, g.under) {
			have |= g.actions
		}
	}
	return have.Has(want)
}

type rule func(matrixPrincipal) int

func refuse(p matrixPrincipal) int {
	if p.anonymous {
		return http.StatusUnauthorized
	}
	return http.StatusForbidden
}

func anyone(p matrixPrincipal) int {
	if p.anonymous && !p.anonymousOn {
		return http.StatusUnauthorized
	}
	return 0
}

func holds(repoKey, path string, want types.Actions) rule {
	return func(p matrixPrincipal) int {
		if p.holds(repoKey, path, want) {
			return 0
		}
		return refuse(p)
	}
}

type matrixPart struct {
	seed func(*testing.T, *matrixFixture)
	rows func() []matrixRow
}

type matrixRow struct {
	route, path, body string
	who               rule
	ok                int
	fresh             bool
	seed              func(*testing.T, *matrixFixture, string)
}

func row(route, path, body string, who rule, ok int) matrixRow {
	return matrixRow{route: route, path: path, body: body, who: who, ok: ok}
}
func (r matrixRow) alone() matrixRow { r.fresh = true; return r }
func (r matrixRow) seeding(f func(*testing.T, *matrixFixture, string)) matrixRow {
	r.seed = f
	return r
}
func (r matrixRow) method() string { m, _, _ := strings.Cut(r.route, " "); return m }
func (r matrixRow) want(p matrixPrincipal) int {
	if p.bad || p.registry {
		return http.StatusUnauthorized
	}
	if status := r.who(p); status != 0 {
		return status
	}
	return r.ok
}
