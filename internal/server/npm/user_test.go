package npm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// cheap is a password hash cost that keeps the tests fast; every user of
// the accounts fixture has password "pw".
var cheap = auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

// accounts is the npm fixture with login and logout served by a real
// authenticator and token service, over users:
//   - ann, enabled, password "pw";
//   - locked, whose account is locked;
//   - expired, whose password an administrator expired;
//   - external, whose internal password is disabled;
//   - off, a disabled user.
type accounts struct {
	*fixture
	auth   *auth.Authenticator
	tokens *token.Service
}

// newAccounts builds the fixture; settings are stored first, so the
// authenticator reads them on its first login.
func newAccounts(t *testing.T, settings map[string]any, expiry time.Duration) *accounts {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t)
	for k, v := range settings {
		if err := f.db.Settings().Put(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := auth.HashPasswordWith(cheap, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []types.User{
		{Name: "ann", PasswordHash: hash},
		{Name: "locked", PasswordHash: hash, Status: types.UserLocked},
		{Name: "expired", PasswordHash: hash, PasswordExpired: true},
		{Name: "external", PasswordHash: hash, InternalPasswordDisabled: true},
		{Name: "off", PasswordHash: hash, Status: types.UserDisabled},
	} {
		if err := f.db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, f.db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(f.db.Users(), f.db.Settings(), auth.Options{
		Hash: cheap, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.remount(t, npm.Options{Accounts: npm.Accounts{
		Passwords: a, Tokens: tokens, TokenExpiry: expiry,
	}})
	return &accounts{fixture: f, auth: a, tokens: tokens}
}

const loginPath = registry + "-/user/org.couchdb.user:"

// login sends npm-profile's loginCouch body for name with password.
func (f *accounts) login(name, password string) *httptest.ResponseRecorder {
	body := `{"_id":"org.couchdb.user:` + name + `","name":"` + name + `","password":"` +
		password + `","type":"user","roles":[],"date":"2026-10-19T09:00:00.000Z"}`
	return f.send("PUT", loginPath+name, "", strings.NewReader(body))
}

// tokenOf is the token a successful login answered.
func tokenOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	wantStatus(t, rec, http.StatusCreated)
	tok, _ := decode(t, rec)["token"].(string)
	if tok == "" {
		t.Fatalf("no token: %s", rec.Body)
	}
	return tok
}

// A legacy login answers 201 with an access token that authenticates as
// the user with the user's own permissions, lasts npm.login_token_expiry,
// and can be revoked whatever its lifetime.
func TestAccLogin(t *testing.T) {
	for _, c := range []struct {
		expiry, want time.Duration
	}{
		{0, npm.DefaultLoginTokenExpiry},
		{time.Hour, time.Hour},
	} {
		f := newAccounts(t, nil, c.expiry)
		rec := f.login("ann", "pw")
		got := decode(t, rec)
		tok := tokenOf(t, rec)
		if got["ok"] != true || got["id"] != "org.couchdb.user:ann" {
			t.Errorf("answer %s", rec.Body)
		}
		rec2, err := f.tokens.Verify(context.Background(), tok)
		if err != nil {
			t.Fatal(err)
		}
		if !rec2.Revocable || rec2.Description != "npm login" ||
			rec2.Scope.String() != types.UserScope().String() ||
			rec2.ExpiresAt.Sub(rec2.IssuedAt) != c.want {
			t.Errorf("expiry %s: token %+v", c.expiry, rec2)
		}
		p, err := f.auth.Token(context.Background(), tok, "")
		if err != nil || p.Name != "ann" || p.Admin {
			t.Errorf("token authenticates as %+v, %v", p, err)
		}
	}
}

// The user is the one the path names, encoded as npm-profile encodes it;
// the body must name the same user.
func TestAccLoginNames(t *testing.T) {
	f := newAccounts(t, nil, 0)
	ctx := context.Background()
	hash, _ := auth.HashPasswordWith(cheap, "pw")
	if err := f.db.Users().
		Create(ctx, types.User{Name: "a@b.example", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"a@b.example","password":"pw"}`
	rec := f.send("PUT", loginPath+"a%40b.example", "", strings.NewReader(body))
	tokenOf(t, rec)
	for _, c := range []struct{ path, body string }{
		{loginPath + "ann", `{"name":"bob","password":"pw"}`},
		{loginPath + "ann", `{"password":"pw"}`},
		{loginPath + "ann", `{"name":"ann","password":`},
		{loginPath + "ann", `["ann"]`},
	} {
		rec := f.send("PUT", c.path, "", strings.NewReader(c.body))
		wantStatus(t, rec, http.StatusBadRequest)
		if decode(t, rec)["error"] == nil {
			t.Errorf("%s %s: not npm's envelope: %s", c.path, c.body, rec.Body)
		}
	}
	// A path naming no user is no login route.
	wantStatus(t, f.send("PUT", loginPath, "", strings.NewReader(`{"name":""}`)),
		http.StatusMethodNotAllowed)
	big := `{"name":"ann","password":"` + strings.Repeat("x", 64<<10) + `"}`
	wantStatus(t, f.send("PUT", loginPath+"ann", "", strings.NewReader(big)),
		http.StatusRequestEntityTooLarge)
}

// A login's password meets every check a Basic login's does, with the
// same answers: never 400 or 409, which npm reads as "no such user" and
// "update the user".
func TestAccLoginRefusals(t *testing.T) {
	f := newAccounts(t, nil, 0)
	for _, c := range []struct {
		name, password string
		want           int
		msg            string
	}{
		{"ann", "wrong", http.StatusUnauthorized, "Bad credentials"},
		{"nobody-here", "pw", http.StatusUnauthorized, "Bad credentials"},
		{"ann", "", http.StatusUnauthorized, "Bad credentials"},
		{"locked", "pw", http.StatusForbidden, "User is Locked"},
		{"expired", "pw", http.StatusForbidden, "Password has expired"},
		{"external", "pw", http.StatusUnauthorized, "Bad credentials"},
		{"off", "pw", http.StatusUnauthorized, "Bad credentials"},
	} {
		wantError(t, f.login(c.name, c.password), c.want, c.msg)
	}
	issued, _, err := f.db.Tokens().List(context.Background(), store.TokenFilter{}, store.Page{})
	if err != nil || len(issued) != 0 {
		t.Errorf("refused logins issued tokens: %v, %v", issued, err)
	}
}

// While basic authentication is switched off no password logs in, as no
// Basic login does.
func TestAccLoginBasicAuthDisabled(t *testing.T) {
	f := newAccounts(t, map[string]any{types.SettingBasicAuthEnabled: false}, 0)
	wantError(t, f.login("ann", "pw"), http.StatusUnauthorized, "Basic authentication")
}

// token.allow_basic_auth governs the Access token API alone; the server
// gives the npm handlers no say in it, so a login succeeds whatever it is.
// Failed logins count toward the user lock as Basic ones do.
func TestAccLoginCountsFailures(t *testing.T) {
	f := newAccounts(t, map[string]any{
		types.SettingUserLockPolicy:     types.UserLockPolicy{Enabled: true, LoginAttempts: 2},
		types.SettingAccessLockSettings: types.AccessLockSettings{},
	}, 0)
	for range 2 {
		wantError(t, f.login("ann", "wrong"), http.StatusUnauthorized, "Bad credentials")
	}
	wantError(t, f.login("ann", "pw"), http.StatusForbidden, "User is Locked")
	if _, err := f.auth.Basic(
		context.Background(),
		"ann",
		"pw",
	); !errors.Is(
		err,
		auth.ErrAccountLocked,
	) {
		t.Errorf("Basic after the failed logins: %v", err)
	}
}

// Failed logins suspend the name as failed Basic logins do, and while the
// suspension lasts even the right password is refused.
func TestAccLoginSuspension(t *testing.T) {
	f := newAccounts(t, map[string]any{
		types.SettingAccessLockSettings: types.AccessLockSettings{
			ClientLoginSuspensionEnabled: true, MaxLoginDelayIncorrectAttempts: 1,
			BaseLoginDelayMillis: 60_000, MaxLoginDelayMillis: 60_000,
		},
	}, 0)
	wantError(t, f.login("ann", "wrong"), http.StatusUnauthorized, "Bad credentials")
	wantError(t, f.login("ann", "pw"), http.StatusForbidden, "recurrent login failures")
	_, err := f.auth.Basic(context.Background(), "ann", "pw")
	if !errors.Is(err, auth.ErrLoginSuspended) {
		t.Errorf("Basic during the suspension: %v", err)
	}
}

// A token sent as the password logs in as the stored user it acts as, as a
// Basic password that is a token does. A token narrower than its user, a
// registry token or a transient user's would gain a broader credential, and
// is refused as wrong credentials.
func TestAccLoginWithToken(t *testing.T) {
	f := newAccounts(t, nil, 0)
	ctx := context.Background()
	hour := time.Hour
	issue := func(user, scope string, id types.TokenIdentity) token.Issued {
		t.Helper()
		s, err := types.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		issued, err := f.tokens.Issue(ctx, token.Request{
			Username: user, Scope: s, ExpiresIn: &hour, ByAdmin: true, Identity: id,
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued
	}
	whole := issue("ann", "applied-permissions/user", types.IdentityStored)
	tokenOf(t, f.login("ann", whole.AccessToken))
	encrypted, err := f.tokens.EncryptedPassword(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	tokenOf(t, f.login("ann", encrypted))
	registryToken, err := f.tokens.IssueRegistry(ctx, token.RegistryRequest{
		Username: "ann", ExpiresIn: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"narrowed":  issue("ann", "applied-permissions/groups:readers", types.IdentityStored).AccessToken,
		"registry":  registryToken.AccessToken,
		"transient": issue("ghost", "applied-permissions/user", types.IdentityTransient).AccessToken,
		"other":     whole.AccessToken,
	} {
		user := "ann"
		if name == "transient" {
			user = "ghost"
		}
		if name == "other" {
			user = "locked"
		}
		wantError(t, f.login(user, raw), http.StatusUnauthorized, "Bad credentials")
	}
}

// Without an authenticator or a token service there is nothing to log in
// with: 501.
func TestAccLoginNotServed(t *testing.T) {
	f := newFixture(t)
	rec := f.send("PUT", loginPath+"ann", "", strings.NewReader(`{"name":"ann","password":"pw"}`))
	wantStatus(t, rec, http.StatusNotImplemented)
	// Logout has nothing to revoke, and still lets npm forget its token.
	wantStatus(t, f.do("DELETE", registry+"-/user/token/abc", ""), http.StatusOK)
}

// A token service that fails to issue is a fault.
func TestAccLoginIssueFailure(t *testing.T) {
	f := newAccounts(t, nil, 0)
	f.remount(t, npm.Options{Accounts: npm.Accounts{
		Passwords: f.auth, Tokens: failingTokens{},
	}})
	wantStatus(t, f.login("ann", "pw"), http.StatusInternalServerError)
	if len(f.faults) != 1 || !errors.Is(f.faults[0], errTokens) {
		t.Errorf("faults %v", f.faults)
	}
}

// Logout revokes the token its path names: the token npm logged in with
// stops authenticating at once. It answers 200 whatever it finds, so npm
// forgets a token that is already gone too.
func TestAccLogout(t *testing.T) {
	f := newAccounts(t, nil, 0)
	ctx := context.Background()
	tok := tokenOf(t, f.login("ann", "pw"))
	rec := f.do("DELETE", registry+"-/user/token/"+tok, "")
	wantStatus(t, rec, http.StatusOK)
	if decode(t, rec)["ok"] != true {
		t.Errorf("answer %s", rec.Body)
	}
	if _, err := f.auth.Token(ctx, tok, ""); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("token after logout: %v", err)
	}
	// Gone already, never a token, and a reference token.
	wantStatus(t, f.do("DELETE", registry+"-/user/token/"+tok, ""), http.StatusOK)
	wantStatus(t, f.do("DELETE", registry+"-/user/token/not-a-token", ""), http.StatusOK)
	ref, err := f.tokens.EncryptedPassword(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("DELETE", registry+"-/user/token/"+ref, ""), http.StatusOK)
	if _, err := f.auth.Token(ctx, ref, "ann"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("reference token after logout: %v", err)
	}
}

// A token that cannot be revoked, shorter than the revocable threshold
// and neither forced revocable nor refreshable, stays until it expires; a
// login's token is always revocable, so this is never one npm logged in
// with.
func TestAccLogoutOfIrrevocableToken(t *testing.T) {
	f := newAccounts(t, nil, 0)
	ctx := context.Background()
	hour := time.Hour
	issued, err := f.tokens.Issue(ctx, token.Request{Username: "ann", ExpiresIn: &hour})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("DELETE", registry+"-/user/token/"+issued.AccessToken, ""), http.StatusOK)
	if _, err := f.auth.Token(ctx, issued.AccessToken, ""); err != nil {
		t.Errorf("irrevocable token after logout: %v", err)
	}
}

// A store that fails to revoke is a fault, not a logout.
func TestAccLogoutFailure(t *testing.T) {
	f := newFixture(t)
	f.remount(t, npm.Options{Accounts: npm.Accounts{Tokens: failingTokens{}}})
	wantStatus(t, f.do("DELETE", registry+"-/user/token/abc", ""), http.StatusInternalServerError)
	if len(f.faults) != 1 || !errors.Is(f.faults[0], errTokens) {
		t.Errorf("faults %v", f.faults)
	}
}

var errTokens = errors.New("token store down")

// failingTokens is a token service whose store is down.
type failingTokens struct{}

func (failingTokens) Issue(context.Context, token.Request) (token.Issued, error) {
	return token.Issued{}, errTokens
}

func (failingTokens) RevokeValue(context.Context, string) error { return errTokens }
