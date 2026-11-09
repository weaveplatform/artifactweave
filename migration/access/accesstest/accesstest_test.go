package accesstest_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	auth "github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	errBoom = errors.New("boom")
	now     = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	libs    = accessapi.Repo{Key: "libs-local", Class: "local"}
)

// kitIssuer is the issuer of the tokens a kit's token service signs over a
// store that had no instance identity.
const kitIssuer = "jfac@01234567-89ab-4cde-8f01-23456789abcd"

func clock() time.Time { return now }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// reads reports whether d lets its subject read a file in repo.
func reads(d accessapi.Decider, repo accessapi.Repo) bool {
	return d != nil && d.Allows(accessapi.ResourceArtifact, repo, "a/b.jar", accessapi.ActionRead)
}

// readLibs returns a permission that lets the users and groups in g read
// libs.
func readLibs(name string, g types.Grants) types.Permission {
	return types.Permission{Name: name, Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {Actions: g, Targets: map[string]types.Target{libs.Key: {}}},
	}}
}

// recorder stands in for testing.TB where a test expects the kit to fail it:
// Fatal and Fatalf record the message and end the goroutine, as the real
// ones do, and Errorf records the message, without failing the surrounding
// test.
type recorder struct {
	testing.TB
	fatal  string
	errors []string
}

func (r *recorder) Fatal(args ...any) {
	r.fatal = fmt.Sprint(args...)
	runtime.Goexit()
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// fatalOf runs fn on its own goroutine, because Fatal ends the goroutine
// that calls it, and returns the message fn failed its tb with ("" when it
// did not).
func fatalOf(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	return r.fatal
}

// noPermissions is a store whose permission lookups fail, so every
// evaluation does.
type noPermissions struct{ accesstest.Store }

func (s noPermissions) Permissions() store.PermissionStore {
	return failingPermissions{s.Store.Permissions()}
}

type failingPermissions struct{ store.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]types.Permission, error) {
	return nil, errBoom
}

// noIdentity is a store whose instance identity cannot be read.
type noIdentity struct{ accesstest.Store }

func (s noIdentity) Instance() store.InstanceStore { return failingInstance{s.Store.Instance()} }

type failingInstance struct{ store.InstanceStore }

func (failingInstance) Get(context.Context) (types.InstanceIdentity, error) {
	return types.InstanceIdentity{}, errBoom
}

// noKeys is a store whose signing keys cannot be read.
type noKeys struct{ accesstest.Store }

func (s noKeys) Keys() store.KeyStore { return failingKeys{s.Store.Keys()} }

type failingKeys struct{ store.KeyStore }

func (failingKeys) SigningKeys(context.Context) ([]types.SigningKey, error) {
	return nil, errBoom
}

// stubVerifier answers every token with rec and records the tokens it saw.
type stubVerifier struct {
	rec  types.Token
	seen []string
}

func (v *stubVerifier) Verify(_ context.Context, raw string) (types.Token, error) {
	v.seen = append(v.seen, raw)
	return v.rec, nil
}

// A permission the test stores decides a read through Authorize and Decide
// alike, read when each call is made, and the principal Authorize stores has
// admin and groups as evaluated. Over keeps the test's store and settings,
// and Authorize keeps the binding's error text.
func TestUnitAuthorizeAndDecideReadTheTestStore(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	settings := accessapi.Settings{AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 3}
	k := accesstest.Over(t, st, accesstest.WithSettings(settings))
	if k.Store() != st || k.API().Settings != settings {
		t.Fatalf("Store = %v, Settings = %+v; want the test's", k.Store(), k.API().Settings)
	}
	must(t, st.Groups().Create(ctx, types.Group{Name: "readers"}))
	must(t, st.Groups().Create(ctx, types.Group{Name: "admins", AdminPrivileges: true}))
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}}
	subject := accessapi.SubjectOf(ann)

	check := func(want bool) {
		t.Helper()
		authorised, err := k.Authorize(ctx, ann, false)
		d, _ := accessapi.DeciderFrom(authorised)
		if err != nil || reads(d, libs) != want {
			t.Errorf("Authorize: reads libs = %t, %v; want %t", reads(d, libs), err, want)
		}
		d, err = k.Decide(ctx, subject, false)
		if err != nil || reads(d, libs) != want {
			t.Errorf("Decide: reads libs = %t, %v; want %t", reads(d, libs), err, want)
		}
	}
	check(false)
	must(t, st.Permissions().Create(ctx, readLibs("read-libs", types.Grants{
		Groups: map[string]types.Actions{"readers": types.ActionRead},
	})))
	check(true)

	// A member of a group with admin privileges is stored as an
	// administrator.
	bob := kernel.Principal{Name: "bob", Groups: []string{"admins"}}
	authorised, err := k.Authorize(ctx, bob, false)
	if p, ok := kernel.FromContext(authorised); err != nil || !ok || !p.Admin {
		t.Errorf("Authorize(bob) stored %+v, %t, %v; want an administrator", p, ok, err)
	}

	broken := accesstest.Over(t, noPermissions{inmem.New()})
	got, err := broken.Authorize(ctx, ann, false)
	if _, ok := kernel.FromContext(got); got != ctx || ok || !errors.Is(err, errBoom) ||
		!strings.HasPrefix(err.Error(), "auth: permissions: ") {
		t.Errorf("Authorize, permissions failing = %v, %v; want ctx and the binding's error",
			got, err)
	}
	if d, err := broken.Decide(ctx, subject, false); d != nil || !errors.Is(err, errBoom) {
		t.Errorf("Decide, permissions failing = %v, %v; want the evaluator's error", d, err)
	}
}

