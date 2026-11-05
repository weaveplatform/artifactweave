package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Authenticator verifies credentials and rechecks credentials it verified
// earlier against current state (decision 0005). It answers who a credential
// acts as, never what that principal may do: Authorizer evaluates the
// permissions. A credential that does not verify answers
// kernel.ErrBadCredentials, and a verified user whose API access an
// administrator has restricted answers kernel.ErrAPIRestricted (decision
// 0043); an error reading the stores is wrapped with what was being read.
// Each call uses ctx for the stores it reads, so a call inside a store
// transaction takes part in it.
type Authenticator interface {
	// Basic verifies a user name and password. Wrong credentials, an unknown
	// or disabled user and a user without an internal password answer
	// kernel.ErrBadCredentials alike. A locked account answers
	// kernel.ErrAccountLocked whatever the password, and a correct but
	// expired password answers kernel.ErrPasswordExpired with the principal,
	// marked PasswordExpired. While basic authentication is switched off,
	// every password answers 401 "Basic authentication with a password is
	// disabled; use an access token"; while the name is suspended after
	// failed logins, every password answers a 403 that says how many seconds
	// to wait.
	Basic(ctx context.Context, name, password string) (kernel.Principal, error)
	// Token verifies an access or reference token presented as Bearer, as
	// X-JFrog-Art-Api, or as the password of a Basic credential whose user
	// name is user ("" for the first two), which must then name the token's
	// subject. The subject must still be an enabled user, so disabling a user
	// disables their tokens; a token that does not verify answers
	// kernel.ErrBadCredentials.
	Token(ctx context.Context, raw, user string) (kernel.Principal, error)
	// AnonymousAllowed reports whether a request without credentials acts as
	// the anonymous user: the security.anonymous_access setting, which is off
	// when it was never written, as in Artifactory.
	AnonymousAllowed(ctx context.Context) (bool, error)
	// Current rechecks a principal that a password, a token or anonymous
	// access authenticated earlier, after a slow remote read or for queued
	// work, against current local state: the user's status and password, the
	// token's record and expiry, the basic-authentication and anonymous
	// access settings, the password expiry policy and the user's API
	// restriction. It returns the principal as it is now, or
	// kernel.ErrBadCredentials once the credential no longer holds, which a
	// registry token's principal (CurrentRegistry rechecks those) and a
	// principal that no credential authenticated always answer. It retains,
	// forwards and verifies no credential a second time, and reads only local
	// state, so a publication may call it inside its transaction.
	Current(ctx context.Context, p kernel.Principal) (kernel.Principal, error)
	// CurrentRegistry rechecks a verified registry token's principal without
	// turning it into a general access token: its grants, scope and expiry
	// stay as they were, and the current user and the token it descends from
	// can only remove access. A principal that is no verified registry token,
	// or whose token has expired, answers kernel.ErrBadCredentials.
	CurrentRegistry(ctx context.Context, p kernel.Principal) (kernel.Principal, error)
	// RestoreDownload rechecks, as Current does, the credential that a signed
	// download envelope names for the user name: a token, a password or
	// anonymous access. The caller verifies the envelope's signature,
	// subject, path, digest and expiry first, since holding a
	// DownloadCredential proves nothing. A credential of an unknown kind, or
	// with fields its kind does not have, answers kernel.ErrBadCredentials.
	RestoreDownload(
		ctx context.Context,
		name string,
		ref kernel.DownloadCredential,
	) (kernel.Principal, error)
}
