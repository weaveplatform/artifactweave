// Package identity manages internal users and groups and the account
// policies (decision 0005): creation and replacement with hashed passwords,
// group membership and auto-join, the user lock and password expiration
// policies, locks and forced password changes. The legacy security API and
// Access v2 serve it in their own shapes.
package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrInvalid reports a request the caller must change: a missing or
// mismatched field, an unknown group or member, or a change to a protected
// principal. Handlers answer it 400.
var ErrInvalid = errors.New("invalid")

// Service manages users, groups and the account policies.
type Service struct {
	Store store.Store
	// Hash is the password hash cost; the zero value is production's.
	Hash auth.HashParams
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

// tx runs fn in one transaction. The wrap keeps fn's error for errors.Is.
func (s *Service) tx(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := s.Store.Tx(ctx, fn); err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	return nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// validName refuses names that cannot appear in a path segment or a token
// subject (jfac@<id>/users/<name>).
func validName(kind, name string) error {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.TrimSpace(name) != name {
		return invalid("%s name %q is not valid", kind, name)
	}
	return nil
}

// --- Users -----------------------------------------------------------------

// UserPatch holds the fields an update changes; nil leaves a field as it is.
type UserPatch struct {
	Email                    *string
	Password                 *string
	Admin                    *bool
	ProfileUpdatable         *bool
	DisableUIAccess          *bool
	InternalPasswordDisabled *bool
	Groups                   *[]string
	PolicyViewer             *bool
	PolicyManager            *bool
	WatchManager             *bool
	ReportsManager           *bool
}

// PutUser creates u, or replaces every field an existing user's document
// carries, and reports whether it created it. password is the clear-text
// password, required unless u.InternalPasswordDisabled. A new user also
// joins every auto-join group.
func (s *Service) PutUser(ctx context.Context, u types.User, password string) (bool, error) {
	if err := validName("user", u.Name); err != nil {
		return false, err
	}
	if u.Email == "" {
		return false, invalid("email is required")
	}
	if password == "" && !u.InternalPasswordDisabled {
		return false, invalid("password is required unless internalPasswordDisabled is true")
	}
	var created bool
	err := s.tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Users().Get(ctx, u.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			created = true
		case err != nil:
			return err //nolint:wrapcheck // a store error, mapped by the handler
		}
		now := s.now()
		next := u
		next.Realm, next.MFA = types.RealmInternal, types.MFANone
		if created {
			next.Status, next.Created = types.UserEnabled, now
			auto, err := s.Store.Groups().AutoJoin(ctx)
			if err != nil {
				return err //nolint:wrapcheck // a store error
			}
			next.Groups = sortedUnique(append(slices.Clone(u.Groups), auto...))
		} else {
			// Server-owned state survives a replacement.
			next.Status, next.Created, next.LastLoggedIn = old.Status, old.Created, old.LastLoggedIn
			next.FailedLogins = old.FailedLogins
			next.PasswordHash, next.PasswordChanged = old.PasswordHash, old.PasswordChanged
			next.PasswordExpired = old.PasswordExpired
			next.Roles.ResourcesManager, next.Roles.ManageWebhook = old.Roles.ResourcesManager, old.Roles.ManageWebhook
			next.Roles.PlatformAuditor = old.Roles.PlatformAuditor
			if err := s.keepAnAdmin(ctx, old, next); err != nil {
				return err
			}
		}
		if err := s.setPassword(&next, password, now); err != nil {
			return err
		}
		if created {
			return groupsError(s.Store.Users().Create(ctx, next))
		}
		return groupsError(s.Store.Users().Update(ctx, next))
	})
	return created, err
}

// PatchUser changes the fields p carries; Groups, when present, replaces
// the membership.
func (s *Service) PatchUser(ctx context.Context, name string, p UserPatch) error {
	return s.tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		next := old
		set(&next.Email, p.Email)
		set(&next.Admin, p.Admin)
		set(&next.ProfileUpdatable, p.ProfileUpdatable)
		set(&next.DisableUIAccess, p.DisableUIAccess)
		set(&next.InternalPasswordDisabled, p.InternalPasswordDisabled)
		set(&next.Roles.PolicyViewer, p.PolicyViewer)
		set(&next.Roles.PolicyManager, p.PolicyManager)
		set(&next.Roles.WatchManager, p.WatchManager)
		set(&next.Roles.ReportsManager, p.ReportsManager)
		if p.Groups != nil {
			next.Groups = slices.Clone(*p.Groups)
		}
		// Only an update that sets the email must set a real one: the seeded
		// admin has none, and an update of its other fields must work.
		if p.Email != nil && *p.Email == "" {
			return invalid("email cannot be empty")
		}
		if err := s.keepAnAdmin(ctx, old, next); err != nil {
			return err
		}
		if p.Password != nil {
			if *p.Password == "" {
				return invalid("password cannot be empty")
			}
			if err := s.setPassword(&next, *p.Password, s.now()); err != nil {
				return err
			}
		}
		return groupsError(s.Store.Users().Update(ctx, next))
	})
}