// With anonymous access off, the anonymous principal gets nothing, though a
// permission grants the anonymous user a read; with it on, the grant
// applies.
func TestUnitAnonymousAccessOffGrantsNothing(t *testing.T) {
	ctx := t.Context()
	k := accesstest.New(t)
	must(t, k.Store().Permissions().Create(ctx, readLibs("anonymous-reads", types.Grants{
		Users: map[string]types.Actions{kernel.AnonymousUser: types.ActionRead},
	})))
	for _, on := range []bool{false, true} {
		authorised, err := k.Authorize(ctx, kernel.Anonymous, on)
		p, _ := kernel.FromContext(authorised)
		d, _ := accessapi.DeciderFrom(authorised)
		if err != nil || !p.Anonymous || d == nil || d.IsAdmin() || reads(d, libs) != on {
			t.Errorf("Authorize, on %t: stored %+v, reads %t, %v", on, p, reads(d, libs), err)
		}
		d, err = k.Decide(ctx, accessapi.SubjectOf(kernel.Anonymous), on)
		if err != nil || reads(d, libs) != on {
			t.Errorf("Decide, on %t: reads %t, %v", on, reads(d, libs), err)
		}
	}
}

// The middleware authorises the principal the test maps each request to and
// stores it with its decider, which kernel.FromContext, accessapi.DeciderFrom
// and authn.DeciderFrom read back. A failed authorisation is reported on the
// test the middleware was given, and answers 500 without serving the
// handler.
func TestUnitMiddleware(t *testing.T) {
	ctx := t.Context()
	k := accesstest.New(t)
	must(t, k.Store().Groups().Create(ctx, types.Group{Name: "admins", AdminPrivileges: true}))
	must(t, k.Store().Permissions().Create(ctx, readLibs("read-libs", types.Grants{
		Users: map[string]types.Actions{"ann": types.ActionRead},
	})))
	principalOf := func(r *http.Request) kernel.Principal {
		return kernel.Principal{Name: r.Header.Get("X-User"), Groups: r.Header.Values("X-Group")}
	}
	request := func(user string, groups ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/artifactory/libs-local/a/b.jar", nil)
		r.Header.Set("X-User", user)
		for _, g := range groups {
			r.Header.Add("X-Group", g)
		}
		return r
	}
	type seen struct {
		p      kernel.Principal
		found  bool
		d      accessapi.Decider
		legacy accessapi.Decider
	}
	var got []seen
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, found := kernel.FromContext(r.Context())
		d, _ := accessapi.DeciderFrom(r.Context())
		got = append(got, seen{p: p, found: found, d: d, legacy: auth.DeciderFrom(r.Context())})
		w.WriteHeader(http.StatusNoContent)
	})

	h := k.Middleware(t, false, principalOf)(next)
	for _, r := range []*http.Request{request("ann"), request("bob", "admins"), request("carl")} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("%s: %d %s", r.Header.Get("X-User"), w.Code, w.Body)
		}
	}
	if len(got) != 3 {
		t.Fatalf("the handler ran %d times; want 3", len(got))
	}
	for i, want := range []struct {
		name         string
		admin, reads bool
	}{{"ann", false, true}, {"bob", true, true}, {"carl", false, false}} {
		s := got[i]
		if !s.found || s.p.Name != want.name || s.p.Admin != want.admin ||
			reads(s.d, libs) != want.reads || s.legacy == nil || s.legacy != s.d {
			t.Errorf("%s: stored %+v, %t, reads %t, decider %v; the legacy accessor's %v",
				want.name, s.p, s.found, reads(s.d, libs), s.d, s.legacy)
		}
	}

	failing := accesstest.Over(t, noPermissions{inmem.New()})
	rec := &recorder{TB: t}
	served := false
	h = failing.Middleware(rec, false, principalOf)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }),
	)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("ann"))
	if w.Code != http.StatusInternalServerError || served || len(rec.errors) != 1 ||
		!strings.Contains(rec.errors[0], "auth: permissions: ") || rec.fatal != "" {
		t.Errorf("a failed authorisation answered %d, served %t, reported %q, fatal %q",
			w.Code, served, rec.errors, rec.fatal)
	}
}

