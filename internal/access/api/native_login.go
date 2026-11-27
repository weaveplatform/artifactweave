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

// NativeStart contains separate browser and polling capabilities. Neither
// capability is a credential; approval requires a fresh authenticated session.
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
// their ordinary storage and revocation semantics.
type NativeLogin interface {
	Start(context.Context, NativeRequest) (NativeStart, error)
	Open(browser, session string) (NativePage, error)
	Login(context.Context, string, string, string, string, string) (NativePage, error)
	Consent(context.Context, string, string, string, bool) (string, error)
	Logout(string, string, string) error
	Poll(context.Context, string, string) (IssuedToken, error)
	Exchange(context.Context, NativeExchange) (IssuedToken, error)
}
