// Package authz evaluates Artifactory's permission model (decision 0006):
// the admin flag, the union of grants to a user and their groups, permission
// targets with Ant include and exclude patterns, the anonymous user, and
// token scopes, which only ever remove actions. It is a library: handlers
// build a Decider per request and ask it questions.
package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Subject is who a request acts as. Groups are the groups the subject
// belongs to; the token service (#74) may instead put a token's scoped
// groups here when it mints a token for a subject that is not a user.
type Subject struct {
	Name      string
	Groups    []string
	Admin     bool
	Anonymous bool
	// Scope is the scope of the token the request carries; nil for password
	// authentication, which acts with everything the subject holds.
	Scope *types.Scope
}

// Sources is what a Decider reads once per request.
type Sources struct {
	Permissions store.PermissionStore
	Groups      store.GroupStore
	// AnonymousEnabled is the security.anonymous_access setting; the
	// anonymous user's grants apply only while it is on.
	AnonymousEnabled bool
}

// Repo identifies a permission target: a repository's key and class (local,
// remote, virtual, federated, distribution), or the fixed build-info or
// release-bundle repository of those resources. "<remote>-cache" is
// accepted for a remote's cache and resolves to the remote.
type Repo struct {
	Key   string
	Class string
}

type rule struct {
	target  string
	include []Pattern
	exclude []Pattern
	actions types.Actions
}

// Decider answers permission questions for one subject from one snapshot
// of the permissions.
type Decider struct {
	// admin: the subject acts as an administrator (admin-only endpoints).
	// every: the subject holds every action on content, which an admin keeps
	// under a token scoped to resources while losing admin endpoints.
	admin bool
	every bool
	rules map[types.ResourceType][]rule
	// open is false when a token scope grants no applied permissions
	// (resource scopes only), so only its artifact entries can grant.
	open bool
	// filter, when set, limits artifact actions to what the scope's
	// artifact entries allow; builds, when set, limits build actions to
	// what its build entries allow.
	filter []types.ScopeEntry
	builds []types.ScopeEntry
	scope  *types.Scope
	// user and groups are the identities the subject's grants are read
	// for, after a token scope narrowed them: user is empty when the scope
	// does not act as the user, and groups are those it acts through.
	user   string
	groups []string
}

// New reads the permissions that apply to s and returns its Decider.
func New(ctx context.Context, src Sources, s Subject) (*Decider, error) {
	if s.Anonymous && !src.AnonymousEnabled {
		return &Decider{}, nil
	}
	sc := analyse(s.Scope)
	d := &Decider{open: sc.open, scope: s.Scope, rules: map[types.ResourceType][]rule{}}
	if sc.filtered {
		d.filter = sc.artifact
		if d.filter == nil {
			// Resource entries alone, none of them for artifacts: a build
			// or repo scope reaches no artifact.
			d.filter = []types.ScopeEntry{}
		}
	}
	d.builds = sc.build
	groups := sc.groups(s.Groups)
	d.groups = groups
	user := ""
	if sc.user {
		user = s.Name
	}
	viaGroup, err := groupAdmin(ctx, src.Groups, groups)
	if err != nil {
		return nil, err
	}
	d.user = user
	d.every = (sc.user && s.Admin) || viaGroup
	d.admin = sc.open && ((s.Admin && sc.adminAllowed) || viaGroup)
	perms, err := src.Permissions.ForPrincipal(ctx, user, groups)
	if err != nil {
		return nil, fmt.Errorf("authz: permissions of %s: %w", s.Name, err)
	}
	for _, p := range perms {
		for rt, res := range p.Resources {
			held := heldBy(res.Actions, user, groups)
			if held == 0 {
				continue
			}
			for target, t := range res.Targets {
				d.rules[rt] = append(d.rules[rt], rule{
					target:  target,
					include: compileAll(t.Include, "**"),
					exclude: compileAll(t.Exclude, ""),
					actions: held,
				})
			}
		}
	}
	return d, nil
}

// Admin reports whether the subject acts as an administrator.
func (d *Decider) Admin() bool { return d.admin }

// Groups returns the groups the subject acts through: all of its groups, or
// those a token scope names.
func (d *Decider) Groups() []string { return slices.Clone(d.groups) }

// RepoVisible reports whether the subject may see repo in a listing and read
// its configuration (decision 0006). An administrator sees everything. A
// token scope with repo entries sees exactly the repositories they name.
// Otherwise the subject sees a repository it may read anywhere in, and every
// repository through the system:info:r scope.
func (d *Decider) RepoVisible(repo Repo) bool {
	if d.admin {
		return true
	}
	if d.scope != nil {
		for _, e := range d.scope.Entries {
			if e.Kind == types.ScopeRepo {
				return d.RepoConfig(repo.Key)
			}
		}
	}
	return d.AllowsFolder(types.ResourceArtifact, repo, "", types.ActionRead) ||
		d.System("info", types.ScopeRead)
}