// WithTokens builds the real token service. The kit stores the fixed test
// identity when the store has none and keeps one it has, the first signing
// key exists once the kit is built, and a token the service issues verifies
// through the authenticator. A store that cannot give the identity or the
// keys fails the test. Without the option API().Tokens is nil and every
// token is refused.
func TestUnitWithTokens(t *testing.T) {
	ctx := t.Context()

	plain := accesstest.New(t)
	if plain.API().Tokens != nil {
		t.Error("API().Tokens is set without WithTokens")
	}
	_, err := plain.API().Authenticator.Token(ctx, "eyJ.a.b", "")
	if !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("a token without WithTokens: %v; want ErrBadCredentials", err)
	}

	k := accesstest.New(t, accesstest.WithTokens())
	if id, err := k.Store().Instance().Get(ctx); err != nil ||
		id.InstanceID != "jfrt@01234567-89ab-4cde-8f01-23456789abcd" {
		t.Errorf("instance identity = %+v, %v; want the kit's", id, err)
	}
	if keys, err := k.Store().Keys().SigningKeys(ctx); err != nil || len(keys) != 1 {
		t.Errorf("signing keys after New = %d, %v; want 1", len(keys), err)
	}
	must(t, k.Store().Users().Create(ctx, types.User{Name: "ann", Email: "ann@example.com"}))
	issued, err := k.API().Tokens.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := k.API().Authenticator.Token(ctx, issued.AccessToken, "")
	if err != nil || p.Name != "ann" || p.TokenID != issued.TokenID {
		t.Errorf("the token authenticates as %+v, %v; want ann by %s", p, err, issued.TokenID)
	}
	if rec, err := k.Verify(ctx, issued.AccessToken); err != nil ||
		rec.Subject != kitIssuer+"/users/ann" {
		t.Errorf("Verify = %+v, %v; want the kit's issuer", rec, err)
	}

	st := inmem.New()
	const own = "fedcba98-7654-4321-8fed-cba987654321"
	must(t, st.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@" + own, ProtocolVersion: 1,
	}))
	kept := accesstest.Over(t, st, accesstest.WithTokens())
	if id, err := st.Instance().Get(ctx); err != nil || id.InstanceID != "jfrt@"+own {
		t.Errorf("instance identity = %+v, %v; want the store's own, kept", id, err)
	}
	issued, err = kept.API().Tokens.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := kept.Verify(ctx, issued.AccessToken); err != nil ||
		rec.Subject != "jfac@"+own+"/users/ann" {
		t.Errorf("Verify = %+v, %v; want the issuer of the store's identity", rec, err)
	}

	for _, c := range []struct {
		name string
		st   accesstest.Store
		want string
	}{
		{"identity", noIdentity{inmem.New()}, "accesstest: instance identity: boom"},
		{"keys", noKeys{inmem.New()}, "accesstest: token service: token: signing keys: boom"},
	} {
		build := func(tb testing.TB) { accesstest.Over(tb, c.st, accesstest.WithTokens()) }
		if msg := fatalOf(t, build); msg != c.want {
			t.Errorf("a store whose %s fails: fatal %q; want %q", c.name, msg, c.want)
		}
	}
}

