// Package identity manages internal users and groups and the account
// policies (decision 0005): creation and replacement with hashed passwords,
// group membership and auto-join, the user lock and password expiration
// policies, locks and forced password changes. The legacy security API and
// Access v2 serve it in their own shapes.
package identity

import (
	"bytes"
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

// ErrProtected marks the ErrInvalid errors that refuse a change to a
// built-in principal. The legacy API answers them 400 like any ErrInvalid;
// Access v2 documents 403 for deleting a built-in user.
var ErrProtected = errors.New("protected")

// ErrExists answers a create-only call for a name already taken (409).
var ErrExists = errors.New("already exists")

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
	// The flags only Access v2 sets; the legacy API leaves them nil.
	ResourcesManager *bool
	ManageWebhook    *bool
	PlatformAuditor  *bool
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
			if next, err = s.newUser(ctx, u, now); err != nil {
				return err
			}
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
		if err := s.setPassword(ctx, &next, password, now, true); err != nil {
			return err
		}
		if created {
			return groupsError(s.Store.Users().Create(ctx, next))
		}
		return groupsError(s.Store.Users().Update(ctx, next))
	})
	return created, err
}

// CreateUser creates u with password, as PutUser does for a new name, and
// answers ErrExists when the name is taken instead of replacing the user.
func (s *Service) CreateUser(ctx context.Context, u types.User, password string) error {
	if err := validName("user", u.Name); err != nil {
		return err
	}
	if u.Email == "" {
		return invalid("email is required")
	}
	if password == "" && !u.InternalPasswordDisabled {
		return invalid("password is required unless internal_password_disabled is true")
	}
	return s.tx(ctx, func(ctx context.Context) error {
		now := s.now()
		next, err := s.newUser(ctx, u, now)
		if err != nil {
			return err
		}
		if err := s.setPassword(ctx, &next, password, now, false); err != nil {
			return err
		}
		err = s.Store.Users().Create(ctx, next)
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("%w: user %q", ErrExists, u.Name)
		}
		return groupsError(err)
	})
}

// newUser is u as first stored: an enabled internal user that also joins
// every auto-join group.
func (s *Service) newUser(ctx context.Context, u types.User, now time.Time) (types.User, error) {
	next := u
	next.Realm, next.MFA = types.RealmInternal, types.MFANone
	next.Status, next.Created = types.UserEnabled, now
	auto, err := s.Store.Groups().AutoJoin(ctx)
	if err != nil {
		return next, err //nolint:wrapcheck // a store error
	}
	next.Groups = sortedUnique(append(slices.Clone(u.Groups), auto...))
	return next, nil
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
		set(&next.Roles.ResourcesManager, p.ResourcesManager)
		set(&next.Roles.ManageWebhook, p.ManageWebhook)
		set(&next.Roles.PlatformAuditor, p.PlatformAuditor)
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
			if err := s.setPassword(ctx, &next, *p.Password, s.now(), true); err != nil {
				return err
			}
		}
		return groupsError(s.Store.Users().Update(ctx, next))
	})
}

