package permission

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Handlers remove the sentinel prefix and return the remaining message to
// clients; preserve the casing of those permission API response templates.
var (
	// ErrInvalid reports a document the caller must change; handlers answer
	// it 400 with the message after the sentinel.
	ErrInvalid = errors.New("permission: invalid permission")
	// ErrNotFound reports a missing permission, or a missing section of one.
	ErrNotFound = errors.New("permission: permission not found")
	// ErrConflict reports a create of a name already in use.
	ErrConflict = errors.New("permission: permission already exists")
	// ErrForbidden reports a change the caller may not make to a permission
	// it manages; handlers answer it 403 with the message after the sentinel.
	ErrForbidden = errors.New("permission: permission change not allowed")
)

const (
	// MaxNameLength is the longest permission name; the v2 and Access
	// documentation both cap names at 64 characters.
	MaxNameLength = 64
	// MaxPatternLength is the longest include or exclude pattern.
	MaxPatternLength = 1024
)

// listPage is how many permissions List reads per store call; a variable so
// a test can cross a page boundary cheaply.
var listPage = 500

// resourceTypes are the sections an Access v2 permission may carry. The
// platform provider sends destination and pipeline_source; they are stored
// and echoed but grant nothing in artifactweave.
var resourceTypes = []accessapi.ResourceType{
	accessapi.ResourceArtifact, accessapi.ResourceBuild, accessapi.ResourceReleaseBundle,
	accessapi.ResourceDestination, accessapi.ResourcePipelineSource,
}

// specialTargets are the artifact targets that name a class of
// repositories rather than one repository.
var specialTargets = []string{
	accessapi.TargetAny,
	accessapi.TargetAnyLocal,
	accessapi.TargetAnyRemote,
	accessapi.TargetAnyDistribution,
}

// ValidResourceType reports whether rt names a section the API accepts.
func ValidResourceType(rt accessapi.ResourceType) bool { return slices.Contains(resourceTypes, rt) }

// Store is what the permission service reads and writes: the access
// module's stores, of which it uses the permission, user and group stores,
// and their unit of work (port.Store's Tx). The application passes its
// composite store, which satisfies it. Store holds no repository store: the
// repositories a permission names belong to the artifacts module, which the
// service reaches only through Service.Repositories.
type Store interface {
	port.Store
}