// setPassword hashes password into u, unless it is empty, and restarts the
// password's age.
func (s *Service) setPassword(u *types.User, password string, now time.Time) error {
	if password == "" {
		return nil
	}
	hash, err := auth.HashPasswordWith(s.Hash, password)
	if err != nil {
		return fmt.Errorf("identity: hash password: %w", err)
	}
	u.PasswordHash, u.PasswordChanged, u.PasswordExpired = hash, now, false
	return nil
}

// keepAnAdmin refuses demoting the last administrator: without one, nothing
// could manage the instance again.
func (s *Service) keepAnAdmin(ctx context.Context, old, next types.User) error {
	if !old.Admin || next.Admin {
		return nil
	}
	admins, err := s.admins(ctx)
	if err != nil {
		return err
	}
	if len(admins) == 1 && admins[0] == old.Name {
		return invalid("user %q is the last administrator", old.Name)
	}
	return nil
}

func (s *Service) admins(ctx context.Context) ([]string, error) {
	users, err := s.Users(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		if u.Admin {
			out = append(out, u.Name)
		}
	}
	return out, nil
}

// DeleteUser removes a user, its group memberships and its tokens. The
// seeded admin and anonymous users and the last administrator stay.
func (s *Service) DeleteUser(ctx context.Context, name string) error {
	if name == auth.AdminUser || name == auth.AnonymousUser {
		return invalid("the built-in user %q cannot be deleted", name)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		u, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		if err := s.keepAnAdmin(ctx, u, types.User{}); err != nil {
			return err
		}
		if err := s.revokeTokens(ctx, name); err != nil {
			return err
		}
		return s.Store.Users().Delete(ctx, name) //nolint:wrapcheck // a store error
	})
}

// revokeTokens deletes the user's tokens. Their subject is
// jfac@<service id>/users/<name> (decision 0005).
func (s *Service) revokeTokens(ctx context.Context, name string) error {
	suffix := "/users/" + name
	var page store.Page
	for {
		tokens, next, err := s.Store.Tokens().List(ctx, store.TokenFilter{}, page)
		if err != nil {
			return err //nolint:wrapcheck // a store error
		}
		for _, t := range tokens {
			if strings.HasSuffix(t.Subject, suffix) {
				if err := s.Store.Tokens().Delete(ctx, t.ID); err != nil &&
					!errors.Is(err, store.ErrNotFound) {
					return err //nolint:wrapcheck // a store error
				}
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

// User returns one user.
func (s *Service) User(ctx context.Context, name string) (types.User, error) {
	return s.Store.Users().Get(ctx, name) //nolint:wrapcheck // ErrNotFound is the handler's 404
}

// Users returns every user, ordered by name.
func (s *Service) Users(ctx context.Context) ([]types.User, error) {
	return all(ctx, s.Store.Users().List)
}

// --- Groups ----------------------------------------------------------------

// GroupPatch holds the fields an update changes; nil leaves a field as it is.
type GroupPatch struct {
	Description     *string
	AutoJoin        *bool
	AdminPrivileges *bool
	Members         *[]string
	WatchManager    *bool
	PolicyManager   *bool
	ReportsManager  *bool
}

// PutGroup creates g, or replaces every field of an existing group, and
// reports whether it created it. members, when not nil, replaces the
// membership; otherwise an existing group keeps its members.
func (s *Service) PutGroup(ctx context.Context, g types.Group, members *[]string) (bool, error) {
	if err := validName("group", g.Name); err != nil {
		return false, err
	}
	var created bool
	err := s.tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Groups().Get(ctx, g.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			created = true
		case err != nil:
			return err //nolint:wrapcheck // a store error
		}
		next := g
		next.Realm, next.Members = types.RealmInternal, nil
		switch {
		case members != nil:
			next.Members = sortedUnique(*members)
		case !created:
			next.Members = old.Members
		}
		if !created {
			next.ExternalID = old.ExternalID
			next.Roles.ResourcesManager, next.Roles.ManageWebhook = old.Roles.ResourcesManager, old.Roles.ManageWebhook
			next.Roles.PlatformAuditor, next.Roles.PolicyViewer = old.Roles.PlatformAuditor, old.Roles.PolicyViewer
			return membersError(s.Store.Groups().Update(ctx, next))
		}
		return membersError(s.Store.Groups().Create(ctx, next))
	})
	return created, err
}

// PatchGroup changes the fields p carries; Members, when present, replaces
// the membership.
func (s *Service) PatchGroup(ctx context.Context, name string, p GroupPatch) error {
	return s.tx(ctx, func(ctx context.Context) error {
		g, err := s.Store.Groups().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		set(&g.Description, p.Description)
		set(&g.AutoJoin, p.AutoJoin)
		set(&g.AdminPrivileges, p.AdminPrivileges)
		set(&g.Roles.WatchManager, p.WatchManager)
		set(&g.Roles.PolicyManager, p.PolicyManager)
		set(&g.Roles.ReportsManager, p.ReportsManager)
		if p.Members != nil {
			g.Members = sortedUnique(*p.Members)
		}
		return membersError(s.Store.Groups().Update(ctx, g))
	})
}

// DeleteGroup removes a group and its memberships. The seeded readers group
// stays: the Anything permission grants through it.
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	if name == auth.ReadersGroup {
		return invalid("the built-in group %q cannot be deleted", name)
	}
	return s.Store.Groups().Delete(ctx, name) //nolint:wrapcheck // ErrNotFound is the handler's 404
}

// Group returns one group with its members.
func (s *Service) Group(ctx context.Context, name string) (types.Group, error) {
	return s.Store.Groups().Get(ctx, name) //nolint:wrapcheck // ErrNotFound is the handler's 404
}

// Groups returns every group, ordered by name.
func (s *Service) Groups(ctx context.Context) ([]types.Group, error) {
	return all(ctx, s.Store.Groups().List)
}

// --- Locks and expiry --------------------------------------------------------

// ExpirePasswords forces each named user to set a new password before the
// next login and returns the names that matched no user.
func (s *Service) ExpirePasswords(ctx context.Context, names []string) ([]string, error) {
	var unknown []string
	err := s.tx(ctx, func(ctx context.Context) error {
		unknown = nil
		for _, n := range sortedUnique(names) {
			u, err := s.Store.Users().Get(ctx, n)
			if errors.Is(err, store.ErrNotFound) {
				unknown = append(unknown, n)
				continue
			}
			if err != nil {
				return err //nolint:wrapcheck // a store error
			}
			u.PasswordExpired = true
			if err := s.Store.Users().Update(ctx, u); err != nil {
				return err //nolint:wrapcheck // a store error
			}
		}
		return nil
	})
	return unknown, err
}

// UnexpirePassword clears an administrator's forced password change and
// restarts the password's age, so the policy does not expire it again at
// once.
func (s *Service) UnexpirePassword(ctx context.Context, name string) error {
	return s.tx(ctx, func(ctx context.Context) error {
		u, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		u.PasswordExpired, u.PasswordChanged = false, s.now()
		return s.Store.Users().Update(ctx, u) //nolint:wrapcheck // a store error
	})
}

// Locked returns the names of locked users.
func (s *Service) Locked(ctx context.Context) ([]string, error) {
	return s.Store.Users().Locked(ctx) //nolint:wrapcheck // a store error
}

// Unlock unlocks the named users; unknown names are ignored. An empty list
// is refused, so a client bug cannot read as "unlock nobody".
func (s *Service) Unlock(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return invalid("name at least one user to unlock")
	}
	return s.Store.Users().Unlock(ctx, names...) //nolint:wrapcheck // a store error
}