// Verify returns the stored record of a token API().Tokens issued, with its
// description, scope and lifetime on the kit's clock, and the service's
// error for a token that does not verify. On a kit without a token service
// it fails the test.
func TestUnitVerify(t *testing.T) {
	ctx := t.Context()
	k := accesstest.New(t, accesstest.WithTokens(), accesstest.WithNow(clock))
	scope, err := kernel.ParseScope("applied-permissions/user artifact:libs-local/**:r")
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	issued, err := k.API().Tokens.Issue(ctx, accessapi.TokenRequest{
		Username: "ann", Scope: scope, ExpiresIn: &hour, ForceRevocable: true, Description: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := k.Verify(ctx, issued.AccessToken)
	if err != nil || rec.ID != issued.TokenID || rec.Description != "ci" ||
		rec.Scope.String() != scope.String() || !rec.Revocable ||
		!rec.IssuedAt.Equal(now) || !rec.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("Verify = %+v, %v; want %s, \"ci\", %s, revocable, %s to %s",
			rec, err, issued.TokenID, scope, now, now.Add(time.Hour))
	}
	if _, err := k.Verify(ctx, "eyJ.a.b"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("Verify(malformed) = %v; want ErrInvalidToken", err)
	}

	msg := fatalOf(t, func(tb testing.TB) {
		_, _ = accesstest.New(tb).Verify(ctx, issued.AccessToken)
	})
	if msg != "accesstest: Verify needs a kit built with WithTokens" {
		t.Errorf("Verify without WithTokens: fatal %q", msg)
	}
}

// WithTokenVerifier's verifier answers the authenticator's token checks, with
// or without the token service, and the authenticator resolves the record as
// app's does: a transient user who is not stored joins the store's auto-join
// groups.
func TestUnitWithTokenVerifier(t *testing.T) {
	ctx := t.Context()
	v := &stubVerifier{rec: types.Token{
		ID: "stub", Subject: kitIssuer + "/users/visitor", Scope: types.UserScope(),
		Identity: types.IdentityTransient,
	}}

	k := accesstest.New(t, accesstest.WithTokenVerifier(v))
	must(t, k.Store().Groups().Create(ctx, types.Group{Name: "joiners", AutoJoin: true}))
	p, err := k.API().Authenticator.Token(ctx, "raw", "")
	if err != nil || p.Name != "visitor" || !slices.Equal(p.Groups, []string{"joiners"}) ||
		!slices.Equal(v.seen, []string{"raw"}) {
		t.Errorf("Token = %+v, %v, the verifier saw %q; want visitor in joiners", p, err, v.seen)
	}
	if k.API().Tokens != nil {
		t.Error("API().Tokens is set without WithTokens")
	}

	both := accesstest.New(t, accesstest.WithTokens(), accesstest.WithTokenVerifier(v))
	issued, err := both.API().Tokens.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = both.API().Authenticator.Token(ctx, issued.AccessToken, "")
	if err != nil || p.Name != "visitor" || v.seen[len(v.seen)-1] != issued.AccessToken {
		t.Errorf("Token with WithTokens too = %+v, %v; want the verifier's answer", p, err)
	}
}

// HashPassword hashes at the cheap cost, and the hash passes the kit's
// Basic, which checks the API restrictions in the store as app's does.
func TestUnitHashPassword(t *testing.T) {
	ctx := t.Context()
	k := accesstest.New(t, accesstest.WithNow(clock))
	hash := accesstest.HashPassword(t, "pw")
	if !strings.Contains(string(hash), "$m=64,t=1,p=1$") {
		t.Errorf("hash %s; want the cost m=64,t=1,p=1", hash)
	}
	for _, name := range []string{"ann", "bob"} {
		must(t, k.Store().Users().Create(ctx, types.User{Name: name, PasswordHash: hash}))
	}
	authn := k.API().Authenticator
	if p, err := authn.Basic(ctx, "ann", "pw"); err != nil || p.Name != "ann" || !p.Password {
		t.Errorf("Basic(ann) = %+v, %v; want ann by password", p, err)
	}
	if _, err := authn.Basic(ctx, "ann", "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("Basic(ann, wrong) = %v; want ErrBadCredentials", err)
	}
	_, err := k.Store().APIRestrictions().Put(ctx, store.APIRestriction{
		User: "bob", Actor: "admin", Reason: "audit", Changed: now, Until: now.Add(time.Hour),
	})
	must(t, err)
	if _, err := authn.Basic(ctx, "bob", "pw"); !errors.Is(err, kernel.ErrAPIRestricted) {
		t.Errorf("Basic(bob) while restricted = %v; want ErrAPIRestricted", err)
	}
}

// While failed logins suspend a user name, every password answers an error
// matching ErrLoginSuspended, the authenticator's own sentinel, until the
// kit's clock passes the suspension.
func TestUnitErrLoginSuspended(t *testing.T) {
	ctx := t.Context()
	at := now
	k := accesstest.New(t, accesstest.WithNow(func() time.Time { return at }))
	must(t, k.Store().Settings().Put(ctx, types.SettingAccessLockSettings, types.AccessLockSettings{
		ClientLoginSuspensionEnabled: true, MaxLoginDelayIncorrectAttempts: 1,
		BaseLoginDelayMillis: 60_000, MaxLoginDelayMillis: 60_000,
	}))
	must(t, k.Store().Users().Create(ctx, types.User{
		Name: "ann", PasswordHash: accesstest.HashPassword(t, "pw"),
	}))
	authn := k.API().Authenticator
	if _, err := authn.Basic(ctx, "ann", "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Basic(ann, wrong) = %v; want ErrBadCredentials", err)
	}
	_, err := authn.Basic(ctx, "ann", "pw")
	if !errors.Is(err, accesstest.ErrLoginSuspended) || !errors.Is(err, auth.ErrLoginSuspended) {
		t.Errorf("Basic(ann) while suspended = %v; want ErrLoginSuspended", err)
	}
	at = at.Add(time.Minute)
	if p, err := authn.Basic(ctx, "ann", "pw"); err != nil || p.Name != "ann" {
		t.Errorf("Basic(ann) once the suspension has passed = %+v, %v", p, err)
	}
}