// setPassword hashes password into u, unless it is empty, restarts the
// password's age and revokes u's encrypted password, which stood for the
// old one. Every way of setting a password comes through here, so none
// leaves the old encrypted password working. The password must satisfy the
// password policy. With keepSame, the current password sent back is no
// change, passes unchecked and keeps its encrypted password: Terraform's
// artifactory_user resends the password on every update, and
// not_match_old, on by default, would refuse every one (decision 0005). The
// password change endpoint passes false, so there it is a reuse.
func (s *Service) setPassword(
	ctx context.Context,
	u *types.User,
	password string,
	now time.Time,
	keepSame bool,
) error {
	if password == "" {
		return nil
	}
	if keepSame && len(u.PasswordHash) > 0 {
		if same, err := auth.CheckPassword(u.PasswordHash, password); err == nil && same {
			return nil
		}
	}
	if err := s.checkPolicy(ctx, *u, password); err != nil {
		return err
	}
	hash, err := auth.HashPasswordWith(s.Hash, password)
	if err != nil {
		return fmt.Errorf("identity: hash password: %w", err)
	}
	u.PasswordHash, u.PasswordChanged, u.PasswordExpired = hash, now, false
	return s.revokeEncryptedPasswords(ctx, u.Name)
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
		return fmt.Errorf(
			"%w: %w: the built-in user %q cannot be deleted",
			ErrInvalid,
			ErrProtected,
			name,
		)
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

// revokeEncryptedPasswords deletes the named users' encrypted passwords
// (types.TokenPurposeEncryptedPassword tokens); the next request for one
// issues a new value. Each user holds at most one, so the list is one entry
// per user who ever asked, not every token.
func (s *Service) revokeEncryptedPasswords(ctx context.Context, names ...string) error {
	var page store.Page
	for {
		tokens, next, err := s.Store.Tokens().List(
			ctx, store.TokenFilter{Purpose: types.TokenPurposeEncryptedPassword}, page,
		)
		if err != nil {
			return err //nolint:wrapcheck // a store error
		}
		for _, t := range tokens {
			sub, err := types.ParseSubject(t.Subject)
			if err != nil || sub.Kind != "users" || !slices.Contains(names, sub.Name) {
				continue
			}
			if err := s.Store.Tokens().Delete(ctx, t.ID); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return err //nolint:wrapcheck // a store error
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
	// The fields only Access v2 sets; the legacy API leaves them nil.
	ExternalID       *string
	PolicyViewer     *bool
	ResourcesManager *bool
	ManageWebhook    *bool
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
		set(&g.ExternalID, p.ExternalID)
		set(&g.Roles.PolicyViewer, p.PolicyViewer)
		set(&g.Roles.ResourcesManager, p.ResourcesManager)
		set(&g.Roles.ManageWebhook, p.ManageWebhook)
		if p.Members != nil {
			g.Members = sortedUnique(*p.Members)
		}
		return membersError(s.Store.Groups().Update(ctx, g))
	})
}

// DeleteGroup removes a group, its memberships and, in the same
// transaction, its grants in every permission: a grant left behind would
// pass to a later group of the same name. The seeded readers group is no
// exception (no JFrog source protects it, and jf rt group-delete makes
// none); seeding runs once, so it does not come back.
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.Store.Groups().Delete(ctx, name); err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		perms, err := all(ctx, s.Store.Permissions().List)
		if err != nil {
			return err
		}
		for _, p := range perms {
			if !dropGroupGrants(p, name) {
				continue
			}
			if err := s.Store.Permissions().Update(ctx, p); err != nil {
				return err //nolint:wrapcheck // a store error
			}
		}
		return nil
	})
}