// UnlockAll unlocks every locked user.
func (s *Service) UnlockAll(ctx context.Context) error {
	return s.Store.Users().Unlock(ctx) //nolint:wrapcheck // a store error
}

// LockPolicy returns the user lock policy.
func (s *Service) LockPolicy(ctx context.Context) (types.UserLockPolicy, error) {
	return auth.LockPolicy(ctx, s.Store.Settings()) //nolint:wrapcheck // already wrapped
}

// SetLockPolicy stores p. Disabling the policy also clears every lock, as
// Artifactory does, so turning it off is never a way to strand users.
func (s *Service) SetLockPolicy(ctx context.Context, p types.UserLockPolicy) error {
	if p.LoginAttempts == 0 {
		p.LoginAttempts = auth.DefaultLoginAttempts
	}
	if p.LoginAttempts < 1 {
		return invalid("loginAttempts must be at least 1")
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.Store.Settings().Put(ctx, types.SettingUserLockPolicy, p); err != nil {
			return err //nolint:wrapcheck // a store error
		}
		if !p.Enabled {
			return s.Store.Users().Unlock(ctx) //nolint:wrapcheck // a store error
		}
		return nil
	})
}

// ExpirationPolicy returns the password expiration policy.
func (s *Service) ExpirationPolicy(ctx context.Context) (types.PasswordExpirationPolicy, error) {
	return auth.ExpirationPolicy(ctx, s.Store.Settings()) //nolint:wrapcheck // already wrapped
}

// SetExpirationPolicy stores p and returns what was stored.
func (s *Service) SetExpirationPolicy(
	ctx context.Context,
	p types.PasswordExpirationPolicy,
) (types.PasswordExpirationPolicy, error) {
	if p.MaxAgeDays < 1 {
		return p, invalid("passwordMaxAge must be at least 1 day")
	}
	if err := s.Store.Settings().Put(ctx, types.SettingPasswordExpirationPolicy, p); err != nil {
		return p, err //nolint:wrapcheck // a store error
	}
	return p, nil
}

// --- Helpers -----------------------------------------------------------------

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// groupsError turns the store's "no such group" into the 400 the API
// documents for unknown groups.
func groupsError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return invalid("%v", err)
	}
	return err
}

// membersError turns the store's "no such user" into a 400 for unknown
// members.
func membersError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return invalid("%v", err)
	}
	return err
}

// all collects every page of a list.
func all[T any](
	ctx context.Context,
	list func(context.Context, store.Page) ([]T, string, error),
) ([]T, error) {
	var (
		out  []T
		page store.Page
	)
	for {
		items, next, err := list(ctx, page)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}
