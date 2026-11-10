package accesstest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access"
	accinmem "github.com/weaveplatform/artifactweave/migration/access/adapters/inmem"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	platinmem "github.com/weaveplatform/artifactweave/migration/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// cheap is the password hash cost of the kit's authenticator and of
// HashPassword, the cost test/server/support's TestHash has. The production
// cost, 64 MiB and three passes for each hash, would make every login in a
// test slow, under the race detector most of all.
var cheap = authn.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

// keyBits is the RSA size of the token service's signing keys. A key of the
// production size takes long enough to generate that building a kit for
// every test would be slow.
const keyBits = 1024

// identity is the instance identity WithTokens stores when the store has
// none. The token service derives its issuer, jfac@<uuid>, from it. It is
// the identity tests that build a token service by hand store, so an issuer
// a test expects stays the same when the test moves onto the kit.
var identity = instance.InstanceIdentity{
	InstanceID:      "jfrt@01234567-89ab-4cde-8f01-23456789abcd",
	ProtocolVersion: instance.ProtocolVersion,
}

// ErrLoginSuspended is the error, matched with errors.Is, that Basic answers
// for any password while failed logins have the user name suspended. It is
// the authenticator's own sentinel, the same value, re-exported because the
// kernel declares no such sentinel: a test outside access asserts it without
// importing access internals.
var ErrLoginSuspended = authn.ErrLoginSuspended

// Store is what the kit builds the access module over: the module's Store
// plus the instance identity its token service names as its issuer. The
// in-memory store New makes satisfies it, as does the composite store a
// test holds; every call joins the transaction ctx carries.
type Store interface {
	access.Store
	// Instance holds the instance identity, which WithTokens stores when
	// there is none.
	Instance() instance.InstanceStore
}

// Kit is the access module a running server would build, over one store: the
// real authenticator, permission evaluator and, with WithTokens, token
// service, bound through access.New as app binds them. Its methods may be
// called from any goroutine, except Verify on a kit without a token service,
// which fails the test.
type Kit struct {
	tb     testing.TB
	st     Store
	api    accessapi.API
	tokens *token.Service
}

// Option configures New and Over.
type Option func(*options)

type options struct {
	now      func() time.Time
	tokens   bool
	verifier authn.TokenVerifier
	settings accessapi.Settings
}

// WithNow makes now the clock of the authenticator and the token service, in
// place of time.Now, so that a test can move time past a token's expiry, a
// login suspension or an API restriction. access.New is given it too.
func WithNow(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithTokens builds the real token service over the store, with 1024-bit
// signing keys, and binds it as API().Tokens and as the authenticator's
// token verifier. A store without an instance identity first gets a fixed
// test identity, from which the service derives its issuer; an identity the
// store holds is kept. The service creates the first signing key while the
// kit is built, so a test that counts signing keys sees it before its own
// steps.
func WithTokens() Option { return func(o *options) { o.tokens = true } }

// WithTokenVerifier makes the authenticator verify access and reference
// tokens with v, such as a stub that answers a fixed token record. Without
// WithTokens, API().Tokens stays nil; with it, v still answers the
// authenticator, and the token service issues tokens and serves Verify.
func WithTokenVerifier(v authn.TokenVerifier) Option {
	return func(o *options) { o.verifier = v }
}

// WithSettings publishes s as API().Settings. Without it the settings are the
// zero value, which behaves as the defaults do.
func WithSettings(s accessapi.Settings) Option {
	return func(o *options) { o.settings = s }
}

// New is Over with a fresh in-memory store, which the test reaches through
// Store: the access module's in-memory stores and the platform's, over one
// unit of work.
func New(tb testing.TB, opts ...Option) *Kit {
	tb.Helper()
	return Over(tb, newMemStore(), opts...)
}

// accessStore and platformStore name the two stores memStore embeds, which
// would both be named Store as embedded fields.
type (
	accessStore   = accinmem.Store
	platformStore = platinmem.Store
)

// memStore is the store New builds: the access module's in-memory stores and
// the platform's over one memtx unit of work, so that every call joins the
// transaction ctx carries, whichever store it reaches. Both stores embed the
// unit of work, so the Tx and AfterCommit they promote would be ambiguous
// selectors; memStore declares them itself.
type memStore struct {
	*accessStore
	*platformStore
	db *memtx.Store
}

func newMemStore() *memStore {
	db := memtx.New()
	return &memStore{accessStore: accinmem.New(db), platformStore: platinmem.New(db), db: db}
}

// Tx runs fn as one transaction of the store's unit of work.
func (s *memStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	//nolint:wrapcheck // fn's own error, which the kit's callers match
	return s.db.Tx(ctx, fn)
}

// AfterCommit defers fn until the transaction ctx carries commits.
func (s *memStore) AfterCommit(ctx context.Context, fn func()) { s.db.AfterCommit(ctx, fn) }

// Over builds the access module over st, the test's own store, as app does:
// the authenticator over st's users and settings, with st's groups (which a
// transient token's user joins) and API restrictions and the token verifier,
// then access.New over st, the authenticator and the token service. Every
// answer reads st when it is asked for, so the users, groups, permissions,
// settings, tokens and restrictions a test stores apply from its next call.
// Without WithTokens or WithTokenVerifier every token is refused and
// API().Tokens is nil, as in most route tests. Over fails tb with Fatal when
// the module cannot be built, so it must be called from the goroutine
// running the test.
func Over(tb testing.TB, st Store, opts ...Option) *Kit {
	tb.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	k := &Kit{tb: tb, st: st}
	// An interface, assigned only a service that exists: a nil
	// *token.Service in it would be a typed nil, which the authenticator
	// would call instead of refusing the token.
	verifier := o.verifier
	if o.tokens {
		svc, err := newTokens(tb.Context(), st, o.now)
		fatalIf(tb, err)
		k.tokens = svc
		if verifier == nil {
			verifier = svc
		}
	}
	authn, authErr := authn.NewAuthenticator(st.Users(), st.Settings(), authn.Options{
		Now: o.now, Hash: cheap, Tokens: verifier, Groups: st.Groups(),
		APIRestrictions: st.APIRestrictions(),
	})
	m, err := access.New(access.Deps{
		Store: st, Authenticator: authn, Tokens: k.tokens, Settings: o.settings, Now: o.now,
	})
	fatalIf(tb, errors.Join(authErr, err))
	k.api = m.API()
	return k
}

// newTokens builds the token service over st, which refuses to start without
// an instance identity.
func newTokens(ctx context.Context, st Store, now func() time.Time) (*token.Service, error) {
	_, err := st.Instance().Get(ctx)
	if errors.Is(err, kernel.ErrNotFound) {
		err = st.Instance().Init(ctx, identity)
	}
	if err != nil {
		return nil, fmt.Errorf("instance identity: %w", err)
	}
	svc, err := token.New(ctx, st, token.Options{Now: now, KeyBits: keyBits})
	if err != nil {
		return nil, fmt.Errorf("token service: %w", err)
	}
	return svc, nil
}

func fatalIf(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("accesstest: %v", err)
	}
}

