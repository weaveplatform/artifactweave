package session

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var errTest = errors.New("controlled store failure")

type (
	transactionKey struct{}
	testStore      struct{ err error }
)

func (st *testStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	if st.err != nil {
		return st.err
	}
	return fn(context.WithValue(ctx, transactionKey{}, true))
}
func (*testStore) AfterCommit(_ context.Context, fn func()) { fn() }

type testAuth struct {
	accessapi.Authenticator
	err        error
	currentErr error
	basicHook  func()
}

func (a *testAuth) Basic(_ context.Context, name, password string) (kernel.Principal, error) {
	if a.basicHook != nil {
		a.basicHook()
	}
	if a.err != nil {
		return kernel.Principal{}, a.err
	}
	if password != "password" {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return kernel.Principal{Name: name, Password: true, Identity: kernel.IdentityStored}, nil
}

func (a *testAuth) Current(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
	if ctx.Value(transactionKey{}) != true {
		panic("identity outside transaction")
	}
	if a.currentErr != nil {
		return kernel.Principal{}, a.currentErr
	}
	if p.Name == "" {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return p, nil
}

type testTokens struct {
	accessapi.Tokens
	count atomic.Int64
	err   error
}

func (a *testTokens) Issue(ctx context.Context, r accessapi.TokenRequest) (accessapi.IssuedToken, error) {
	if ctx.Value(transactionKey{}) != true || !r.ForceRevocable || r.Refreshable || r.ExpiresIn == nil || *r.ExpiresIn <= 0 {
		panic("unsafe issuance")
	}
	a.count.Add(1)
	if a.err != nil {
		return accessapi.IssuedToken{}, a.err
	}
	return accessapi.IssuedToken{AccessToken: "issued", ExpiresIn: 3600}, nil
}

type testDecider struct {
	accessapi.Decider
	visible bool
}

func (a testDecider) IsRepoVisible(accessapi.Repo) bool { return a.visible }

type testAuthorizer struct {
	accessapi.Authorizer
	err     error
	visible bool
}

func (a *testAuthorizer) For(context.Context, kernel.Principal) (accessapi.Decider, error) {
	return testDecider{visible: a.visible}, a.err
}

type fixture struct {
	s                              *Service
	now                            time.Time
	auth                           *testAuth
	tokens                         *testTokens
	store                          *testStore
	authorizer                     *testAuthorizer
	binding                        accessapi.NativeRepository
	repoErr, guardErr, eligibleErr error
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		now:        time.Now(),
		auth:       &testAuth{},
		tokens:     &testTokens{},
		store:      &testStore{},
		authorizer: &testAuthorizer{visible: true},
		binding:    accessapi.NativeRepository{Key: "npm", Class: "local", Incarnation: "one", Project: "alpha"},
	}
	f.s = New(
		Deps{
			Store:       f.store,
			Auth:        f.auth,
			Tokens:      f.tokens,
			Authorizer:  f.authorizer,
			TokenExpiry: time.Hour,
			Now:         func() time.Time { return f.now },
			Guard: func(ctx context.Context) error {
				if ctx.Value(transactionKey{}) != true {
					t.Error("guard outside transaction")
				}
				return f.guardErr
			},
			Eligible:   func(context.Context, kernel.Principal) error { return f.eligibleErr },
			Repository: func(context.Context, string) (accessapi.NativeRepository, error) { return f.binding, f.repoErr },
		},
	)
	return f
}

func start(t *testing.T, f *fixture, r accessapi.NativeRequest) accessapi.NativeStart {
	t.Helper()
	v, e := f.s.Start(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func login(t *testing.T, f *fixture, b string) accessapi.NativePage {
	t.Helper()
	p, e := f.s.Open(b, "")
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.s.Login(t.Context(), b, p.Session, p.CSRF, "alice", "password")
	if e != nil {
		t.Fatal(e)
	}
	if v.Session == p.Session || v.CSRF == p.CSRF {
		t.Fatal("login did not rotate capabilities")
	}
	return v
}

func approve(t *testing.T, f *fixture, b string) string {
	t.Helper()
	p := login(t, f, b)
	v, e := f.s.Consent(t.Context(), b, p.Session, p.CSRF, true)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func npmRequest() accessapi.NativeRequest {
	return accessapi.NativeRequest{Kind: "npm", Repository: "npm"}
}

func terraformRequest() accessapi.NativeRequest {
	return accessapi.NativeRequest{
		Kind:            "terraform",
		ClientID:        "terraform-cli",
		RedirectURI:     "http://localhost:10000/login",
		State:           "original state",
		Challenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		ChallengeMethod: "S256",
	}
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func TestUnitNativeLoginOneWinner(t *testing.T) {
	for _, kind := range []string{"npm", "terraform"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			r := npmRequest()
			if kind == "terraform" {
				r = terraformRequest()
			}
			st := start(t, f, r)
			if kind == "npm" {
				if _, e := f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(e, accessapi.ErrNativePending) {
					t.Fatal(e)
				}
			}
			redirect := approve(t, f, st.Browser)
			u, _ := url.Parse(redirect)
			code := u.Query().Get("code")
			if kind == "terraform" && (code == "" || u.Query().Get("state") != r.State) {
				t.Fatal("callback binding", redirect)
			}
			var wins atomic.Int64
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					var e error
					if kind == "npm" {
						_, e = f.s.Poll(t.Context(), st.Poll, "npm")
					} else {
						_, e = f.s.Exchange(t.Context(), accessapi.NativeExchange{Code: code, ClientID: r.ClientID, RedirectURI: r.RedirectURI, Verifier: verifier})
					}
					if e == nil {
						wins.Add(1)
					} else if !errors.Is(e, accessapi.ErrNativeLogin) {
						t.Error(e)
					}
				})
			}
			wg.Wait()
			if wins.Load() != 1 || f.tokens.count.Load() != 1 {
				t.Fatal("duplicate token", wins.Load(), f.tokens.count.Load())
			}
		})
	}
}