// dropGroupGrants removes group's grants from p and reports whether it had
// any.
func dropGroupGrants(p types.Permission, group string) bool {
	dropped := false
	for _, r := range p.Resources {
		if _, ok := r.Actions.Groups[group]; ok {
			delete(r.Actions.Groups, group)
			dropped = true
		}
	}
	return dropped
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
// next login, revokes their encrypted passwords, and returns the names that
// matched no user.
func (s *Service) ExpirePasswords(ctx context.Context, names []string) ([]string, error) {
	var unknown []string
	err := s.tx(ctx, func(ctx context.Context) error {
		unknown = nil
		var expired []string
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
			expired = append(expired, n)
		}
		if len(expired) == 0 {
			return nil
		}
		return s.revokeEncryptedPasswords(ctx, expired...)
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

// ChangePassword replaces name's password with newPassword once oldPassword
// verifies, which the legacy password change asks of every caller,
// administrators included. An expired old password still verifies:
// changing it is how an expired password is replaced. confirm must repeat
// newPassword, and the password policy applies as for any password set.
//
// A wrong old password counts against the user lock policy as a wrong login
// does. The request is already authenticated, possibly by a token, so
// without the count this would be a way to guess the password without
// limit.
func (s *Service) ChangePassword(
	ctx context.Context,
	name, oldPassword, newPassword, confirm string,
) error {
	u, err := s.Store.Users().Get(ctx, name)
	if err != nil {
		return err //nolint:wrapcheck // ErrNotFound is the handler's 404
	}
	if u.Status == types.UserLocked {
		return invalid("user %q is locked", name)
	}
	if !passwordMatches(u, oldPassword) {
		s.recordFailure(ctx, name)
		return invalid("the old password is incorrect")
	}
	switch {
	case newPassword != confirm:
		return invalid("the new passwords do not match")
	case newPassword == "":
		return invalid("the new password is required")
	}
	return s.tx(ctx, func(ctx context.Context) error {
		cur, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		// The old password was checked outside the transaction, so the
		// Argon2 work holds no lock; a change that landed since makes it
		// stale.
		if !bytes.Equal(cur.PasswordHash, u.PasswordHash) {
			return invalid("the old password is incorrect")
		}
		if err := s.setPassword(ctx, &cur, newPassword, s.now(), false); err != nil {
			return err
		}
		return s.Store.Users().Update(ctx, cur) //nolint:wrapcheck // a store error
	})
}

// passwordMatches reports whether password is u's internal password, for a
// user who may sign in with one at all. CheckPassword's only error is a
// stored hash it cannot read, which matches nothing.
func passwordMatches(u types.User, password string) bool {
	if len(u.PasswordHash) == 0 || u.InternalPasswordDisabled ||
		(u.Status != "" && u.Status != types.UserEnabled) {
		return false
	}
	same, err := auth.CheckPassword(u.PasswordHash, password)
	return err == nil && same
}

// recordFailure counts a wrong password against the lock policy. Best
// effort, as the authenticator's count is: failing to count must not turn
// the refusal into a 500.
func (s *Service) recordFailure(ctx context.Context, name string) {
	p, err := s.LockPolicy(ctx)
	if err != nil || !p.Enabled {
		return
	}
	_, _ = s.Store.Users().RecordFailedLogin(ctx, name, p.LoginAttempts)
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

// AccessLockPolicy is the Access user lock policy: one document over the
// lock policy, the password expiration policy and the AccessLockSettings, so
// the Access and Artifactory APIs read and write the same state.
type AccessLockPolicy struct {
	// Attempts is the lock limit while locking is on, and 0 while it is off.
	Attempts int
	// PasswordExpiryDays is the maximum password age while expiry is on,
	// and 0 while it is off.
	PasswordExpiryDays int
	types.AccessLockSettings
}

// AccessLockPolicy returns the Access view of the policies, mapped as JFrog
// documents: enabled with loginAttempts is attempts, disabled is 0.
func (s *Service) AccessLockPolicy(ctx context.Context) (AccessLockPolicy, error) {
	settings := s.Store.Settings()
	lock, err := auth.LockPolicy(ctx, settings)
	if err != nil {
		return AccessLockPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	expiry, err := auth.ExpirationPolicy(ctx, settings)
	if err != nil {
		return AccessLockPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	access, err := auth.AccessLockSettings(ctx, settings)
	if err != nil {
		return AccessLockPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	p := AccessLockPolicy{AccessLockSettings: access}
	if lock.Enabled {
		p.Attempts = lock.LoginAttempts
	}
	if expiry.Enabled {
		p.PasswordExpiryDays = expiry.MaxAgeDays
	}
	return p, nil
}

// SetAccessLockPolicy stores p and returns it. A 0 turns locking or expiry
// off but keeps the stored limit or age, which the Artifactory API goes on
// reporting beside enabled false. Turning locking off clears every lock, as
// SetLockPolicy does.
func (s *Service) SetAccessLockPolicy(
	ctx context.Context,
	p AccessLockPolicy,
) (AccessLockPolicy, error) {
	switch {
	case p.Attempts < 0 || p.Attempts > 100:
		return p, invalid("attempts must be from 0 through 100")
	case p.SecondsToUnlock < 0:
		return p, invalid("secondsToUnlock must not be negative")
	case p.PasswordExpiryDays < 0:
		return p, invalid("passwordExpiryDays must not be negative")
	}
	return p, s.tx(ctx, func(ctx context.Context) error {
		settings := s.Store.Settings()
		lock, err := auth.LockPolicy(ctx, settings)
		if err != nil {
			return err //nolint:wrapcheck // already wrapped
		}
		expiry, err := auth.ExpirationPolicy(ctx, settings)
		if err != nil {
			return err //nolint:wrapcheck // already wrapped
		}
		if lock.Enabled = p.Attempts > 0; lock.Enabled {
			lock.LoginAttempts = p.Attempts
		}
		if expiry.Enabled = p.PasswordExpiryDays > 0; expiry.Enabled {
			expiry.MaxAgeDays = p.PasswordExpiryDays
		}
		for _, w := range []struct {
			key string
			v   any
		}{
			{types.SettingUserLockPolicy, lock},
			{types.SettingPasswordExpirationPolicy, expiry},
			{types.SettingAccessLockSettings, p.AccessLockSettings},
		} {
			if err := settings.Put(ctx, w.key, w.v); err != nil {
				return err //nolint:wrapcheck // a store error
			}
		}
		if !lock.Enabled {
			return s.Store.Users().Unlock(ctx) //nolint:wrapcheck // a store error
		}
		return nil
	})
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