// Store returns the store the kit was built over: New's fresh in-memory
// store, or the one the test passed to Over.
func (k *Kit) Store() Store { return k.st }

// API returns the module's published surface, as API() of the module app
// builds: every access/api contract bound over the store, Tokens nil without
// WithTokens, and the settings WithSettings gave. A test that needs the
// principal or decider an evaluation returns, rather than a context, calls
// API().Authorizer itself.
func (k *Kit) API() accessapi.API { return k.api }

// Authorize evaluates p's permissions as the server's middleware does once it
// knows the caller (decision 0006), through API().Authorizer.Authorize, with
// the anonymous user's grants applying only while anonymousEnabled holds. It
// returns ctx carrying the principal that evaluation returned, with admin and
// groups as evaluated, and its decider, stored together with
// accessapi.WithCaller, so that kernel.FromContext, accessapi.DeciderFrom and
// authn.DeciderFrom read them as a handler behind the server would. On error
// it returns ctx unchanged and the binding's error, unwrapped, whose text
// starts "auth: permissions:" as the server's does.
func (k *Kit) Authorize(
	ctx context.Context,
	p kernel.Principal,
	anonymousEnabled bool,
) (context.Context, error) {
	p, d, err := k.api.Authorizer.Authorize(ctx, p, anonymousEnabled)
	if err != nil {
		return ctx, err //nolint:wrapcheck // the binding's "auth: permissions: %w" is the server's text
	}
	return accessapi.WithCaller(ctx, p, d), nil
}

// Decide evaluates s, a subject no request authenticated, through
// API().Authorizer.Decide, with the anonymous user's grants applying only
// while anonymousEnabled holds. Its error is the evaluator's, unwrapped.
func (k *Kit) Decide(
	ctx context.Context,
	s accessapi.Subject,
	anonymousEnabled bool,
) (accessapi.Decider, error) {
	//nolint:wrapcheck // the evaluator's error, which the contract leaves to callers to wrap
	return k.api.Authorizer.Decide(ctx, s, anonymousEnabled)
}

// Middleware returns a middleware that authorises principal(r) for every
// request with Authorize and serves the next handler with the authorised
// context, as the server's middleware does once it knows the caller. The
// test keeps its own mapping from a request to its principal, such as a
// header that names the user. When Authorize fails, the middleware reports
// the error with tb.Errorf and answers 500 without serving the next handler:
// it never calls tb.Fatal, because handlers may run off the goroutine running
// the test, where Fatal must not be called. It serves concurrent requests.
func (k *Kit) Middleware(
	tb testing.TB,
	anonymousEnabled bool,
	principal func(*http.Request) kernel.Principal,
) func(http.Handler) http.Handler {
	tb.Helper()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, err := k.Authorize(r.Context(), principal(r), anonymousEnabled)
			if err != nil {
				tb.Errorf("accesstest: authorize %s %s: %v", r.Method, r.URL.Path, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Verify resolves an access or reference token to its stored record through
// the kit's token service, so that a test checks what a consumer issued: its
// description, scope, lifetime, revocability, registry grants and parent. It
// is the service's own Verify, so it records the token's last use as a
// request presenting it would, and its errors are the service's, unwrapped: a
// token that does not verify matches authn.ErrInvalidToken. On a kit built
// without WithTokens there is no service to ask, and Verify fails the kit's
// test with Fatal, so it must then be called from the goroutine running that
// test.
func (k *Kit) Verify(ctx context.Context, raw string) (types.Token, error) {
	k.tb.Helper()
	if k.tokens == nil {
		k.tb.Fatal("accesstest: Verify needs a kit built with WithTokens")
	}
	return k.tokens.Verify(ctx, raw) //nolint:wrapcheck // the service's errors, for errors.Is
}

// HashPassword returns password hashed at the kit's cheap cost, for the
// PasswordHash of a user a test stores. A hash records its own cost, so any
// authenticator verifies it. HashPassword fails tb with Fatal if hashing
// fails.
func HashPassword(tb testing.TB, password string) []byte {
	tb.Helper()
	h, err := authn.HashPasswordWith(cheap, password)
	fatalIf(tb, err)
	return h
}
