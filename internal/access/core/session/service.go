package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/url"
	"sync"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

const (
	approvalLifetime = 2 * time.Minute
	codeLifetime     = time.Minute
	maxApprovals     = 1024
)

// Repository describes an npm repository in the caller's guarded transaction.
// Its binding includes the immutable incarnation and current owner; the service
// separately checks current user visibility before consent and issuance.
type Repository func(context.Context, string) (accessapi.NativeRepository, error)

// Deps supplies local identity publication. Guard serializes current security
// and repository policy with token issuance; Basic runs outside that transaction
// because an enabled directory can perform network I/O.
type Deps struct {
	Store tx.Runner
	Auth  accessapi.Authenticator
	// Authenticate applies the access-owned browser-password context. It must
	// never mark an ordinary API request as an interactive login.
	Authenticate func(context.Context, string, string) (kernel.Principal, error)
	Authorizer   accessapi.Authorizer
	Tokens       accessapi.Tokens
	Guard        func(context.Context) error
	Eligible     func(context.Context, kernel.Principal) error
	Repository   Repository
	Now          func() time.Time
	TokenExpiry  time.Duration
}

type approval struct {
	request                           accessapi.NativeRequest
	browser, poll, cookie, csrf, code string
	binding                           accessapi.NativeRepository
	principal                         kernel.Principal
	expires, codeExpires              time.Time
	approved, busy                    bool
}

// Service owns bounded approvals. It starts no goroutines. Short map operations
// reserve each transition, then release the mutex before credential/store I/O.
// A claimed redemption is consumed even when issuance fails or its response is
// lost: retrying a capability can never mint a second token.
type Service struct {
	d       Deps
	mu      sync.Mutex
	entries map[string]*approval
}

// New creates an empty process-local service. Dependencies must be fully bound
// by the access module; constructing it performs no store or directory I/O.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Authenticate == nil {
		d.Authenticate = d.Auth.Basic
	}
	return &Service{d: d, entries: make(map[string]*approval)}
}

