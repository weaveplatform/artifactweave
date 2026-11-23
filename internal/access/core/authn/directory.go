package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrNoDirectory reports that no external directory is enabled, so a
// password can only be checked against the internal one.
var ErrNoDirectory = errors.New("auth: no external directory is enabled")

// errNoPublication refuses Options with a Directory but without the guarded
// transaction its logins are published in.
var errNoPublication = errors.New("auth: Options.Directory needs Options.Tx and Options.Guard")

// DirectoryLogin is a password an external directory (LDAP, access/core/ldap)
// accepted, as the authenticator publishes it.
type DirectoryLogin struct {
	// Proof binds the login to the user and to the connection and revision
	// that accepted it. It becomes the principal's PasswordRevision, so
	// Current, RestoreDownload and CurrentRegistry ask the directory again
	// whether it still names an enabled, unchanged connection, and never
	// keep the password.
	Proof [32]byte
	// Email is the address the directory holds for the user, or "".
	Email string
	// AutoCreate stores a user on first login; otherwise the user acts as a
	// transient user in the auto-join groups.
	AutoCreate bool
	// ProfileUpdatable is the stored user's profile_updatable when the login
	// creates it.
	ProfileUpdatable bool
	// Fresh marks a login the directory answered now, rather than one the
	// directory client remembered: only a fresh login writes the last login
	// time and the email.
	Fresh bool
}

// Directory checks passwords against the external directories.
type Directory interface {
	// Login checks name and password against every enabled directory. It
	// answers ErrNoDirectory when none is enabled, kernel.ErrBadCredentials
	// when every directory refused them, and any other error when no
	// directory could be asked. It does directory I/O, so it is never called
	// inside a transaction.
	Login(ctx context.Context, name, password string) (DirectoryLogin, error)
	// Current reports whether proof still names an enabled connection whose
	// settings have not changed since the login, with those settings; it
	// answers kernel.ErrBadCredentials when it does not. It reads local
	// state only and joins the transaction ctx carries.
	Current(ctx context.Context, name string, proof [32]byte) (DirectoryLogin, error)
}

// directoryOutcome is what asking the directories decided.
type directoryOutcome int

const (
	// noDirectory: none is enabled; the internal answer stands.
	noDirectory directoryOutcome = iota
	// directoryRefused: every directory refused the password, a failed
	// login that counts toward the lock policy.
	directoryRefused
	// directoryUnavailable: no directory could answer; the login is refused
	// without counting, so a directory outage locks nobody out.
	directoryUnavailable
	// directoryAccepted: a directory accepted it and the login is published.
	directoryAccepted
)

// viaDirectory asks the directories about name and password outside any
// transaction, then publishes an accepted login. A refusal that publication
// finds (a changed connection, a disabled user) answers with its error and
// directoryAccepted, since the directory itself did accept the password.
func (a *Authenticator) viaDirectory(
	ctx context.Context,
	name, password string,
	now time.Time,
) (kernel.Principal, directoryOutcome, error) {
	if a.directory == nil {
		return kernel.Principal{}, noDirectory, nil
	}
	login, err := a.directory.Login(ctx, name, password)
	switch {
	case errors.Is(err, ErrNoDirectory):
		return kernel.Principal{}, noDirectory, nil
	case errors.Is(err, kernel.ErrBadCredentials):
		return kernel.Principal{}, directoryRefused, nil
	case err != nil:
		return kernel.Principal{}, directoryUnavailable, nil
	}
	p, err := a.publish(ctx, name, login, now)
	return p, directoryAccepted, err
}

