package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	artrepo "github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/kernel/antpath"
)

// Sources is what New reads once per request: the permissions that apply
// to the subject and the groups it acts through, which may grant admin
// privileges. New calls each store in the caller's context, so the reads
// join the caller's transaction, if there is one.
type Sources struct {
	Permissions port.PermissionStore
	Groups      port.GroupStore
	// AnonymousEnabled is the security.anonymous_access setting; the
	// anonymous user's grants apply only while it is on.
	AnonymousEnabled bool
}

type rule struct {
	target  string
	include []antpath.Pattern
	exclude []antpath.Pattern
	actions accessapi.Actions
}

// Decider answers permission questions for one subject from one snapshot
// of the permissions. Its methods only read that snapshot, so one Decider
// may be used from several goroutines. The zero Decider holds no action and
// is no administrator.
type Decider struct {
	// isAdmin: the subject acts as an administrator (admin-only endpoints).
	// hasEveryAction: the subject holds every action on content, which an admin keeps
	// under a token scoped to resources while losing admin endpoints.
	isAdmin        bool
	hasEveryAction bool
	rules          map[accessapi.ResourceType][]rule
	// hasAppliedPermissions is false when a token scope grants no applied permissions
	// (resource scopes only), so only its artifact entries can grant.
	hasAppliedPermissions bool
	// filter, when set, limits artifact actions to what the scope's
	// artifact entries allow; builds, when set, limits build actions to
	// what its build entries allow.
	filter []kernel.ScopeEntry
	builds []kernel.ScopeEntry
	scope  *kernel.Scope
	// user and groups are the identities the subject's grants are read
	// for, after a token scope narrowed them: user is empty when the scope
	// does not act as the user, and groups are those it acts through.
	user   string
	groups []string
	// self and member are who the subject is, whatever a token scope
	// narrowed it to act as: its user name, and the groups it belongs to or
	// a groups scope acts through.
	self   string
	member []string
}

