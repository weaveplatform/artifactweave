package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// The operations Access v2 adds (issue #81): create-only groups, membership
// changes by add and remove lists, a password change of its own, the bulk
// password expiry and the password policy.

// CreateGroup creates g with members, as PutGroup does for a new name, and
// answers ErrExists when the name is taken instead of replacing the group.
// An unknown member is kernel.ErrNotFound, which Access v2 documents as 404;
// the legacy PutGroup answers it 400.
func (s *Service) CreateGroup(ctx context.Context, g model.Group, members []string) error {
	if err := validName("group", g.Name); err != nil {
		return err
	}
	next := g
	next.Realm, next.Members = model.RealmInternal, sortedUnique(members)
	err := s.Store.Groups().Create(ctx, next)
	if errors.Is(err, kernel.ErrConflict) {
		return fmt.Errorf("%w: group %q", ErrExists, g.Name)
	}
	return err //nolint:wrapcheck // ErrNotFound names the unknown member
}

// ReplaceGroup creates g with members, as CreateGroup does, when no group
// has g's name, and otherwise replaces that group with g; it reports whether
// it created the group. It is Access v2's create or replace, not the legacy
// PutGroup: JFrog's Create or Replace a Group resets every field a request
// leaves out to its create default, so g is stored as given (the caller
// fills the defaults) and members replaces the membership even when empty.
// What the request cannot carry survives: the realm and realm attributes,
// which the page says a replacement keeps, and the platform auditor flag,
// which no group request sets. An unknown member is kernel.ErrNotFound,
// which Access v2 documents as 404.
func (s *Service) ReplaceGroup(ctx context.Context, g model.Group, members []string) (bool, error) {
	if err := validName("group", g.Name); err != nil {
		return false, err
	}
	var created bool
	err := s.tx(ctx, func(ctx context.Context) error {
		groups := s.Store.Groups()
		old, err := groups.Get(ctx, g.Name)
		next := g
		next.Members = sortedUnique(members)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			created = true
			next.Realm, next.RealmAttributes = model.RealmInternal, ""
			return groups.Create(ctx, next) //nolint:wrapcheck // names an unknown member
		case err != nil:
			return err //nolint:wrapcheck // a store error
		}
		next.Realm, next.RealmAttributes = old.Realm, old.RealmAttributes
		next.Roles.PlatformAuditor = old.Roles.PlatformAuditor
		return groups.Update(ctx, next) //nolint:wrapcheck // names an unknown member
	})
	return created, err
}

// UpdateUserGroups applies add, then remove, to the user's memberships and
// returns the resulting groups. A name in both lists ends up removed; an
// unknown group in add is ErrInvalid.
func (s *Service) UpdateUserGroups(
	ctx context.Context,
	name string,
	add, remove []string,
) ([]string, error) {
	var groups []string
	err := s.tx(ctx, func(ctx context.Context) error {
		u, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		next := sortedUnique(append(slices.Clone(u.Groups), add...))
		next = slices.DeleteFunc(next, func(g string) bool { return slices.Contains(remove, g) })
		u.Groups = next
		if err := groupsError(s.Store.Users().Update(ctx, u)); err != nil {
			return err
		}
		groups = next
		return nil
	})
	return groups, err
}

// UpdateGroupMembers applies add, then remove, to the group's members and
// returns the resulting member list. Unknown users in add are
// kernel.ErrNotFound, which Access v2 answers 404.
func (s *Service) UpdateGroupMembers(
	ctx context.Context,
	name string,
	add, remove []string,
) ([]string, error) {
	var members []string
	err := s.tx(ctx, func(ctx context.Context) error {
		if _, err := s.Store.Groups().Get(ctx, name); err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		if len(add) > 0 {
			if err := s.Store.Groups().AddMembers(ctx, name, add...); err != nil {
				return err //nolint:wrapcheck // ErrNotFound names the unknown user
			}
		}
		if len(remove) > 0 {
			if err := s.Store.Groups().RemoveMembers(ctx, name, remove...); err != nil {
				return err //nolint:wrapcheck // a store error
			}
		}
		g, err := s.Store.Groups().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // a store error
		}
		members = g.Members
		return nil
	})
	return members, err
}

// SetPassword replaces the user's password. It must satisfy the policy and,
// when the policy forbids reuse, differ from the current one. A new
// password clears an expiry.
func (s *Service) SetPassword(ctx context.Context, name, password string) error {
	if password == "" {
		return invalidf("password is required")
	}
	return s.tx(ctx, func(ctx context.Context) error {
		u, err := s.Store.Users().Get(ctx, name)
		if err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the handler's 404
		}
		if err := s.setPassword(ctx, &u, password, s.now(), false); err != nil {
			return err
		}
		return s.Store.Users().Update(ctx, u) //nolint:wrapcheck // a store error
	})
}