// Holds reports whether g grants every action in want to the subject,
// directly or through a group it acts as. It answers questions about a
// permission document itself (who may manage it), not about content.
func (d *Decider) Holds(g types.Grants, want types.Actions) bool {
	return heldBy(g, d.user, d.groups).Has(want)
}

// Grantable returns the actions the subject may grant or revoke in a
// permission section whose grants are g. An administrator may change any.
// A holder of MANAGE in g may change only the other actions it holds in g:
// Artifactory 7.x's default exclude-manage rule, without which a holder
// could hand out rights it lacks, or appoint and remove other managers
// (decision 0006). Anyone else may change none, so a holder of MANAGE in
// one section changes nothing in another.
func (d *Decider) Grantable(g types.Grants) types.Actions {
	if d.admin {
		return allActions
	}
	held := heldBy(g, d.user, d.groups)
	if !held.Has(types.ActionManage) {
		return 0
	}
	return held &^ types.ActionManage
}

// Actions returns every action the subject holds on path in repo's
// resource. folder is true for a folder, which is also visible when a
// permitted path lies below it.
func (d *Decider) Actions(
	rt types.ResourceType,
	repo Repo,
	path string,
	folder bool,
) types.Actions {
	var have types.Actions
	if d.every {
		have = allActions
	} else {
		for _, r := range d.rules[rt] {
			if covers(r.target, repo) && r.allows(path, folder) {
				have |= r.actions
			}
		}
	}
	switch {
	case rt == types.ResourceArtifact && d.filter != nil:
		have &= scopeActions(d.filter, repo, path, folder)
	case rt == types.ResourceBuild && d.builds != nil:
		have &= scopeActions(d.builds, repo, path, folder)
	case rt != types.ResourceArtifact && !d.open:
		have = 0
	}
	return have
}

// Allows reports whether the subject holds every action in want on a file
// at path.
func (d *Decider) Allows(rt types.ResourceType, repo Repo, path string, want types.Actions) bool {
	return want != 0 && d.Actions(rt, repo, path, false).Has(want)
}

// AllowsFolder is Allows for a folder.
func (d *Decider) AllowsFolder(
	rt types.ResourceType,
	repo Repo,
	path string,
	want types.Actions,
) bool {
	return want != 0 && d.Actions(rt, repo, path, true).Has(want)
}

// RepoConfig reports whether a token scope lets the subject read a
// repository's configuration. Without a scope, or with applied permissions
// and no repo entries, the permissions alone decide; repo entries
// (repo:<key>:r) narrow that to the repositories they name.
func (d *Decider) RepoConfig(key string) bool {
	if d.admin || d.scope == nil {
		return true
	}
	var named bool
	for _, e := range d.scope.Entries {
		if e.Kind != types.ScopeRepo {
			continue
		}
		named = true
		if (e.Target == "*" || e.Target == key) && e.Actions.Covers(types.ScopeRead) {
			return true
		}
	}
	return !named && d.open
}

// System reports whether the subject may use a system capability (an area
// such as "info", "metrics" or "identities"): administrators may; anyone
// else only through a token's system scope covering the area and actions.
func (d *Decider) System(area string, want types.ScopeActions) bool {
	if d.admin {
		return true
	}
	if d.scope == nil {
		return false
	}
	for _, e := range d.scope.Entries {
		if e.Kind == types.ScopeSystem && areaCovers(e.Target, area) && e.Actions.Covers(want) {
			return true
		}
	}
	return false
}

const allActions = types.ActionRead | types.ActionAnnotate | types.ActionWrite |
	types.ActionDelete | types.ActionManage | types.ActionScan | types.ActionExecute

func (r rule) allows(path string, folder bool) bool {
	for _, x := range r.exclude {
		if x.Match(path) {
			return false
		}
	}
	for _, in := range r.include {
		if in.Match(path) || (folder && in.MatchBelow(path)) {
			return true
		}
	}
	return false
}

// covers reports whether a permission target names repo. A target naming a
// repository that no longer exists simply never matches, as Artifactory
// keeps such targets and ignores them.
func covers(target string, repo Repo) bool {
	key, cached := types.ResolveRepoKey(repo.Key)
	class := repo.Class
	if cached {
		class = "remote"
	}
	return types.MatchesRepository(target, key, class)
}