// Service validates permission documents and reads and writes them in Store,
// checking the repositories they name through Repositories.
type Service struct {
	// Store holds the permissions and the users and groups they name. Every
	// write runs in one Tx of Store, and the calls it makes join it through
	// the context.
	Store Store
	// Repositories reads the repositories a permission's artifact section
	// names. Its errors come back unwrapped, so kernel.ErrNotFound still
	// means a missing repository, and its calls take the context of Store's
	// transaction, so it must be the artifacts module's binding over the same
	// unit of work.
	Repositories artifactsapi.Repositories
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func notFound(name string) error {
	return fmt.Errorf("%w: Permission '%s' does not exist", ErrNotFound, name)
}

// tx runs fn in one transaction; the wrap keeps fn's error for errors.Is.
func (s Service) tx(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := s.Store.Tx(ctx, fn); err != nil {
		return fmt.Errorf("permission: %w", err)
	}
	return nil
}

// ValidName refuses names that cannot be a path segment or exceed the
// documented length.
func ValidName(name string) error {
	switch {
	case name == "" || strings.TrimSpace(name) != name:
		return invalidf("Permission name %q is not valid", name)
	case strings.ContainsAny(name, "/\\"):
		return invalidf("Permission name %q must not contain a slash", name)
	case utf8.RuneCountInString(name) > MaxNameLength:
		return invalidf("Permission name %q is longer than %d characters", name, MaxNameLength)
	}
	return nil
}

// List returns every permission, ordered by name.
func (s Service) List(ctx context.Context) ([]model.Permission, error) {
	var out []model.Permission
	page := kernel.Page{Limit: listPage}
	for {
		ps, next, err := s.Store.Permissions().List(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("permission: list: %w", err)
		}
		out = append(out, ps...)
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// Pages walks every permission a store page at a time, in List's order,
// for a list written as it is read. A failed read carries List's prefix.
func (s Service) Pages() *kernel.Pages[model.Permission] {
	return kernel.NewPages(
		func(ctx context.Context, p kernel.Page) ([]model.Permission, string, error) {
			ps, next, err := s.Store.Permissions().List(ctx, p)
			if err != nil {
				return nil, "", fmt.Errorf("permission: list: %w", err)
			}
			return ps, next, nil
		},
		listPage,
	)
}

// Get returns one permission.
func (s Service) Get(ctx context.Context, name string) (model.Permission, error) {
	p, err := s.Store.Permissions().Get(ctx, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.Permission{}, notFound(name)
	}
	if err != nil {
		return model.Permission{}, fmt.Errorf("permission: get %s: %w", name, err)
	}
	return p, nil
}

// Principals are the users and groups a filtered listing asks about: those
// named, and the named users' groups when inherited grants count.
type Principals struct {
	Users  []string
	Groups []string
}

// Narrow returns p with only the grants it makes to pr. An expanded
// listing shows what the requested principals hold, not what the
// permission grants to others (getpermissions.json).
func (pr Principals) Narrow(p model.Permission) model.Permission {
	out := p
	out.Resources = make(map[accessapi.ResourceType]model.Resource, len(p.Resources))
	for rt, r := range p.Resources {
		r.Actions = model.Grants{
			Users:  only(r.Actions.Users, pr.Users),
			Groups: only(r.Actions.Groups, pr.Groups),
		}
		out.Resources[rt] = r
	}
	return out
}

func only(grants map[string]accessapi.Actions, names []string) map[string]accessapi.Actions {
	var out map[string]accessapi.Actions
	for _, n := range names {
		if a, ok := grants[n]; ok {
			if out == nil {
				out = map[string]accessapi.Actions{}
			}
			out[n] = a
		}
	}
	return out
}

// FoldUsers returns, sorted and without repeats, the stored names of the
// users that names name in any case, as the Access listing's user filter
// matches them (getpermissions). The stores hold only folded names
// (model.FoldUsername), so each name's folded spelling is looked up
// exactly. ErrNotFound names the first that matches no user, as the request
// spelled it.
func (s Service) FoldUsers(ctx context.Context, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		u, err := s.Store.Users().Get(ctx, model.FoldUsername(n))
		if errors.Is(err, kernel.ErrNotFound) {
			return nil, fmt.Errorf("%w: User '%s' does not exist", ErrNotFound, n)
		}
		if err != nil {
			return nil, fmt.Errorf("permission: user %s: %w", n, err)
		}
		out = append(out, u.Name)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Granting returns the permissions granting any action to one of users or
// one of groups, ordered by name, and the principals that matched. With
// inherited, a user's groups count as well. A user name matches in any
// case, and the principals carry its stored name; a group name matches
// exactly. ErrNotFound names the first user or group that does not exist.
func (s Service) Granting(
	ctx context.Context,
	users, groups []string,
	inherited bool,
) ([]model.Permission, Principals, error) {
	var pr Principals
	byName := map[string]model.Permission{}
	collect := func(user string, gs []string) error {
		ps, err := s.Store.Permissions().ForPrincipal(ctx, user, gs)
		if err != nil {
			return fmt.Errorf("permission: by principal: %w", err)
		}
		for _, p := range ps {
			byName[p.Name] = p
		}
		return nil
	}
	for _, name := range users {
		u, err := s.Store.Users().Get(ctx, model.FoldUsername(name))
		if errors.Is(err, kernel.ErrNotFound) {
			return nil, Principals{}, fmt.Errorf("%w: User '%s' does not exist", ErrNotFound, name)
		}
		if err != nil {
			return nil, Principals{}, fmt.Errorf("permission: user %s: %w", name, err)
		}
		var gs []string
		if inherited {
			gs = u.Groups
		}
		if err := collect(u.Name, gs); err != nil {
			return nil, Principals{}, err
		}
		pr.Users = append(pr.Users, u.Name)
		pr.Groups = append(pr.Groups, gs...)
	}
	for _, name := range groups {
		if _, err := s.Store.Groups().Get(ctx, name); errors.Is(err, kernel.ErrNotFound) {
			return nil, Principals{}, fmt.Errorf("%w: Group '%s' does not exist", ErrNotFound, name)
		} else if err != nil {
			return nil, Principals{}, fmt.Errorf("permission: group %s: %w", name, err)
		}
		if err := collect("", []string{name}); err != nil {
			return nil, Principals{}, err
		}
		pr.Groups = append(pr.Groups, name)
	}
	slices.Sort(pr.Users)
	pr.Users = slices.Compact(pr.Users)
	slices.Sort(pr.Groups)
	pr.Groups = slices.Compact(pr.Groups)
	out := slices.Collect(maps.Values(byName))
	slices.SortFunc(out, func(a, b model.Permission) int { return strings.Compare(a.Name, b.Name) })
	return out, pr, nil
}

// Create stores a new permission; ErrConflict when the name is taken. Its
// user grants are stored under folded names (model.Permission.FoldGrants).
func (s Service) Create(ctx context.Context, p model.Permission) error {
	if err := s.Validate(ctx, p); err != nil {
		return err
	}
	p = p.FoldGrants()
	return s.tx(ctx, func(ctx context.Context) error {
		if _, err := s.Store.Permissions().Get(ctx, p.Name); err == nil {
			return fmt.Errorf("%w: Permission '%s' already exists", ErrConflict, p.Name)
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("permission: get %s: %w", p.Name, err)
		}
		now := s.now()
		p.Created, p.Updated = now, now
		return s.Store.Permissions().Create(ctx, p)
	})
}

// Write applies change to the named permission in one transaction and
// reports whether it created it. change gets the stored permission, with
// exists false when there is none, and returns the permission to store or
// an error that stores nothing. A caller's checks against the stored
// permission belong in change: run before the transaction, they could pass
// on a permission a concurrent write has since replaced.
func (s Service) Write(
	ctx context.Context,
	name string,
	change func(old model.Permission, exists bool) (model.Permission, error),
) (bool, error) {
	return s.write(ctx, name, change)
}

// Manager is a holder of MANAGE who is not an administrator, as
// CheckManagerEdit asks about it; *authz.Decider is one.
type Manager interface {
	// Grantable returns the actions it may grant or revoke in a section
	// whose stored grants are g.
	Grantable(g model.Grants) accessapi.Actions
	// Self returns its user name and the groups whose entries are its own.
	Self() (user string, groups []string)
}

// CheckManagerEdit checks a replace of the sections of old by those of p
// made by m. Such a holder may change only actions
// (createorreplacepermissiontargetv2.json): every section and target stays,
// with the same patterns. It may not change its own entry, or the entry of
// a group it belongs to: the permissions page lets a manager change the
// grants of "other users and groups", and without the rule a holder could
// strip or rewrite the grants an administrator gave it. In each section it
// may grant or revoke only what m.Grantable allows from the stored grants;
// judged on p's grants instead, a holder could first give itself an action
// and then pass it on.
func CheckManagerEdit(
	old, p model.Permission,
	sections []accessapi.ResourceType,
	m Manager,
) error {
	user, groups := m.Self()
	for _, rt := range sections {
		was, inOld := old.Resources[rt]
		now, inNew := p.Resources[rt]
		// The stored grants are folded and so is m's own name, so p's must be
		// too: otherwise a holder could spell its own entry BOB and pass it
		// off as another user's.
		now = now.FoldGrants()
		if inOld != inNew || !sameTargets(was.Targets, now.Targets) {
			return fmt.Errorf("%w: A user with Manage permission can only edit the actions",
				ErrForbidden)
		}
		// An absent entry reads as no actions, so an own entry that a body
		// leaves out is a revoke and is refused, while one it repeats
		// unchanged passes.
		if was.Actions.Users[user] != now.Actions.Users[user] ||
			slices.ContainsFunc(groups, func(g string) bool {
				return was.Actions.Groups[g] != now.Actions.Groups[g]
			}) {
			return fmt.Errorf("%w: A user with Manage permission cannot change its own actions "+
				"or those of its groups", ErrForbidden)
		}
		may := m.Grantable(was.Actions)
		if (changed(was.Actions.Users, now.Actions.Users)|
			changed(was.Actions.Groups, now.Actions.Groups))&^may != 0 {
			return fmt.Errorf("%w: A user with Manage permission can grant or revoke only "+
				"actions it holds, and not Manage", ErrForbidden)
		}
	}
	return nil
}

// changed is every action granted or revoked between two grant maps, over
// all principals; a principal added or removed changes all of its actions.
func changed(was, now map[string]accessapi.Actions) accessapi.Actions {
	var out accessapi.Actions
	for name, a := range was {
		out |= a ^ now[name]
	}
	for name, a := range now {
		if _, ok := was[name]; !ok {
			out |= a
		}
	}
	return out
}

// sameTargets reports whether two sections name the same targets with the
// same patterns.
func sameTargets(a, b map[string]model.Target) bool {
	return maps.EqualFunc(a, b, func(x, y model.Target) bool {
		return slices.Equal(includes(x), includes(y)) && slices.Equal(x.Exclude, y.Exclude)
	})
}

// includes are a target's include patterns; none means everything, which
// the v2 form spells "**", so the two compare equal.
func includes(t model.Target) []string {
	if len(t.Include) == 0 {
		return []string{"**"}
	}
	return t.Include
}

// Put creates p or replaces the stored permission of that name whole, and
// reports whether it created it.
func (s Service) Put(ctx context.Context, p model.Permission) (bool, error) {
	return s.write(ctx, p.Name, func(model.Permission, bool) (model.Permission, error) {
		return p, nil
	})
}

// PutResource replaces one section of the named permission, creating the
// permission when create is set and it does not exist, and keeping its
// other sections. The v1 API writes its repository section this way, so
// build and release bundle sections set through v2 survive a v1 PUT.
func (s Service) PutResource(
	ctx context.Context,
	name string,
	rt accessapi.ResourceType,
	r model.Resource,
	create bool,
) (bool, error) {
	return s.write(ctx, name, func(old model.Permission, exists bool) (model.Permission, error) {
		if !exists && !create {
			return model.Permission{}, notFound(name)
		}
		p := model.Permission{Name: name, Description: old.Description}
		p.Resources = maps.Clone(old.Resources)
		if p.Resources == nil {
			p.Resources = map[accessapi.ResourceType]model.Resource{}
		}
		p.Resources[rt] = r
		return p, nil
	})
}

// MergeResource merges patch into an existing section: principals and
// targets in patch replace their stored counterparts, others stay.
func (s Service) MergeResource(
	ctx context.Context,
	name string,
	rt accessapi.ResourceType,
	patch model.Resource,
) (model.Resource, error) {
	var merged model.Resource
	_, err := s.write(ctx, name, func(old model.Permission, exists bool) (model.Permission, error) {
		if !exists {
			return model.Permission{}, notFound(name)
		}
		cur, ok := old.Resources[rt]
		if !ok {
			return model.Permission{}, fmt.Errorf("%w: Permission '%s' has no %s resource",
				ErrNotFound, name, rt)
		}
		// Folded first, so a patch's BOB replaces the stored bob as the
		// same principal rather than joining it.
		over := patch.FoldGrants()
		merged = model.Resource{
			Actions: model.Grants{
				Users:  mergeMap(cur.Actions.Users, over.Actions.Users),
				Groups: mergeMap(cur.Actions.Groups, over.Actions.Groups),
			},
			Targets: mergeMap(cur.Targets, over.Targets),
		}
		p := old
		p.Resources = maps.Clone(old.Resources)
		p.Resources[rt] = merged
		return p, nil
	})
	return merged, err
}

func mergeMap[V any](base, over map[string]V) map[string]V {
	if len(base) == 0 && len(over) == 0 {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]V, len(over))
	}
	maps.Copy(out, over)
	return out
}

// DeleteResource removes one section; the permission stays, even empty.
func (s Service) DeleteResource(ctx context.Context, name string, rt accessapi.ResourceType) error {
	_, err := s.write(ctx, name, func(old model.Permission, exists bool) (model.Permission, error) {
		if !exists {
			return model.Permission{}, notFound(name)
		}
		if _, ok := old.Resources[rt]; !ok {
			return model.Permission{}, fmt.Errorf("%w: Permission '%s' has no %s resource",
				ErrNotFound, name, rt)
		}
		p := old
		p.Resources = maps.Clone(old.Resources)
		delete(p.Resources, rt)
		return p, nil
	})
	return err
}

// Delete removes the permission.
func (s Service) Delete(ctx context.Context, name string) error {
	err := s.Store.Permissions().Delete(ctx, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return notFound(name)
	}
	if err != nil {
		return fmt.Errorf("permission: delete %s: %w", name, err)
	}
	return nil
}

// write reads the named permission, lets change build its new state,
// validates it and stores it, in one transaction. It reports whether the
// permission was created.
func (s Service) write(
	ctx context.Context,
	name string,
	change func(old model.Permission, exists bool) (model.Permission, error),
) (bool, error) {
	if err := ValidName(name); err != nil {
		return false, err
	}
	created := false
	err := s.tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Permissions().Get(ctx, name)
		exists := err == nil
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("permission: get %s: %w", name, err)
		}
		p, err := change(old, exists)
		if err != nil {
			return err
		}
		p.Name = name
		if err := s.Validate(ctx, p); err != nil {
			return err
		}
		p = p.FoldGrants()
		now := s.now()
		p.Updated = now
		if exists {
			p.Created = old.Created
			return s.Store.Permissions().Update(ctx, p)
		}
		p.Created, created = now, true
		return s.Store.Permissions().Create(ctx, p)
	})
	return created, err
}