func TestUnitNativeLoginBindings(t *testing.T) {
	f := setup(t)
	st := start(t, f, npmRequest())
	p, e := f.s.Open(st.Browser, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []struct{ b, c, s string }{{"unknown", p.Session, p.CSRF}, {st.Browser, "foreign", p.CSRF}, {st.Browser, p.Session, "foreign"}} {
		if _, e := f.s.Login(t.Context(), bad.b, bad.c, bad.s, "alice", "password"); !errors.Is(e, accessapi.ErrNativeLogin) {
			t.Fatal(e)
		}
	}
	if _, e := f.s.Login(t.Context(), st.Browser, p.Session, p.CSRF, "alice", "wrong"); !errors.Is(e, kernel.ErrBadCredentials) {
		t.Fatal(e)
	}
	if _, e := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true); e == nil {
		t.Fatal("unauthenticated consent")
	}
	p = login(t, f, st.Browser)
	if _, err := f.s.Open(st.Browser, "copied-url-foreign-browser"); !errors.Is(err, accessapi.ErrNativeLogin) {
		t.Fatal("foreign GET adopted authenticated session", err)
	}
	if _, e := f.s.Poll(t.Context(), st.Poll, "another-repository"); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal(e)
	}
	if _, e := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, false); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal(e)
	}
	st = start(t, f, npmRequest())
	p = login(t, f, st.Browser)
	if e := f.s.Logout(st.Browser, p.Session, p.CSRF); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Logout(st.Browser, p.Session, p.CSRF); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal(e)
	}
}

func TestUnitNativeLoginExpiryAndCapacity(t *testing.T) {
	f := setup(t)
	st := start(t, f, terraformRequest())
	p := login(t, f, st.Browser)
	f.now = f.now.Add(approvalLifetime - time.Second)
	redirect, e := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(redirect)
	code := u.Query().Get("code")
	f.now = f.now.Add(2 * time.Second)
	if _, e := f.s.Exchange(
		t.Context(),
		accessapi.NativeExchange{Code: code, ClientID: "terraform-cli", RedirectURI: "http://localhost:10000/login", Verifier: verifier},
	); e != nil {
		t.Fatal("code inherited pending expiry", e)
	}
	f = setup(t)
	st = start(t, f, npmRequest())
	f.now = f.now.Add(approvalLifetime)
	if _, e := f.s.Open(st.Browser, ""); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal(e)
	}
	if _, e := f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal("expired pending poll", e)
	}
	for range maxApprovals {
		start(t, f, npmRequest())
	}
	if _, e := f.s.Start(t.Context(), npmRequest()); !errors.Is(e, accessapi.ErrNativeCapacity) {
		t.Fatal(e)
	}
	f.now = f.now.Add(approvalLifetime)
	start(t, f, npmRequest())
}

