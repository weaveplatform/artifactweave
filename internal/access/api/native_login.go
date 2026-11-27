package api

import (
	"context"
	"errors"
)

// ErrNativeLogin refuses an unknown, expired, denied or already consumed capability.
// These cases deliberately have the same public answer.
var ErrNativeLogin = errors.New("native login: approval unavailable")

// ErrNativePending reports an approval that has not been granted yet.
var ErrNativePending = errors.New("native login: approval pending")

// ErrNativeCapacity bounds the number of simultaneously outstanding approvals.
var ErrNativeCapacity = errors.New("native login: too many outstanding approvals")

// NativeRequest binds a client to the exact approval and redemption protocol.
type NativeRequest struct {
	Kind, Repository             string
	ClientID, RedirectURI, State string
	Challenge, ChallengeMethod   string
}

// NativeStart contains secret browser and polling capabilities, not P4 tokens.
// Browser opens the approval form and binds its authenticated session; Poll can
// redeem the approved npm request. Keep each handle private to that capability.
// Possessing either handle alone does not authenticate or approve a user.
type NativeStart struct{ Browser, Poll string }

// NativePage is the escaped approval form's state. Session is a cookie value,
// never a URL parameter. CSRF belongs to that session and approval together.
type NativePage struct {
	Session, CSRF, User, Client, Repository string
	Approved                                bool
}

// NativeExchange binds an authorization code to the original public client.
type NativeExchange struct{ Code, ClientID, RedirectURI, Verifier string }

// NativeRepository binds an npm approval without exposing repository secrets.
type NativeRepository struct{ Key, Class, Project, Incarnation string }

// NativeLogin is the access-owned, bounded single-process browser approval
// service. Restart invalidates approvals and sessions; issued P4 tokens retain
// their ordinary storage and revocation semantics. Methods are safe for concurrent
// use; transitions reserve one winner without holding a mutex over store or
// directory I/O. Invalid, expired, denied and consumed capabilities return
// ErrNativeLogin; store, identity and policy failures may return wrapped errors.
//
// Call context-taking methods outside a caller-owned store transaction. The
// service owns its short guarded transactions and process-local transitions
// cannot be rolled back by a caller. In particular, Login may perform directory
// I/O before its identity-publication transaction.
//
// Poll and Exchange consume a matching approved capability before issuance. Only
// one concurrent call can mint; an issuance failure, cancellation or lost response
// does not restore the capability. Start a new approval rather than retrying it.
type NativeLogin interface {
	// Start validates the client protocol bindings and captures the npm repository
	// incarnation in its own guarded transaction. It returns independent secret
	// handles, ErrNativeCapacity when full, or an error without creating approval.
	Start(ctx context.Context, request NativeRequest) (NativeStart, error)
	// Open creates or resumes the session-cookie/CSRF binding for browser. A
	// foreign session cannot adopt an authenticated or approved browser request.
	Open(browser, session string) (NativePage, error)
	// Login verifies username/password outside its own guarded publication
	// transaction, rechecks current browser eligibility/repository access, then
	// rotates the session and CSRF secrets. It retains no submitted password.
	Login(ctx context.Context, browser, session, csrf, username, password string) (NativePage, error)
	// Consent validates the session/CSRF binding and current authority in its own
	// guarded transaction. Allow starts the separate delivery lifetime; refusal
	// consumes the request. The result is Terraform's bound code/error callback
	// URL, or an empty string for npm; it never contains an access token.
	Consent(ctx context.Context, browser, session, csrf string, allow bool) (string, error)
	// Logout validates the session/CSRF binding and consumes a pending browser
	// approval. Previously issued tokens retain ordinary token revocation rules.
	Logout(browser, session, csrf string) error
	// Poll redeems poll only for its original npm repository. A valid unapproved
	// request, including concurrent browser work, returns ErrNativePending and is
	// safe to poll again. Successful redemption rechecks current authority and
	// issues one token in the service's guarded transaction; terminal failures
	// require a fresh approval as described above.
	Poll(ctx context.Context, poll, repository string) (IssuedToken, error)
	// Exchange redeems a Terraform code only for its original client, exact
	// callback and S256 verifier. It rechecks current authority and issues one
	// token in the service's guarded transaction; consumed or invalid bindings
	// return ErrNativeLogin. No refresh token is issued.
	Exchange(ctx context.Context, request NativeExchange) (IssuedToken, error)
}