// Validate checks a permission against the rules every API shares: a valid
// name, known resource types, targets that exist and are not virtual,
// patterns within length, and principals that exist. A user grant's name
// matches its user in any case; an unknown one is named as the document
// spells it. The writes fold the user grants after Validate, for that
// message.
func (s Service) Validate(ctx context.Context, p model.Permission) error {
	if err := ValidName(p.Name); err != nil {
		return err
	}
	users, groups := map[string]bool{}, map[string]bool{}
	for _, rt := range p.ResourceTypes() {
		if !ValidResourceType(rt) {
			return invalidf("Unknown resource type '%s'", rt)
		}
		r := p.Resources[rt]
		if err := s.validTargets(ctx, rt, r.Targets); err != nil {
			return err
		}
		for u := range r.Actions.Users {
			users[u] = true
		}
		for g := range r.Actions.Groups {
			groups[g] = true
		}
	}
	for _, u := range slices.Sorted(maps.Keys(users)) {
		_, err := s.Store.Users().Get(ctx, model.FoldUsername(u))
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return invalidf("User '%s' does not exist", u)
		case err != nil:
			return fmt.Errorf("permission: user %s: %w", u, err)
		}
	}
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		if _, err := s.Store.Groups().Get(ctx, g); errors.Is(err, kernel.ErrNotFound) {
			return invalidf("Group '%s' does not exist", g)
		} else if err != nil {
			return fmt.Errorf("permission: group %s: %w", g, err)
		}
	}
	return nil
}