// publish stores what a directory login changes, in one transaction under
// the administration guard that every connection write holds: the
// connection is checked again there, so a connection disabled, deleted or
// changed while the directory answered publishes nothing.
func (a *Authenticator) publish(
	ctx context.Context,
	name string,
	login DirectoryLogin,
	now time.Time,
) (kernel.Principal, error) {
	var p kernel.Principal
	err := a.tx.Tx(ctx, func(ctx context.Context) error {
		if err := a.guard(ctx); err != nil {
			return fmt.Errorf("auth: guard directory login: %w", err)
		}
		current, err := a.directory.Current(ctx, name, login.Proof)
		if err != nil {
			return err //nolint:wrapcheck // kernel.ErrBadCredentials, or the store's error
		}
		u, err := a.users.Get(ctx, name)
		transient := false
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			u, transient, err = a.directoryUser(ctx, name, login, current, now)
			if err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("auth: user %s: %w", name, err)
		case u.Status == model.UserLocked:
			return kernel.ErrAccountLocked
		case u.Status != "" && u.Status != model.UserEnabled:
			return kernel.ErrBadCredentials
		case login.Fresh:
			if err := a.refreshDirectoryUser(ctx, u, login, now); err != nil {
				return err
			}
		}
		if err := a.apiAllowed(ctx, u.Name); err != nil {
			return err
		}
		p = directoryPrincipal(u, login.Proof, transient)
		return nil
	})
	if err != nil {
		return kernel.Principal{}, err //nolint:wrapcheck // the sentinels above, or wrapped store errors
	}
	a.suspended.forget(name)
	return p, nil
}

// directoryUser is the user a first directory login introduces: stored in
// the ldap realm, with no internal password, the directory's email, the
// connection's profile setting and the auto-join groups, as JFrog's "Auto
// Create System Users" does; or, with auto-create off, a transient user in
// those groups that is never stored, which transient reports.
func (a *Authenticator) directoryUser(
	ctx context.Context,
	name string,
	login, current DirectoryLogin,
	now time.Time,
) (u model.User, transient bool, err error) {
	groups, err := a.TransientGroups(ctx)
	if err != nil {
		return model.User{}, false, err
	}
	if !current.AutoCreate {
		return model.User{Name: name, Groups: groups}, true, nil
	}
	u = model.User{
		Name: name, Email: login.Email, Realm: model.RealmLDAP, ProfileUpdatable: current.ProfileUpdatable,
		Status: model.UserEnabled, MFA: model.MFANone, Groups: groups, Created: now, LastLoggedIn: now,
	}
	if err := a.users.Create(ctx, u); err != nil {
		return model.User{}, false, fmt.Errorf("auth: create directory user %s: %w", name, err)
	}
	return u, false, nil
}

// refreshDirectoryUser records a fresh directory login and, for a user the
// directory created, takes the email the directory now holds. An internal
// user who logs in through the directory keeps the email set here.
func (a *Authenticator) refreshDirectoryUser(
	ctx context.Context,
	u model.User,
	login DirectoryLogin,
	now time.Time,
) error {
	if u.Realm == model.RealmLDAP && login.Email != "" && login.Email != u.Email {
		u.Email = login.Email
		if err := a.users.Update(ctx, u); err != nil {
			return fmt.Errorf("auth: update directory user %s: %w", u.Name, err)
		}
	}
	if err := a.users.RecordLogin(ctx, u.Name, now); err != nil {
		return fmt.Errorf("auth: record directory login %s: %w", u.Name, err)
	}
	return nil
}

// directoryPrincipal is the principal of a directory login: a password
// principal whose PasswordRevision is the login's proof. A transient one
// names no stored user, which the authorizer then resolves to the auto-join
// groups (kernel.IdentityTransient).
func directoryPrincipal(u model.User, proof [32]byte, transient bool) kernel.Principal {
	p := kernel.Principal{
		Name: u.Name, Admin: u.Admin, Groups: slices.Clone(u.Groups), Password: true, PasswordRevision: proof,
	}
	if transient {
		p.Identity = kernel.IdentityTransient
	}
	return p
}

// currentPasswordProof checks a password principal's revision against the
// stored user: the internal password's hash, or else a directory login's
// proof. found is false for a name no user is stored under, which only a
// directory login of a transient user can explain.
func (a *Authenticator) currentPasswordProof(
	ctx context.Context,
	u model.User,
	found bool,
	name string,
	revision [32]byte,
) (directory bool, err error) {
	if found && sha256.Sum256(u.PasswordHash) == revision {
		return false, nil
	}
	if a.directory == nil {
		return false, kernel.ErrBadCredentials
	}
	current, err := a.directory.Current(ctx, name, revision)
	if err != nil {
		return false, err //nolint:wrapcheck // kernel.ErrBadCredentials, or the store's error
	}
	if !found && current.AutoCreate {
		// The connection stores its users, so this one was deleted since.
		return false, kernel.ErrBadCredentials
	}
	return true, nil
}