// New reads the permissions that apply to s and returns its Decider. An
// anonymous subject while anonymous access is off gets the zero Decider,
// without a read. A group of s that no longer exists grants nothing; any
// other failure to read a group or the permissions is returned, wrapped,
// with no Decider.
func New(ctx context.Context, src Sources, s accessapi.Subject) (*Decider, error) {
	if s.Anonymous && !src.AnonymousEnabled {
		return &Decider{}, nil
	}
	sc := analyse(s.Scope)
	d := &Decider{
		hasAppliedPermissions: sc.hasAppliedPermissions,
		scope:                 s.Scope,
		rules:                 map[accessapi.ResourceType][]rule{},
		self:                  s.Name,
		member:                slices.Clone(s.Groups),
	}
	if sc.hasArtifactFilter {
		d.filter = sc.artifact
		if d.filter == nil {
			// Resource entries alone, none of them for artifacts: a build
			// or repo scope reaches no artifact.
			d.filter = []kernel.ScopeEntry{}
		}
	}
	d.builds = sc.build
	groups := sc.groups(s.Groups)
	d.groups = groups
	user := ""
	if sc.canUseUserGrants {
		user = s.Name
	}
	viaGroup, err := groupAdmin(ctx, src.Groups, groups)
	if err != nil {
		return nil, err
	}
	d.user = user
	d.hasEveryAction = (sc.canUseUserGrants && s.Admin) || viaGroup || sc.isAdmin
	d.isAdmin = sc.isAdmin ||
		sc.hasAppliedPermissions && ((s.Admin && sc.canActAsAdmin) || viaGroup)
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

// IsAdmin reports whether the subject acts as an administrator.
func (d *Decider) IsAdmin() bool { return d.isAdmin }

// Groups returns the groups the subject acts through: all of its groups, or
// those a token scope names.
func (d *Decider) Groups() []string { return slices.Clone(d.groups) }

// IsRepoVisible reports whether the subject may see repo in a listing and read
// its configuration (decision 0006). An administrator sees everything. A
// token scope with repo entries sees exactly the repositories they name.
// Otherwise the subject sees a repository it may read anywhere in, and every
// repository through the system:info:r scope.
func (d *Decider) IsRepoVisible(repo accessapi.Repo) bool {
	if d.isAdmin {
		return true
	}
	if d.scope != nil {
		for _, e := range d.scope.Entries {
			if e.Kind == kernel.ScopeRepo {
				return d.RepoConfig(repo.Key)
			}
		}
	}
	return d.AllowsFolder(accessapi.ResourceArtifact, repo, "", accessapi.ActionRead) ||
		d.System("info", kernel.ScopeRead)
}

// Holds reports whether g grants every action in want to the subject,
// directly or through a group it acts as. It answers questions about a
// permission document itself (who may manage it), not about content, which
// only the permission handlers ask, so accessapi.Decider leaves it out.
func (d *Decider) Holds(g model.Grants, want accessapi.Actions) bool {
	return heldBy(g, d.user, d.groups).Has(want)
}

// Grantable returns the actions the subject may grant or revoke in a
// permission section whose grants are g. An administrator may change any.
// A holder of MANAGE in g may change only the other actions it holds in g:
// Artifactory 7.x's default exclude-manage rule, without which a holder
// could hand out rights it lacks, or appoint and remove other managers
// (decision 0006). Anyone else may change none, so a holder of MANAGE in
// one section changes nothing in another. Only the permission service asks
// it, so accessapi.Decider leaves it out.
func (d *Decider) Grantable(g model.Grants) accessapi.Actions {
	if d.isAdmin {
		return allActions
	}
	held := heldBy(g, d.user, d.groups)
	if !held.Has(accessapi.ActionManage) {
		return 0
	}
	return held &^ accessapi.ActionManage
}

// Self returns the user the subject is and the groups it belongs to or acts
// through, whose entries in a permission are its own (decision 0006). A
// token scope does not narrow them: a token that acts only through a group
// is still its user's, so editing the user's entry with it is still a
// holder editing its own grants. Only the permission service asks it, so
// accessapi.Decider leaves it out.
func (d *Decider) Self() (user string, groups []string) {
	return d.self, slices.Clone(d.member)
}

// Actions returns every action the subject holds on path in repo's
// resource. folder is true for a folder, which is also visible when a
// permitted path lies below it. Allows and AllowsFolder are built on it;
// accessapi.Decider leaves it out because other modules ask for the
// actions they need, never for the whole set.
func (d *Decider) Actions(
	rt accessapi.ResourceType,
	repo accessapi.Repo,
	path string,
	folder bool,
) accessapi.Actions {
	var have accessapi.Actions
	if d.hasEveryAction {
		have = allActions
	} else {
		for _, r := range d.rules[rt] {
			if covers(r.target, repo) && r.allows(path, folder) {
				have |= r.actions
			}
		}
	}
	switch {
	case rt == accessapi.ResourceArtifact && d.filter != nil:
		have &= scopeActions(d.filter, repo, path, folder)
	case rt == accessapi.ResourceBuild && d.builds != nil:
		have &= scopeActions(d.builds, repo, path, folder)
	case rt != accessapi.ResourceArtifact && !d.hasAppliedPermissions:
		have = 0
	}
	return have
}

// Allows reports whether the subject holds every action in want on a file
// at path.
func (d *Decider) Allows(
	rt accessapi.ResourceType,
	repo accessapi.Repo,
	path string,
	want accessapi.Actions,
) bool {
	return want != 0 && d.Actions(rt, repo, path, false).Has(want)
}

// AllowsFolder is Allows for a folder.
func (d *Decider) AllowsFolder(
	rt accessapi.ResourceType,
	repo accessapi.Repo,
	path string,
	want accessapi.Actions,
) bool {
	return want != 0 && d.Actions(rt, repo, path, true).Has(want)
}

// RepoConfig reports whether a token scope lets the subject read a
// repository's configuration. Without a scope, or with applied permissions
// and no repo entries, the permissions alone decide; repo entries
// (repo:<key>:r) narrow that to the repositories they name.
// accessapi.Decider leaves it out because other modules ask
// IsRepoVisible, which applies it with the permissions.
func (d *Decider) RepoConfig(key string) bool {
	if d.isAdmin || d.scope == nil {
		return true
	}
	var named bool
	for _, e := range d.scope.Entries {
		if e.Kind != kernel.ScopeRepo {
			continue
		}
		named = true
		if (e.Target == "*" || e.Target == key) && e.Actions.Covers(kernel.ScopeRead) {
			return true
		}
	}
	return !named && d.hasAppliedPermissions
}

// System reports whether the subject may use a system capability (an area
// such as "info", "metrics" or "identities"): administrators may; anyone
// else only through a token's system scope covering the area and actions.
func (d *Decider) System(area string, want kernel.ScopeActions) bool {
	if d.isAdmin {
		return true
	}
	if d.scope == nil {
		return false
	}
	for _, e := range d.scope.Entries {
		if e.Kind == kernel.ScopeSystem && areaCovers(e.Target, area) && e.Actions.Covers(want) {
			return true
		}
	}
	return false
}

// ManagesResources reports whether the subject holds the Manage Resources
// role (the user's resources_manager, a group's manage_resources), which
// lets it manage every permission (decision 0006). The role follows the
// identities the subject acts as, as a group's admin privileges do: the
// user's own flag needs a scope that keeps the user, a group's a scope that
// keeps the group, and a scope of resource entries alone keeps neither.
// The flags are read here, not in New, because only the permission APIs
// ask, which is also why accessapi.Decider leaves it out.
func (d *Decider) ManagesResources(
	ctx context.Context,
	users port.UserStore,
	groups port.GroupStore,
) (bool, error) {
	if !d.hasAppliedPermissions {
		return false, nil
	}
	if d.user != "" {
		u, err := users.Get(ctx, d.user)
		switch {
		case err == nil && u.Roles.ResourcesManager:
			return true, nil
		case err != nil && !errors.Is(err, kernel.ErrNotFound):
			// A transient user from an OIDC token is not stored; it has no
			// flag of its own.
			return false, fmt.Errorf("authz: user %s: %w", d.user, err)
		}
	}
	for _, name := range d.groups {
		g, err := groups.Get(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("authz: group %s: %w", name, err)
		}
		if g.Roles.ResourcesManager {
			return true, nil
		}
	}
	return false, nil
}

const allActions = accessapi.ActionRead | accessapi.ActionAnnotate | accessapi.ActionWrite |
	accessapi.ActionDelete | accessapi.ActionManage | accessapi.ActionScan | accessapi.ActionExecute

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
func covers(target string, repo accessapi.Repo) bool {
	key, cached := artrepo.ResolveKey(repo.Key)
	class := repo.Class
	if cached {
		class = "remote"
	}
	return model.MatchesRepository(target, key, class)
}

// TargetAllows reports whether a permission target's include and exclude
// patterns admit path, a folder admitting what lies below it.
func TargetAllows(t model.Target, path string, folder bool) bool {
	return rule{include: compileAll(t.Include, "**"), exclude: compileAll(t.Exclude, "")}.
		allows(path, folder)
}

func compileAll(patterns []string, fallback string) []antpath.Pattern {
	var out []antpath.Pattern
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, antpath.CompilePattern(p))
		}
	}
	// An empty include list means everything, Artifactory's default; an
	// empty exclude list excludes nothing.
	if out == nil && fallback != "" {
		out = []antpath.Pattern{antpath.CompilePattern(fallback)}
	}
	return out
}