func TestUnitNativeLoginBootstrapFailureAndMintExpiry(t *testing.T) {
	for _, failure := range []string{"guard", "store", "repository", "missing"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			switch failure {
			case "guard":
				f.guardErr = errTest
			case "store":
				f.store.err = errTest
			case "repository":
				f.repoErr = errTest
			case "missing":
				f.s.d.Repository = nil
			}
			if _, err := f.s.Start(t.Context(), npmRequest()); err == nil {
				t.Fatal("unavailable bootstrap accepted")
			}
			if len(f.s.entries) != 0 {
				t.Fatal("failed bootstrap retained state")
			}
		})
	}
	for _, kind := range []string{"npm", "terraform"} {
		t.Run("mint_expiry_"+kind, func(t *testing.T) {
			f := setup(t)
			r := npmRequest()
			if kind == "terraform" {
				r = terraformRequest()
			}
			st := start(t, f, r)
			redirect := approve(t, f, st.Browser)
			f.s.d.Eligible = func(context.Context, kernel.Principal) error { f.now = f.now.Add(codeLifetime); return nil }
			var err error
			if kind == "npm" {
				_, err = f.s.Poll(t.Context(), st.Poll, "npm")
			} else {
				u, _ := url.Parse(redirect)
				_, err = f.s.Exchange(t.Context(), accessapi.NativeExchange{Code: u.Query().Get("code"), ClientID: r.ClientID, RedirectURI: r.RedirectURI, Verifier: verifier})
			}
			if !errors.Is(err, accessapi.ErrNativeLogin) || f.tokens.count.Load() != 0 {
				t.Fatal("mint crossed final deadline", err, f.tokens.count.Load())
			}
		})
	}
}

func TestUnitNativeLoginRejectsNonPasswordIdentity(t *testing.T) {
	for _, p := range []kernel.Principal{{Name: "alice", Anonymous: true, Password: true}, {Name: "alice", Identity: kernel.IdentityStored}, {Name: "alice", Password: true, Identity: kernel.IdentityTransient}} {
		f := setup(t)
		st := start(t, f, npmRequest())
		page, err := f.s.Open(st.Browser, "")
		if err != nil {
			t.Fatal(err)
		}
		f.s.d.Authenticate = func(context.Context, string, string) (kernel.Principal, error) { return p, nil }
		if _, err := f.s.Login(t.Context(), st.Browser, page.Session, page.CSRF, "alice", "password"); !errors.Is(err, accessapi.ErrNativeLogin) {
			t.Fatal("nonpassword identity admitted", p.Identity, err)
		}
	}
}

func TestUnitNativeLoginCurrentPolicy(t *testing.T) {
	for _, stage := range []string{"login", "consent", "redeem"} {
		for _, change := range []string{"identity", "repository", "owner", "permissions", "eligible", "guard", "store", "issuance"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				f := setup(t)
				st := start(t, f, npmRequest())
				p, e := f.s.Open(st.Browser, "")
				if e != nil {
					t.Fatal(e)
				}
				if stage != "login" {
					p = login(t, f, st.Browser)
				}
				if stage == "redeem" {
					_, e = f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true)
					if e != nil {
						t.Fatal(e)
					}
				}
				switch change {
				case "identity":
					f.auth.currentErr = kernel.ErrBadCredentials
				case "repository":
					f.repoErr = errTest
				case "owner":
					f.binding.Project = "new-owner"
				case "permissions":
					f.authorizer.visible = false
				case "eligible":
					f.eligibleErr = accessapi.ErrNativeLogin
				case "guard":
					f.guardErr = errTest
				case "store":
					f.store.err = errTest
				case "issuance":
					f.tokens.err = errTest
				}
				if stage == "login" {
					_, e = f.s.Login(t.Context(), st.Browser, p.Session, p.CSRF, "alice", "password")
				} else if stage == "consent" {
					_, e = f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true)
				} else {
					_, e = f.s.Poll(t.Context(), st.Poll, "npm")
				}
				if change != "issuance" || stage == "redeem" {
					if e == nil {
						t.Fatal("stale authority accepted")
					}
				}
				if stage == "redeem" {
					if _, e = f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(e, accessapi.ErrNativeLogin) {
						t.Fatal("failed issuance replay", e)
					}
				}
			})
		}
	}
}