func secret() string         { return base64.RawURLEncoding.EncodeToString(randBytes()) }
func randBytes() []byte      { b := make([]byte, 32); _, _ = rand.Read(b); return b }
func digest(s string) string { h := sha256.Sum256([]byte(s)); return string(h[:]) }
func same(a, b string) bool  { return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// Start creates independent browser and client capabilities after validating
// protocol bindings. The caller constructs URLs from a trusted configured origin.
func (s *Service) Start(ctx context.Context, r accessapi.NativeRequest) (accessapi.NativeStart, error) {
	if err := validateRequest(r); err != nil {
		return accessapi.NativeStart{}, err
	}
	var binding accessapi.NativeRepository
	if r.Kind == "npm" {
		if s.d.Repository == nil {
			return accessapi.NativeStart{}, accessapi.ErrNativeLogin
		}
		err := s.d.Store.Tx(ctx, func(ctx context.Context) error {
			if err := s.d.Guard(ctx); err != nil {
				return err
			}
			var err error
			binding, err = s.d.Repository(ctx, r.Repository)
			return err
		})
		if err != nil {
			return accessapi.NativeStart{}, fmt.Errorf("native login repository: %w", err)
		}
	}
	b, p := secret(), secret()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, a := range s.entries {
		if !a.busy && !s.d.Now().Before(a.expires) {
			delete(s.entries, k)
		}
	}
	if len(s.entries) >= maxApprovals {
		return accessapi.NativeStart{}, accessapi.ErrNativeCapacity
	}
	s.entries[digest(b)] = &approval{request: r, binding: binding, browser: b, poll: p, expires: s.d.Now().Add(approvalLifetime)}
	return accessapi.NativeStart{Browser: b, Poll: p}, nil
}

func validateRequest(r accessapi.NativeRequest) error {
	switch r.Kind {
	case "npm":
		if r.Repository != "" && len(r.Repository) <= 255 && r.ClientID == "" && r.RedirectURI == "" && r.State == "" && r.Challenge == "" {
			return nil
		}
	case "terraform":
		if r.Repository == "" && r.ClientID == "terraform-cli" && len(r.State) > 0 && len(r.State) <= 1024 &&
			ValidateRedirect(r.RedirectURI, PortRange{Min: 10000, Max: 10010}) == nil && ValidatePKCEChallenge(r.Challenge, r.ChallengeMethod) == nil {
			return nil
		}
	}
	return ErrInvalidRequest
}

func (s *Service) current(browser string) (*approval, error) {
	a := s.entries[digest(browser)]
	if a == nil || a.busy || !s.d.Now().Before(a.expires) {
		return nil, accessapi.ErrNativeLogin
	}
	return a, nil
}

func page(a *approval) accessapi.NativePage {
	return accessapi.NativePage{Session: a.cookie, CSRF: a.csrf, User: a.principal.Name, Client: a.request.Kind, Repository: a.request.Repository, Approved: a.approved}
}

// Open binds a form to an unpredictable cookie. A foreign cookie cannot adopt
// an authenticated session, even when the browser capability is known.
func (s *Service) Open(browser, cookie string) (accessapi.NativePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.current(browser)
	if err != nil {
		return accessapi.NativePage{}, err
	}
	if !same(cookie, a.cookie) {
		// A copied browser URL is not the authenticated browser session. A GET
		// with a foreign cookie must not sign that browser out or revoke its
		// approved poll capability while the client is redeeming it.
		if a.principal.Name != "" || a.approved {
			return accessapi.NativePage{}, accessapi.ErrNativeLogin
		}
		a.cookie, a.csrf = secret(), secret()
		a.principal = kernel.Principal{}
	}
	return page(a), nil
}

func (s *Service) claim(browser, cookie, csrf string) (*approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.current(browser)
	if err != nil {
		return nil, err
	}
	if a.approved || !same(cookie, a.cookie) || !same(csrf, a.csrf) {
		return nil, accessapi.ErrNativeLogin
	}
	a.busy = true
	return a, nil
}

func (s *Service) check(ctx context.Context, a *approval) (kernel.Principal, error) {
	if err := s.d.Guard(ctx); err != nil {
		return kernel.Principal{}, fmt.Errorf("native login authority: %w", err)
	}
	if !s.d.Now().Before(a.expires) {
		return kernel.Principal{}, accessapi.ErrNativeLogin
	}
	p, err := s.d.Auth.Current(ctx, a.principal)
	if err != nil {
		return kernel.Principal{}, fmt.Errorf("native login current identity: %w", err)
	}
	if p.Anonymous || !p.Password || p.Identity != kernel.IdentityStored {
		return kernel.Principal{}, accessapi.ErrNativeLogin
	}
	if err := s.d.Eligible(ctx, p); err != nil {
		return kernel.Principal{}, fmt.Errorf("native login browser eligibility: %w", err)
	}
	if a.request.Kind == "npm" {
		binding, err := s.d.Repository(ctx, a.request.Repository)
		if err != nil {
			return kernel.Principal{}, fmt.Errorf("native login current repository: %w", err)
		}
		if binding != a.binding {
			return kernel.Principal{}, accessapi.ErrNativeLogin
		}
		decider, err := s.d.Authorizer.For(ctx, p)
		if err != nil {
			return kernel.Principal{}, fmt.Errorf("native login permissions: %w", err)
		}
		if !decider.IsRepoVisible(accessapi.Repo{Key: binding.Key, Class: binding.Class}) {
			return kernel.Principal{}, accessapi.ErrNativeLogin
		}
	}
	return p, nil
}

// Login authenticates only the submitted password and rotates session and CSRF
// capabilities after a guarded publication. No password is retained.
func (s *Service) Login(ctx context.Context, browser, cookie, csrf, name, password string) (accessapi.NativePage, error) {
	a, err := s.claim(browser, cookie, csrf)
	if err != nil {
		return accessapi.NativePage{}, err
	}
	defer func() { s.mu.Lock(); a.busy = false; s.mu.Unlock() }()
	p, err := s.d.Authenticate(ctx, name, password)
	if err != nil {
		return accessapi.NativePage{}, fmt.Errorf("native login credentials: %w", err)
	}
	candidate := *a
	candidate.principal = p
	err = s.d.Store.Tx(ctx, func(ctx context.Context) error { _, e := s.check(ctx, &candidate); return e })
	if err != nil {
		return accessapi.NativePage{}, fmt.Errorf("native login publication: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.d.Now().Before(a.expires) {
		return accessapi.NativePage{}, accessapi.ErrNativeLogin
	}
	a.principal = p
	a.cookie, a.csrf = secret(), secret()
	return page(a), nil
}

// Consent grants only this client approval. Terraform receives a code, never a
// token, at the exact validated callback; npm's token stays behind its poll handle.
func (s *Service) Consent(ctx context.Context, browser, cookie, csrf string, allow bool) (string, error) {
	a, err := s.claim(browser, cookie, csrf)
	if err != nil {
		return "", err
	}
	defer func() { s.mu.Lock(); a.busy = false; s.mu.Unlock() }()
	err = s.d.Store.Tx(ctx, func(ctx context.Context) error { _, e := s.check(ctx, a); return e })
	if err != nil {
		return "", fmt.Errorf("native login consent: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.d.Now().Before(a.expires) {
		return "", accessapi.ErrNativeLogin
	}
	if !allow {
		delete(s.entries, digest(browser))
		if a.request.Kind == "terraform" {
			return callback(a, "error", "access_denied"), nil
		}
		return "", nil
	}
	a.approved = true
	// An approval near its pending deadline still receives its complete, separate
	// delivery/code lifetime. No expired pending request can enter this transition.
	a.expires = s.d.Now().Add(codeLifetime)
	if a.request.Kind == "terraform" {
		a.code = secret()
		a.codeExpires = s.d.Now().Add(codeLifetime)
		return callback(a, "code", a.code), nil
	}
	return "", nil
}

func callback(a *approval, key, value string) string {
	return a.request.RedirectURI + "?" + url.Values{key: {value}, "state": {a.request.State}}.Encode()
}

// Logout cancels this browser approval; separately issued client tokens retain
// normal token revocation semantics and are never implicitly broadened.
func (s *Service) Logout(browser, cookie, csrf string) error {
	a, err := s.claim(browser, cookie, csrf)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, digest(a.browser))
	return nil
}

// Poll consumes an approved npm capability for its original repository.
func (s *Service) Poll(ctx context.Context, poll, repository string) (accessapi.IssuedToken, error) {
	return s.redeem(ctx, func(a *approval) bool {
		return a.request.Kind == "npm" && a.request.Repository == repository && same(a.poll, poll)
	}, false)
}

// Exchange consumes a code only with its original client, callback and verifier.
func (s *Service) Exchange(ctx context.Context, r accessapi.NativeExchange) (accessapi.IssuedToken, error) {
	return s.redeem(ctx, func(a *approval) bool {
		return a.request.Kind == "terraform" && same(a.code, r.Code) && a.request.ClientID == r.ClientID && a.request.RedirectURI == r.RedirectURI &&
			VerifyPKCE(a.request.Challenge, r.Verifier) &&
			s.d.Now().Before(a.codeExpires)
	}, true)
}

func (s *Service) redeem(ctx context.Context, match func(*approval) bool, code bool) (accessapi.IssuedToken, error) {
	s.mu.Lock()
	var selected *approval
	for k, a := range s.entries {
		if !match(a) {
			continue
		}
		if !s.d.Now().Before(a.expires) {
			break
		}
		if !a.approved || a.busy && !code {
			s.mu.Unlock()
			return accessapi.IssuedToken{}, accessapi.ErrNativePending
		}
		if a.busy {
			break
		}
		a.busy = true
		selected = a
		delete(s.entries, k)
		break
	}
	s.mu.Unlock()
	if selected == nil {
		return accessapi.IssuedToken{}, accessapi.ErrNativeLogin
	}
	var out accessapi.IssuedToken
	err := s.d.Store.Tx(ctx, func(ctx context.Context) error {
		p, err := s.check(ctx, selected)
		if err != nil {
			return err
		}
		if code && !s.d.Now().Before(selected.codeExpires) {
			return accessapi.ErrNativeLogin
		}
		if !s.d.Now().Before(selected.expires) {
			return accessapi.ErrNativeLogin
		}
		out, err = s.d.Tokens.Issue(
			ctx,
			accessapi.TokenRequest{Username: p.Name, ExpiresIn: &s.d.TokenExpiry, ForceRevocable: true, Description: "native " + selected.request.Kind + " login"},
		)
		if err != nil {
			return fmt.Errorf("issue native P4 token: %w", err)
		}
		return nil
	})
	if err != nil {
		return accessapi.IssuedToken{}, fmt.Errorf("native login token publication: %w", err)
	}
	return out, nil
}