// ExpireAllPasswords forces every internal user with a password to set a
// new one, and revokes their encrypted passwords as ExpirePasswords does, so
// UnexpireAllPasswords cannot bring an old one back; users whose internal
// password is disabled are unaffected.
func (s *Service) ExpireAllPasswords(ctx context.Context) error {
	return s.forEachUser(ctx, func(u *model.User) bool {
		if u.InternalPasswordDisabled || u.PasswordExpired {
			return false
		}
		u.PasswordExpired = true
		return true
	}, func(ctx context.Context, expired []string) error {
		return s.revokeEncryptedPasswords(ctx, expired...)
	})
}

// UnexpireAllPasswords makes every expired internal password valid again
// and restarts its age, so the expiration policy does not expire it again
// at once.
func (s *Service) UnexpireAllPasswords(ctx context.Context) error {
	now := s.now()
	return s.forEachUser(ctx, func(u *model.User) bool {
		if !u.PasswordExpired {
			return false
		}
		u.PasswordExpired, u.PasswordChanged = false, now
		return true
	}, nil)
}

// forEachUser applies change to every user in one transaction and stores
// those it reports changed; then, when set, runs in the same transaction
// with their names.
func (s *Service) forEachUser(
	ctx context.Context,
	change func(*model.User) bool,
	then func(ctx context.Context, changed []string) error,
) error {
	return s.tx(ctx, func(ctx context.Context) error {
		users, err := s.Users(ctx)
		if err != nil {
			return err
		}
		var changed []string
		for i := range users {
			if !change(&users[i]) {
				continue
			}
			if err := s.Store.Users().Update(ctx, users[i]); err != nil {
				return err //nolint:wrapcheck // a store error
			}
			changed = append(changed, users[i].Name)
		}
		if then == nil || len(changed) == 0 {
			return nil
		}
		return then(ctx, changed)
	})
}

// --- Password policy -----------------------------------------------------------

// PasswordPolicy returns the stored policy, or model.DefaultPasswordPolicy
// while none is stored.
func (s *Service) PasswordPolicy(ctx context.Context) (model.PasswordPolicy, error) {
	var p model.PasswordPolicy
	err := s.Store.Settings().Get(ctx, settings.SettingPasswordPolicy, &p)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return model.DefaultPasswordPolicy(), nil
	case err != nil:
		return p, fmt.Errorf("identity: password policy: %w", err)
	}
	return p, nil
}

// SetPasswordPolicy stores p. The policy applies when a password is set or
// changed, not to passwords already stored.
func (s *Service) SetPasswordPolicy(ctx context.Context, p model.PasswordPolicy) error {
	for name, v := range map[string]int{
		"uppercase": p.Uppercase, "lowercase": p.Lowercase, "digit": p.Digit,
		"special_char": p.SpecialChar, "length": p.Length,
	} {
		if v < 0 {
			return invalidf("%s must not be negative", name)
		}
	}
	if err := s.Store.Settings().Put(ctx, settings.SettingPasswordPolicy, p); err != nil {
		return err //nolint:wrapcheck // a store error
	}
	return nil
}

// checkPolicy refuses a password the policy rejects. u is the user as
// stored, so not_match_old compares against the current password.
func (s *Service) checkPolicy(ctx context.Context, u model.User, password string) error {
	p, err := s.PasswordPolicy(ctx)
	if err != nil {
		return err
	}
	var upper, lower, digit, special int
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			upper++
		case unicode.IsLower(r):
			lower++
		case unicode.IsDigit(r):
			digit++
		case !unicode.IsLetter(r):
			special++
		}
	}
	switch {
	case strings.EqualFold(password, u.Name):
		return invalidf("the password must not match the username")
	case len([]rune(password)) < p.Length:
		return invalidf("the password must be at least %d characters long", p.Length)
	case upper < p.Uppercase:
		return invalidf("the password must contain at least %d upper-case letters", p.Uppercase)
	case lower < p.Lowercase:
		return invalidf("the password must contain at least %d lower-case letters", p.Lowercase)
	case digit < p.Digit:
		return invalidf("the password must contain at least %d digits", p.Digit)
	case special < p.SpecialChar:
		return invalidf("the password must contain at least %d special characters", p.SpecialChar)
	}
	if p.NotMatchOld && len(u.PasswordHash) > 0 {
		same, err := authn.CheckPassword(u.PasswordHash, password)
		if err != nil && !errors.Is(err, authn.ErrHashFormat) {
			return fmt.Errorf("identity: compare passwords: %w", err)
		}
		if same {
			return invalidf("the new password must differ from the current one")
		}
	}
	return nil
}