// TargetAllows reports whether a permission target's include and exclude
// patterns admit path, a folder admitting what lies below it.
func TargetAllows(t types.Target, path string, folder bool) bool {
	return rule{include: compileAll(t.Include, "**"), exclude: compileAll(t.Exclude, "")}.
		allows(path, folder)
}

func compileAll(patterns []string, fallback string) []Pattern {
	var out []Pattern
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, CompilePattern(p))
		}
	}
	// An empty include list means everything, Artifactory's default; an
	// empty exclude list excludes nothing.
	if out == nil && fallback != "" {
		out = []Pattern{CompilePattern(fallback)}
	}
	return out
}

func heldBy(g types.Grants, user string, groups []string) types.Actions {
	var a types.Actions
	if user != "" {
		a = g.Users[user]
	}
	for _, name := range groups {
		a |= g.Groups[name]
	}
	return a
}

func groupAdmin(ctx context.Context, gs store.GroupStore, groups []string) (bool, error) {
	for _, name := range groups {
		g, err := gs.Get(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			// A membership can outlive its group; it grants nothing.
			continue
		}
		if err != nil {
			return false, fmt.Errorf("authz: group %s: %w", name, err)
		}
		if g.AdminPrivileges {
			return true, nil
		}
	}
	return false, nil
}

// scopeShape is what a token scope does to a subject's permissions.
type scopeShape struct {
	// user: the subject's own grants apply (applied-permissions/user or
	// admin, no scope at all, or a scope of resource entries only).
	user bool
	// adminAllowed: the subject's own admin flag survives the scope.
	adminAllowed bool
	// open: applied permissions exist, so resources other than artifacts
	// and builds are reachable; false for a scope of resource entries
	// only, where only artifact entries reach artifacts and build entries
	// builds.
	open bool
	// named: the groups a groups scope names; all is the subject's groups.
	named    []string
	all      bool
	filtered bool
	artifact []types.ScopeEntry
	build    []types.ScopeEntry
}

func analyse(s *types.Scope) scopeShape {
	if s == nil {
		return scopeShape{user: true, adminAllowed: true, open: true, all: true}
	}
	var sh scopeShape
	var applied bool
	for _, e := range s.Entries {
		switch e.Kind {
		case types.ScopeUser, types.ScopeAdmin:
			sh.user, sh.adminAllowed, sh.all, applied = true, true, true, true
		case types.ScopeGroups:
			sh.named = append(sh.named, e.Names...)
			applied = true
		case types.ScopeRoles:
			// Project roles are P11; a roles scope grants nothing yet.
			applied = true
		case types.ScopeArtifact:
			sh.artifact = append(sh.artifact, e)
		case types.ScopeBuild:
			sh.build = append(sh.build, e)
		}
	}
	sh.open = applied
	if !applied {
		// Resource entries alone intersect the subject's own permissions
		// (research 05): only artifact entries reach artifacts, and only
		// build entries builds.
		sh.user, sh.all, sh.filtered = true, true, true
	}
	if len(sh.artifact) > 0 {
		sh.filtered = true
	}
	return sh
}

// groups returns the groups whose grants apply: all of the subject's, or
// the scope's named groups the subject belongs to, because a scope only
// narrows.
func (sh scopeShape) groups(member []string) []string {
	if sh.all {
		return member
	}
	var out []string
	for _, g := range member {
		if slices.Contains(sh.named, g) {
			out = append(out, g)
		}
	}
	return out
}

var scopeLetterActions = []struct {
	scope   types.ScopeActions
	actions types.Actions
}{
	{types.ScopeRead, types.ActionRead},
	{types.ScopeWrite, types.ActionWrite},
	{types.ScopeDelete, types.ActionDelete},
	{types.ScopeAnnotate, types.ActionAnnotate},
	{types.ScopeExecute, types.ActionExecute},
	{types.ScopeScan, types.ActionScan},
	{types.ScopeManage, types.ActionManage},
}

func toActions(s types.ScopeActions) types.Actions {
	if s.Covers(types.ScopeAll) {
		return allActions
	}
	var a types.Actions
	for _, m := range scopeLetterActions {
		if s&m.scope != 0 {
			a |= m.actions
		}
	}
	return a
}

// scopeActions is what a token's artifact or build entries allow at path in
// repo.
func scopeActions(entries []types.ScopeEntry, repo Repo, path string, folder bool) types.Actions {
	key, _ := types.ResolveRepoKey(repo.Key)
	var a types.Actions
	for _, e := range entries {
		if e.Target != "*" && e.Target != key {
			continue
		}
		if e.Path != "" {
			p := CompilePattern(e.Path)
			if !p.Match(path) && (!folder || !p.MatchBelow(path)) {
				continue
			}
		}
		a |= toActions(e.Actions)
	}
	return a
}

func areaCovers(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}