func (s Service) validTargets(
	ctx context.Context,
	rt accessapi.ResourceType,
	targets map[string]model.Target,
) error {
	if rt == accessapi.ResourceBuild {
		if len(targets) > 1 {
			return invalidf("The build resource takes exactly one target, %s",
				accessapi.BuildInfoRepository)
		}
		for key := range targets {
			if key != accessapi.BuildInfoRepository {
				return invalidf("The build resource's target must be %s, not '%s'",
					accessapi.BuildInfoRepository, key)
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(targets)) {
		t := targets[key]
		for _, pat := range slices.Concat(t.Include, t.Exclude) {
			if utf8.RuneCountInString(pat) > MaxPatternLength {
				return invalidf("A pattern of target '%s' is longer than %d characters",
					key, MaxPatternLength)
			}
		}
		if rt == accessapi.ResourceArtifact {
			if err := s.validRepository(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// validRepository accepts the special targets, a remote's "<key>-cache"
// alias, and any existing repository except a virtual one, which holds no
// content of its own.
func (s Service) validRepository(ctx context.Context, key string) error {
	if slices.Contains(specialTargets, key) {
		return nil
	}
	// The key as given first: a repository may itself be named "x-cache".
	cfg, err := s.Repositories.Get(ctx, key)
	if name, cached := repo.ResolveKey(key); cached && errors.Is(err, kernel.ErrNotFound) {
		cfg, err = s.Repositories.Get(ctx, name)
		if err == nil && cfg.Class != repo.Remote {
			err = kernel.ErrNotFound
		}
	}
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return invalidf("Repository '%s' does not exist", key)
	case err != nil:
		return fmt.Errorf("permission: repository %s: %w", key, err)
	case cfg.Class == repo.Virtual:
		return invalidf("Virtual repository '%s' cannot be a permission target", key)
	}
	return nil
}