func TestUnitNativeLoginInvalidRequests(t *testing.T) {
	f := setup(t)
	for _, r := range []accessapi.NativeRequest{{}, {Kind: "unknown"}, {Kind: "npm"}, {Kind: "npm", Repository: strings.Repeat("a", 256)}, {Kind: "npm", Repository: "npm", ClientID: "foreign"}, {Kind: "terraform"}} {
		if _, e := f.s.Start(t.Context(), r); !errors.Is(e, ErrInvalidRequest) {
			t.Fatal(r, e)
		}
	}
	r := terraformRequest()
	st := start(t, f, r)
	redirect := approve(t, f, st.Browser)
	u, _ := url.Parse(redirect)
	good := accessapi.NativeExchange{Code: u.Query().Get("code"), ClientID: r.ClientID, RedirectURI: r.RedirectURI, Verifier: verifier}
	for _, field := range []string{"code", "client", "redirect", "verifier"} {
		bad := good
		switch field {
		case "code":
			bad.Code = "other"
		case "client":
			bad.ClientID = "other"
		case "redirect":
			bad.RedirectURI = "http://localhost:10001/login"
		case "verifier":
			bad.Verifier = "wrong"
		}
		if _, e := f.s.Exchange(t.Context(), bad); !errors.Is(e, accessapi.ErrNativeLogin) {
			t.Fatal(field, e)
		}
	}
	f.now = f.now.Add(codeLifetime)
	if _, e := f.s.Exchange(t.Context(), good); !errors.Is(e, accessapi.ErrNativeLogin) {
		t.Fatal(e)
	}
	f = setup(t)
	st = start(t, f, terraformRequest())
	p := login(t, f, st.Browser)
	redirect, e := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, false)
	if e != nil {
		t.Fatal(e)
	}
	u, _ = url.Parse(redirect)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" {
		t.Fatal(redirect)
	}
}

func TestUnitNativeLoginConsentCannotRenewExpiredPending(t *testing.T) {
	for _, stage := range []string{"authority", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t)
			st := start(t, f, terraformRequest())
			p := login(t, f, st.Browser)
			if stage == "authority" {
				f.s.d.Eligible = func(context.Context, kernel.Principal) error { f.now = f.now.Add(approvalLifetime); return nil }
			} else {
				f.s.d.Store = expiryCommitStore{runner: f.store, advance: func() { f.now = f.now.Add(approvalLifetime) }}
			}
			if redirect, err := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true); !errors.Is(err, accessapi.ErrNativeLogin) || redirect != "" {
				t.Fatal("expired request renewed", redirect, err)
			}
			if f.tokens.count.Load() != 0 {
				t.Fatal("expired approval minted")
			}
		})
	}
}

type expiryCommitStore struct {
	runner  *testStore
	advance func()
}

func (s expiryCommitStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	err := s.runner.Tx(ctx, fn)
	s.advance()
	return err
}

func (s expiryCommitStore) AfterCommit(ctx context.Context, fn func()) { s.runner.AfterCommit(ctx, fn) }

func TestUnitNativeLoginPollDuringBrowserWork(t *testing.T) {
	for _, stage := range []string{"login", "consent"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t)
			st := start(t, f, npmRequest())
			p, err := f.s.Open(st.Browser, "")
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			wait := func() { close(entered); <-release }
			if stage == "login" {
				f.auth.basicHook = wait
			} else {
				p = login(t, f, st.Browser)
				f.s.d.Eligible = func(context.Context, kernel.Principal) error { wait(); return nil }
			}
			done := make(chan error, 1)
			go func() {
				if stage == "login" {
					var next accessapi.NativePage
					next, err = f.s.Login(t.Context(), st.Browser, p.Session, p.CSRF, "alice", "password")
					p = next
				} else {
					_, err = f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			if _, err := f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(err, accessapi.ErrNativePending) {
				t.Fatal("pending browser work terminated poll", err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			f.s.d.Eligible = func(context.Context, kernel.Principal) error { return nil }
			if stage == "login" {
				if _, err := f.s.Consent(t.Context(), st.Browser, p.Session, p.CSRF, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Poll(t.Context(), st.Poll, "npm"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Poll(t.Context(), st.Poll, "npm"); !errors.Is(err, accessapi.ErrNativeLogin) {
				t.Fatal(err)
			}
			if f.tokens.count.Load() != 1 {
				t.Fatal("duplicate mint")
			}
		})
	}
}