func heldBy(g model.Grants, user string, groups []string) accessapi.Actions {
	var a accessapi.Actions
	if user != "" {
		a = g.Users[user]
	}
	for _, name := range groups {
		a |= g.Groups[name]
	}
	return a
}

func groupAdmin(ctx context.Context, gs port.GroupStore, groups []string) (bool, error) {
	for _, name := range groups {
		g, err := gs.Get(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) {
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
	// canUseUserGrants: the subject's own grants apply (applied-permissions/user or
	// admin, no scope at all, or a scope of resource entries only).
	canUseUserGrants bool
	// canActAsAdmin: the subject's own admin flag survives the scope.
	canActAsAdmin bool
	// isAdmin: applied-permissions/admin, which makes the token an
	// administrator's whatever its subject, even a user that does not exist
	// (create-scoped-token). Only an administrator may mint it, so it grants
	// nothing its issuer lacked.
	isAdmin bool
	// hasAppliedPermissions: applied permissions exist, so resources other than artifacts
	// and builds are reachable; false for a scope of resource entries
	// only, where only artifact entries reach artifacts and build entries
	// builds.
	hasAppliedPermissions bool
	// named: the groups a groups scope names; canUseAllGroups selects the subject's groups.
	named             []string
	canUseAllGroups   bool
	hasArtifactFilter bool
	artifact          []kernel.ScopeEntry
	build             []kernel.ScopeEntry
}

func analyse(s *kernel.Scope) scopeShape {
	if s == nil {
		return scopeShape{
			canUseUserGrants:      true,
			canActAsAdmin:         true,
			hasAppliedPermissions: true,
			canUseAllGroups:       true,
		}
	}
	var sh scopeShape
	var applied bool
	for _, e := range s.Entries {
		switch e.Kind {
		case kernel.ScopeUser:
			sh.canUseUserGrants, sh.canActAsAdmin, sh.canUseAllGroups, applied = true, true, true, true
		case kernel.ScopeAdmin:
			sh.canUseUserGrants, sh.canActAsAdmin, sh.canUseAllGroups, applied = true, true, true, true
			sh.isAdmin = true
		case kernel.ScopeGroups:
			sh.named = append(sh.named, e.Names...)
			applied = true
		case kernel.ScopeRoles:
			// Project roles are P11; a roles scope grants nothing yet.
			applied = true
		case kernel.ScopeArtifact:
			sh.artifact = append(sh.artifact, e)
		case kernel.ScopeBuild:
			sh.build = append(sh.build, e)
		}
	}
	sh.hasAppliedPermissions = applied
	if !applied {
		// Resource entries alone intersect the subject's own permissions
		// (research 05): only artifact entries reach artifacts, and only
		// build entries builds.
		sh.canUseUserGrants, sh.canUseAllGroups, sh.hasArtifactFilter = true, true, true
	}
	if len(sh.artifact) > 0 {
		sh.hasArtifactFilter = true
	}
	return sh
}

// groups returns the groups whose grants apply: all of the subject's, or
// the scope's named groups the subject belongs to, because a scope only
// narrows.
func (sh scopeShape) groups(member []string) []string {
	if sh.canUseAllGroups {
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
	scope   kernel.ScopeActions
	actions accessapi.Actions
}{
	{kernel.ScopeRead, accessapi.ActionRead},
	{kernel.ScopeWrite, accessapi.ActionWrite},
	{kernel.ScopeDelete, accessapi.ActionDelete},
	{kernel.ScopeAnnotate, accessapi.ActionAnnotate},
	{kernel.ScopeExecute, accessapi.ActionExecute},
	{kernel.ScopeScan, accessapi.ActionScan},
	{kernel.ScopeManage, accessapi.ActionManage},
}

func toActions(s kernel.ScopeActions) accessapi.Actions {
	if s.Covers(kernel.ScopeAll) {
		return allActions
	}
	var a accessapi.Actions
	for _, m := range scopeLetterActions {
		if s&m.scope != 0 {
			a |= m.actions
		}
	}
	return a
}

// scopeActions is what a token's artifact or build entries allow at path in
// repo.
func scopeActions(
	entries []kernel.ScopeEntry,
	repo accessapi.Repo,
	path string,
	folder bool,
) accessapi.Actions {
	key, _ := artrepo.ResolveKey(repo.Key)
	var a accessapi.Actions
	for _, e := range entries {
		if e.Target != "*" && e.Target != key {
			continue
		}
		if e.Path != "" {
			p := antpath.CompilePattern(e.Path)
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
