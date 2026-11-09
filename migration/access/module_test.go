package access_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/authz"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testHash keeps password checks cheap; the bindings add nothing to them.
var testHash = authn.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

var (
	errBoom     = errors.New("boom")
	errRollback = errors.New("roll back")
	now         = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	libs        = accessapi.Repo{Key: "libs-local", Class: "local"}
	public      = accessapi.Repo{Key: "public-local", Class: "local"}
)

func clock() time.Time { return now }

// world is the module as app builds it: over a store holding the instance
// identity and ann (password "secret", in readers), with the authenticator
// and the token service built over the same store.
type world struct {
	st  *inmem.Store
	api accessapi.API
	ann types.User
}

func newWorld(t *testing.T) world {
	t.Helper()
	ctx := t.Context()
	st := inmem.New()
	if err := st.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	hash, err := authn.HashPasswordWith(testHash, "secret")
	if err != nil {
		t.Fatal(err)
	}
	must(t, st.Groups().Create(ctx, types.Group{Name: "readers"}))
	must(t, st.Users().Create(ctx, types.User{
		Name: "ann", Email: "ann@example.com", PasswordHash: hash, Groups: []string{"readers"},
		Status: types.UserEnabled,
	}))
	ann, err := st.Users().Get(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, st, token.Options{Now: clock, KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(st.Users(), st.Settings(), authn.Options{
		Hash: testHash, Now: clock, Tokens: tokens, Groups: st.Groups(),
		APIRestrictions: st.APIRestrictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := access.New(access.Deps{Store: st, Authenticator: a, Tokens: tokens})
	if err != nil {
		t.Fatal(err)
	}
	return world{st: st, api: m.API(), ann: ann}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// contracts returns the names of api's contract fields that are nil.
func contracts(api accessapi.API) (unset []string) {
	v := reflect.ValueOf(api)
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.Interface && f.IsNil() {
			unset = append(unset, v.Type().Field(i).Name)
		}
	}
	return unset
}

// New binds every contract without touching the store, and leaves the
// services it was not given nil, never typed nil.
func TestUnitNew(t *testing.T) {
	if m, err := access.New(access.Deps{}); m != nil || err == nil ||
		err.Error() != "access: Deps.Store is required" {
		t.Errorf("New without a store = %v, %v; want the required-store error", m, err)
	}

	// Any call on this store panics, so New succeeding shows it does no I/O.
	settings := accessapi.Settings{AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 12}
	m, err := access.New(access.Deps{Store: struct{ store.Store }{}, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	api := m.API()
	if unset := contracts(api); !reflect.DeepEqual(unset, []string{"Authenticator", "Tokens"}) {
		t.Errorf("without services, nil contracts = %v; want [Authenticator Tokens]", unset)
	}
	if api.Authenticator != nil || api.Tokens != nil {
		t.Error("an absent service is a non-nil interface")
	}
	if api.Settings != settings {
		t.Errorf("Settings = %+v, want %+v", api.Settings, settings)
	}

	if unset := contracts(newWorld(t).api); unset != nil {
		t.Errorf("with services, nil contracts = %v; want none", unset)
	}
}

func TestUnitAuthenticatorBinding(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	authn := w.api.Authenticator

	if p, err := authn.Basic(ctx, "ann", "secret"); err != nil || p.Name != "ann" || !p.Password {
		t.Errorf("Basic = %+v, %v; want ann by password", p, err)
	}
	if _, err := authn.Basic(ctx, "ann", "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("Basic with a wrong password = %v, want ErrBadCredentials", err)
	}
	if on, err := authn.AnonymousAllowed(ctx); on || err != nil {
		t.Errorf("AnonymousAllowed, never written = %v, %v; want false, nil", on, err)
	}
	must(t, w.st.Settings().Put(ctx, types.SettingAnonymousAccess, true))
	if on, err := authn.AnonymousAllowed(ctx); !on || err != nil {
		t.Errorf("AnonymousAllowed, switched on = %v, %v; want true, nil", on, err)
	}

	issued, err := w.api.Tokens.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	tp, err := authn.Token(ctx, issued.AccessToken, "")
	if err != nil || tp.Name != "ann" || tp.TokenID != issued.TokenID {
		t.Fatalf("Token = %+v, %v; want ann's token %s", tp, err, issued.TokenID)
	}

	// The rechecks read the token records of the module's store.
	parent, err := w.st.Tokens().Get(ctx, issued.TokenID)
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.Principal{
		Name: "ann", IsTokenAuthenticated: true, Registry: &kernel.RegistryAccess{},
		TokenID: "registry-1", TokenExpires: now.Add(time.Hour), TokenParent: parent.ID,
		TokenSubject: parent.Subject, Scope: parent.Scope,
		PasswordRevision: sha256.Sum256(w.ann.PasswordHash),
	}
	ref := kernel.DownloadCredential{Kind: "token", TokenID: issued.TokenID}
	if p, err := authn.Current(ctx, tp); err != nil || p.TokenID != issued.TokenID {
		t.Errorf("Current = %+v, %v; want ann's token", p, err)
	}
	if p, err := authn.CurrentRegistry(ctx, registry); err != nil || p.TokenParent != parent.ID {
		t.Errorf("CurrentRegistry = %+v, %v; want the registry token of ann's token", p, err)
	}
	if p, err := authn.RestoreDownload(ctx, "ann", ref); err != nil || p.TokenID != issued.TokenID {
		t.Errorf("RestoreDownload = %+v, %v; want ann's token", p, err)
	}

	must(t, w.api.Tokens.RevokeValue(ctx, issued.AccessToken))
	_, gone := w.st.Tokens().Get(ctx, issued.TokenID)
	for _, c := range []struct {
		what, prefix string
		err          error
	}{
		{"Current", "auth: current token: ", second(authn.Current(ctx, tp))},
		{"CurrentRegistry", "auth: registry parent: ", second(authn.CurrentRegistry(ctx, registry))},
		// RestoreDownload rechecks with Current, so its error reads the same.
		{"RestoreDownload", "auth: current token: ", second(authn.RestoreDownload(ctx, "ann", ref))},
	} {
		if c.err == nil || c.err.Error() != c.prefix+gone.Error() ||
			!errors.Is(c.err, kernel.ErrNotFound) {
			t.Errorf(
				"%s of a revoked token = %v, want %q and the store's %v",
				c.what,
				c.err,
				c.prefix,
				gone,
			)
		}
	}

	// A token issued inside a transaction is seen inside it and gone after
	// a rollback: both bindings use the caller's context.
	var inside kernel.Principal
	err = w.st.Tx(ctx, func(ctx context.Context) error {
		in, err := w.api.Tokens.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
		if err != nil {
			return err
		}
		inside = kernel.Principal{Name: "ann", TokenID: in.TokenID}
		if _, err := authn.Current(ctx, inside); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("transaction = %v, want the rollback", err)
	}
	if _, err := authn.Current(ctx, inside); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("Current after the rollback = %v, want ErrNotFound", err)
	}
}

func TestUnitAuthorizerBinding(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	must(t, w.st.Groups().Create(ctx, types.Group{Name: "admins", AdminPrivileges: true}))
	must(t, w.st.Permissions().Create(ctx, permission("read-libs", map[string]types.Target{
		libs.Key: {},
	}, types.Grants{Groups: map[string]types.Actions{"readers": types.ActionRead}})))
	must(t, w.st.Permissions().Create(ctx, permission("anonymous-public", map[string]types.Target{
		public.Key: {},
	}, types.Grants{Users: map[string]types.Actions{kernel.AnonymousUser: types.ActionRead}})))
	az := w.api.Authorizer
	src := authz.Sources{Permissions: w.st.Permissions(), Groups: w.st.Groups()}
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}, Password: true}

	// Authorize is auth.Authorize, with the evaluator itself as the Decider.
	p, d, err := az.Authorize(ctx, ann, false)
	wantP, _, wantErr := authn.Authorize(ctx, src, ann)
	if err != nil || wantErr != nil || !reflect.DeepEqual(p, wantP) {
		t.Fatalf("Authorize = %+v, %v; want %+v, %v", p, err, wantP, wantErr)
	}
	if _, ok := authz.FromAPI(d); !ok || !reads(d, libs) || d.Allows(
		accessapi.ResourceArtifact, libs, "a/b.jar", accessapi.ActionWrite,
	) {
		t.Errorf(
			"Authorize's Decider %T reads libs %v; want the evaluator, reading only",
			d,
			reads(d, libs),
		)
	}
	if p, _, _ := az.Authorize(
		ctx,
		kernel.Principal{Name: "ann", Groups: []string{"admins"}},
		false,
	); !p.Admin {
		t.Error("Authorize kept a member of an admin-privilege group from administering")
	}
	// The anonymous user's grants apply only while anonymous access is on.
	anonymous := kernel.Principal{Name: kernel.AnonymousUser, Anonymous: true}
	for _, on := range []bool{false, true} {
		if _, d, err := az.Authorize(ctx, anonymous, on); err != nil || reads(d, public) != on {
			t.Errorf("Authorize(anonymous, %v) reads public = %v, %v", on, reads(d, public), err)
		}
	}

	// For reads the anonymous access setting, which counts as off until
	// written.
	if d, err := az.For(ctx, anonymous); err != nil || reads(d, public) {
		t.Errorf(
			"For(anonymous), setting never written: reads public = %v, %v; want false",
			reads(d, public),
			err,
		)
	}
	must(t, w.st.Settings().Put(ctx, types.SettingAnonymousAccess, true))
	if d, err := az.For(ctx, anonymous); err != nil || !reads(d, public) {
		t.Errorf(
			"For(anonymous), setting on: reads public = %v, %v; want true",
			reads(d, public),
			err,
		)
	}
	if d, err := az.For(ctx, ann); err != nil || !reads(d, libs) || reads(d, public) {
		t.Errorf("For(ann) = %v, %v; want ann's grants only", d, err)
	}

	subject := accessapi.Subject{Name: "ann", Groups: []string{"readers"}}
	if d, err := az.Decide(ctx, subject, false); err != nil || !reads(d, libs) {
		t.Errorf("Decide(ann) = %v, %v; want ann's grants", d, err)
	}

	// A permission created inside a transaction counts inside it only.
	err = w.st.Tx(ctx, func(ctx context.Context) error {
		if err := w.st.Permissions().Create(ctx, permission("write-libs", map[string]types.Target{
			libs.Key: {},
		}, types.Grants{Users: map[string]types.Actions{"ann": types.ActionWrite}})); err != nil {
			return err
		}
		d, err := az.Decide(ctx, subject, false)
		if err != nil || !d.Allows(accessapi.ResourceArtifact, libs, "a", accessapi.ActionWrite) {
			t.Errorf("Decide inside the transaction = %v, %v; want ann may write", d, err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("transaction = %v, want the rollback", err)
	}
	if d, _ := az.Decide(ctx, subject, false); d.Allows(
		accessapi.ResourceArtifact, libs, "a", accessapi.ActionWrite,
	) {
		t.Error("Decide after the rollback still lets ann write")
	}
}

// Each evaluation's error keeps the text of the code the binding replaces,
// and comes with a nil Decider.
func TestUnitAuthorizerErrors(t *testing.T) {
	ctx := t.Context()
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}}
	bad := scripted{Store: inmem.New(), permErr: errBoom}
	az := module(t, bad).Authorizer
	src := authz.Sources{Permissions: bad.Permissions(), Groups: bad.Groups()}
	_, evalErr := authz.New(ctx, src, accessapi.SubjectOf(ann))
	_, _, authorizeErr := authn.Authorize(ctx, src, ann)
	if evalErr == nil || authorizeErr == nil {
		t.Fatal("the failing store did not fail the evaluator")
	}

	p, d, err := az.Authorize(ctx, ann, false)
	if d != nil || !reflect.DeepEqual(p, kernel.Principal{}) || err == nil ||
		err.Error() != authorizeErr.Error() || !strings.HasPrefix(err.Error(), "auth: permissions: ") ||
		!errors.Is(err, errBoom) {
		t.Errorf("Authorize = %+v, %v, %v; want zero, nil, %v", p, d, err, authorizeErr)
	}
	d, err = az.For(ctx, ann)
	if d != nil || err == nil || err.Error() != "evaluate permissions: "+evalErr.Error() ||
		!errors.Is(err, errBoom) {
		t.Errorf("For = %v, %v; want nil, evaluate permissions: %v", d, err, evalErr)
	}
	d, err = az.Decide(ctx, accessapi.SubjectOf(ann), false)
	if d != nil || err == nil || err.Error() != evalErr.Error() || !errors.Is(err, errBoom) {
		t.Errorf("Decide = %v, %v; want nil and the evaluator's %v unwrapped", d, err, evalErr)
	}

	d, err = module(t, scripted{Store: inmem.New(), settingsErr: errBoom}).Authorizer.For(ctx, ann)
	if d != nil || err == nil || err.Error() != "anonymous access setting: boom" ||
		!errors.Is(err, errBoom) {
		t.Errorf(
			"For with an unreadable setting = %v, %v; want nil, anonymous access setting: boom",
			d,
			err,
		)
	}
}

func TestUnitTokensBinding(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	tk := w.api.Tokens

	hour := time.Hour
	issued, err := tk.Issue(ctx, accessapi.TokenRequest{
		Username: "ann", ExpiresIn: &hour, Refreshable: true, IncludeReference: true,
		Description: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := w.st.Tokens().Get(ctx, issued.TokenID)
	if err != nil || rec.Description != "ci" || !issued.IssuedAt.Equal(rec.IssuedAt) ||
		issued.ExpiresIn != 3600 || issued.AccessToken == "" || issued.RefreshToken == "" ||
		issued.ReferenceToken == "" {
		t.Errorf(
			"Issue = %+v and record %+v, %v; want every field of the request and the record",
			issued,
			rec,
			err,
		)
	}
	if _, err := tk.Issue(ctx, accessapi.TokenRequest{}); !errors.Is(err, token.ErrInvalidRequest) {
		t.Errorf("Issue without a user = %v, want the service's ErrInvalidRequest", err)
	}

	reg, err := tk.IssueRegistry(ctx, accessapi.RegistryTokenRequest{
		Username: "ann", ExpiresIn: 10 * time.Minute, Parent: issued.TokenID,
	})
	if err != nil || reg.AccessToken == "" || reg.TokenID == "" || reg.ExpiresIn != 600 ||
		!reg.IssuedAt.Equal(now) {
		t.Errorf("IssueRegistry = %+v, %v; want a ten-minute token issued now", reg, err)
	}
	if _, err := tk.IssueRegistry(ctx, accessapi.RegistryTokenRequest{
		Username: "ann", ExpiresIn: 2 * time.Hour,
	}); !errors.Is(err, token.ErrInvalidRequest) {
		t.Errorf("IssueRegistry for two hours = %v, want the service's ErrInvalidRequest", err)
	}

	pw, err := tk.EncryptedPassword(ctx, "ann")
	again, err2 := tk.EncryptedPassword(ctx, "ann")
	if err != nil || err2 != nil || pw == "" || pw != again {
		t.Errorf("EncryptedPassword = %q, %v then %q, %v; want one value", pw, err, again, err2)
	}

	must(t, tk.RevokeValue(ctx, issued.AccessToken))
	if _, err := w.st.Tokens().Get(ctx, issued.TokenID); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("record after RevokeValue: %v, want ErrNotFound", err)
	}
	if err := tk.RevokeValue(ctx, issued.AccessToken); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("RevokeValue twice = %v, want ErrNotFound", err)
	}

	// The encrypted password's token never expires and stays.
	minute := time.Minute
	expiring, err := tk.Issue(ctx, accessapi.TokenRequest{Username: "ann", ExpiresIn: &minute})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := tk.DeleteExpired(ctx, now.Add(2*time.Minute)); n != 1 || err != nil {
		t.Errorf("DeleteExpired = %d, %v; want the one expired token", n, err)
	}
	if _, err := w.st.Tokens().Get(ctx, expiring.TokenID); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("expired record after DeleteExpired: %v, want ErrNotFound", err)
	}
	canceled := canceledContext(ctx)
	_, err = tk.DeleteExpired(canceled, now)
	_, want := w.st.Tokens().DeleteExpired(canceled, now)
	sameError(t, "DeleteExpired with a canceled context", err, want)
}

// The store-only bindings return the store's answers and errors unwrapped,
// whether reached through API or the *Of constructors, and take part in the
// caller's transaction.
func TestUnitStoreBindings(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	st := w.st
	must(t, st.Keys().CreateKeypair(ctx, types.Keypair{
		Name:       "rsa",
		Type:       accessapi.KeypairRSA,
		Alias:      "rsa",
		PublicKey:  "public",
		PrivateKey: "private",
	}))
	must(t, st.Keys().PutCertificate(ctx, types.Certificate{
		Alias: "client", PEM: "pem", PrivateKey: "key", IssuedTo: "ann",
	}))
	wantKP, _ := st.Keys().Keypair(ctx, "rsa")
	wantCert, _ := st.Keys().Certificate(ctx, "client")
	_, noKP := st.Keys().Keypair(ctx, "gpg")
	_, noCert := st.Keys().Certificate(ctx, "server")
	for name, keys := range map[string]accessapi.Keys{"API": w.api.Keys, "KeysOf": access.KeysOf(st)} {
		kp, err := keys.Keypair(ctx, "rsa")
		c, err2 := keys.Certificate(ctx, "client")
		if err != nil || err2 != nil || !reflect.DeepEqual(kp, wantKP) ||
			!reflect.DeepEqual(c, wantCert) {
			t.Errorf(
				"%s Keypair and Certificate = %+v, %+v, %v, %v; want the stored ones",
				name,
				kp,
				c,
				err,
				err2,
			)
		}
		sameError(t, name+" Keypair(gpg)", second(keys.Keypair(ctx, "gpg")), noKP)
		sameError(t, name+" Certificate(server)", second(keys.Certificate(ctx, "server")), noCert)
	}

	u, err := w.api.Identity.User(ctx, "ann")
	if err != nil || !reflect.DeepEqual(u, accessapi.UserInfo{
		Name: "ann", Email: "ann@example.com", Groups: []string{"readers"}, Status: "enabled",
	}) {
		t.Errorf("User(ann) = %+v, %v", u, err)
	}
	_, noUser := st.Users().Get(ctx, "bob")
	sameError(t, "User(bob)", second(w.api.Identity.User(ctx, "bob")), noUser)
	must(t, st.Groups().Create(ctx, types.Group{Name: "zeta", AutoJoin: true}))
	must(t, st.Groups().Create(ctx, types.Group{Name: "everyone", AutoJoin: true}))
	if g, err := w.api.Identity.AutoJoinGroups(
		ctx,
	); err != nil ||
		!reflect.DeepEqual(g, []string{"everyone", "zeta"}) {
		t.Errorf("AutoJoinGroups = %v, %v; want [everyone zeta]", g, err)
	}

	must(t, st.Projects().Create(ctx, types.Project{Key: "ml", DisplayName: "ML"}))
	for _, e := range []types.Environment{{ProjectKey: "ml", Name: "ML-STAGE"}, {ProjectKey: "ml", Name: "ML-LAB"}, {Name: "QA"}} {
		must(t, st.Projects().CreateEnvironment(ctx, e))
	}
	must(
		t,
		st.Projects().
			PutMember(ctx, types.ProjectMember{ProjectKey: "ml", Type: types.PrincipalUser, Name: "ann"}),
	)
	must(
		t,
		st.Projects().
			PutMember(ctx, types.ProjectMember{ProjectKey: "ml", Type: types.PrincipalGroup, Name: "readers"}),
	)
	_, notMember := st.Projects().Member(ctx, "ml", types.PrincipalUser, "readers")
	_, noProject := st.Projects().Get(ctx, "missing")
	canceled := canceledContext(ctx)
	_, unreadable := st.Projects().Environments(canceled, "ml")
	for name, projects := range map[string]accessapi.Projects{"API": w.api.Projects, "ProjectsOf": access.ProjectsOf(st)} {
		for project, want := range map[string][]string{"ml": {"ML-LAB", "ML-STAGE"}, "": {"QA"}, "missing": {}} {
			if envs, err := projects.Environments(
				ctx,
				project,
			); err != nil ||
				!reflect.DeepEqual(envs, want) {
				t.Errorf("%s Environments(%q) = %#v, %v; want %#v", name, project, envs, err, want)
			}
		}
		envs, err := projects.Environments(canceled, "ml")
		if envs != nil {
			t.Errorf("%s Environments with a canceled context = %v, want none", name, envs)
		}
		sameError(t, name+" Environments with a canceled context", err, unreadable)
		if err := projects.Member(ctx, "ml", "ann"); err != nil {
			t.Errorf("%s Member(ml, ann) = %v, want nil", name, err)
		}
		// Only a user's membership counts, not a group's of the same name.
		sameError(t, name+" Member(ml, readers)", projects.Member(ctx, "ml", "readers"), notMember)
		if err := projects.Exists(ctx, "ml"); err != nil {
			t.Errorf("%s Exists(ml) = %v, want nil", name, err)
		}
		sameError(t, name+" Exists(missing)", projects.Exists(ctx, "missing"), noProject)
	}

	var unused bool
	unwritten := st.Settings().Get(ctx, types.SettingAnonymousAccess, &unused)
	sameError(t, "Enabled, never written", second(w.api.AnonymousAccess.Enabled(ctx)), unwritten)
	must(t, access.AnonymousAccessOf(st).SetEnabled(ctx, true))
	if on, err := w.api.AnonymousAccess.Enabled(ctx); !on || err != nil {
		t.Errorf("Enabled after SetEnabled(true) = %v, %v; want true", on, err)
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		must(t, w.api.AnonymousAccess.SetEnabled(ctx, false))
		if on, err := access.AnonymousAccessOf(st).Enabled(ctx); on || err != nil {
			t.Errorf("Enabled inside the transaction = %v, %v; want false", on, err)
		}
		return errRollback
	})
	if on, _ := w.api.AnonymousAccess.Enabled(ctx); !errors.Is(err, errRollback) || !on {
		t.Errorf("after the rolled back SetEnabled(false): %v, enabled %v; want still on", err, on)
	}

	r := w.api.Restrictions
	if err := r.Check(ctx, "ann", now); err != nil {
		t.Errorf("Check, unrestricted = %v, want nil", err)
	}
	if _, err := st.APIRestrictions().Put(ctx, store.APIRestriction{
		User: "ann", Actor: "admin", Reason: "audit", Until: now.Add(time.Hour), Changed: now,
	}); err != nil {
		t.Fatal(err)
	}
	for at, want := range map[time.Time]error{now: kernel.ErrAPIRestricted, now.Add(time.Hour): nil} {
		if err := r.Check(ctx, "ann", at); !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("Check at %v = %v, want %v", at, err, want)
		}
	}
	if err := r.Check(authn.WithAPIRecovery(ctx), "ann", now); err != nil {
		t.Errorf("Check on a recovery route = %v, want nil", err)
	}
	sameError(t, "Check with a canceled context", r.Check(canceled, "ann", now),
		authn.CheckAPIRestriction(canceled, st.APIRestrictions(), "ann", now))
}

// ItemGrants unions the actions of the permissions whose artifact targets
// admit the item, reading the targets in the order given.
func TestUnitItemGrants(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	read := map[string]types.Actions{"ann": types.ActionRead}
	for _, p := range []types.Permission{
		permission("a-libs", map[string]types.Target{libs.Key: {Include: []string{"com/**"}}},
			types.Grants{Users: read, Groups: map[string]types.Actions{"readers": types.ActionRead}}),
		permission("b-any", map[string]types.Target{types.TargetAny: {}},
			types.Grants{Users: map[string]types.Actions{"ann": types.ActionWrite}}),
		// Its libs-local target excludes the path, so ANY LOCAL decides.
		permission("c-any-local", map[string]types.Target{
			libs.Key:             {Exclude: []string{"com/**"}},
			types.TargetAnyLocal: {},
		}, types.Grants{Users: map[string]types.Actions{"bob": types.ActionDelete}}),
		permission("d-elsewhere", map[string]types.Target{libs.Key: {Include: []string{"org/**"}}},
			types.Grants{Users: map[string]types.Actions{"eve": types.ActionManage}}),
		permission("e-below", map[string]types.Target{libs.Key: {Include: []string{"com/acme/**"}}},
			types.Grants{Users: map[string]types.Actions{"fay": types.ActionRead}}),
		{Name: "f-build", Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {
			Actions: types.Grants{Users: map[string]types.Actions{"gus": types.ActionRead}},
			Targets: map[string]types.Target{libs.Key: {}},
		}}},
	} {
		must(t, st.Permissions().Create(ctx, p))
	}
	var order []string
	g := module(t, scripted{Store: st, targets: &order}).Grants
	targets := []string{libs.Key, types.TargetAny, types.TargetAnyLocal}

	users, groups, err := g.ItemGrants(ctx, targets, "com/x.jar", false)
	wantUsers := map[string]types.Actions{
		"ann": types.ActionRead | types.ActionWrite,
		"bob": types.ActionDelete,
	}
	if err != nil || !reflect.DeepEqual(users, wantUsers) ||
		!reflect.DeepEqual(groups, map[string]types.Actions{"readers": types.ActionRead}) {
		t.Errorf(
			"ItemGrants(com/x.jar) = %v, %v, %v; want %v and readers' READ",
			users,
			groups,
			err,
			wantUsers,
		)
	}
	if !reflect.DeepEqual(order, targets) {
		t.Errorf("targets read %v, want %v", order, targets)
	}
	// A folder also counts the permissions that admit a path below it.
	users, _, _ = g.ItemGrants(ctx, targets, "com", true)
	if users["fay"] != types.ActionRead {
		t.Errorf("ItemGrants(com folder) = %v, want fay's READ from below", users)
	}

	// A store error names the target being read, and the reads stop there.
	for _, c := range []struct {
		fail    string
		targets []string
	}{
		{"", []string{types.TargetAnyLocal, libs.Key}},
		{"", []string{libs.Key, types.TargetAnyLocal}},
		{types.TargetAny, targets},
	} {
		order = nil
		g := module(
			t,
			scripted{Store: st, targets: &order, permErr: errBoom, failTarget: c.fail},
		).Grants
		users, groups, err := g.ItemGrants(ctx, c.targets, "com/x.jar", false)
		failed := order[len(order)-1]
		if users != nil || groups != nil || err == nil ||
			err.Error() != "permissions with target "+failed+": boom" || !errors.Is(err, errBoom) ||
			(c.fail == "" && failed != c.targets[0]) || (c.fail != "" && failed != c.fail) {
			t.Errorf(
				"ItemGrants(%v) failing on %q = %v, %v, %v after reading %v",
				c.targets,
				c.fail,
				users,
				groups,
				err,
				order,
			)
		}
	}
}

// AccessChange compares each permission's covering targets at both ends of
// every pair, in name order and pages of its limit.
func TestUnitAccessChange(t *testing.T) {
	ctx := t.Context()
	pairs := []accessapi.PathPair{{Source: "models/a", Destination: "archive/a", Folder: true}}
	grant := types.Grants{Users: map[string]types.Actions{"ann": types.ActionRead}}
	targets := func(m map[string]types.Target) func(string) types.Permission {
		return func(name string) types.Permission { return permission(name, m, grant) }
	}
	same := targets(map[string]types.Target{"hf-local": {}})
	moves := targets(map[string]types.Target{"hf-local": {Include: []string{"models/**"}}})
	for _, c := range []struct {
		name  string
		perms []types.Permission
		class string
		limit int
		want  string
		err   error
		lists int
	}{
		{"exactly the limit", []types.Permission{same("a"), same("b"), same("c")}, "local", 3, "", nil, 1},
		{"one past the limit", []types.Permission{same("a"), same("b"), same("c"), same("d")}, "local", 3, "", accessapi.ErrScanLimit, 2},
		{"a change within the limit", []types.Permission{same("a"), same("b"), moves("c"), same("d")}, "local", 3, "c", nil, 1},
		// Counted before it is compared, the permission past the limit
		// refuses the scan although it changes access.
		{"a change past the limit", []types.Permission{same("a"), same("b"), same("c"), moves("d")}, "local", 3, "", accessapi.ErrScanLimit, 2},
		{"the first change by name", []types.Permission{moves("c"), same("a"), moves("b")}, "local", 10, "b", nil, 1},
		{"a special target covering the class", []types.Permission{
			targets(map[string]types.Target{types.TargetAnyLocal: {Include: []string{"models/**"}}})("any-local"),
		}, "local", 10, "any-local", nil, 1},
		{"a special target not covering the class", []types.Permission{
			targets(map[string]types.Target{types.TargetAnyRemote: {Include: []string{"models/**"}}})("any-remote"),
		}, "local", 10, "", nil, 1},
		{"another repository's target", []types.Permission{
			targets(map[string]types.Target{"other-local": {Include: []string{"models/**"}}})("other"),
		}, "local", 10, "", nil, 1},
		{"an exclusion", []types.Permission{
			targets(map[string]types.Target{"hf-local": {Exclude: []string{"archive/**"}}})("excluding"),
		}, "local", 10, "excluding", nil, 1},
		// The union of the covering targets admits both ends.
		{"a union admitting both ends", []types.Permission{targets(map[string]types.Target{
			"hf-local":      {Include: []string{"models/**"}},
			types.TargetAny: {Include: []string{"archive/**"}},
		})("union")}, "local", 10, "", nil, 1},
		{"no artifact section", []types.Permission{{Name: "build", Resources: map[types.ResourceType]types.Resource{
			types.ResourceBuild: {Actions: grant, Targets: map[string]types.Target{"hf-local": {Include: []string{"models/**"}}}},
		}}}, "local", 10, "", nil, 1},
	} {
		st := inmem.New()
		for _, p := range c.perms {
			must(t, st.Permissions().Create(ctx, p))
		}
		var pages []store.Page
		g := module(t, scripted{Store: st, pages: &pages}).Grants
		got, err := g.AccessChange(ctx, "hf-local", c.class, pairs, c.limit)
		if got != c.want || !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Errorf("%s: AccessChange = %q, %v; want %q, %v", c.name, got, err, c.want, c.err)
		}
		if len(pages) != c.lists || pages[0].Limit != c.limit {
			t.Errorf("%s: read pages %+v, want %d of %d", c.name, pages, c.lists, c.limit)
		}
	}

	got, err := module(t, scripted{Store: inmem.New(), permErr: errBoom}).Grants.AccessChange(
		ctx, "hf-local", "local", pairs, 3,
	)
	if got != "" || err != errBoom { //nolint:errorlint // the store's error, returned as it is
		t.Errorf(
			"AccessChange with a failing store = %q, %v; want the store's error unwrapped",
			got,
			err,
		)
	}
}

// reads reports whether d lets its subject read a file in repo.
func reads(d accessapi.Decider, repo accessapi.Repo) bool {
	return d != nil && d.Allows(accessapi.ResourceArtifact, repo, "a/b.jar", accessapi.ActionRead)
}

// permission returns a permission whose artifact section grants g on
// targets.
func permission(name string, targets map[string]types.Target, g types.Grants) types.Permission {
	return types.Permission{Name: name, Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {Actions: g, Targets: targets},
	}}
}

// module returns the API of a module over st with no services.
func module(t *testing.T, st store.Store) accessapi.API {
	t.Helper()
	m, err := access.New(access.Deps{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return m.API()
}

// sameError fails unless got reads as want, the error that the store, or the
// legacy function the binding calls, gives for the same call: the same text,
// so the binding added no wrap, and a match for kernel.ErrNotFound when want
// is one.
func sameError(t *testing.T, what string, got, want error) {
	t.Helper()
	if want == nil || got == nil || got.Error() != want.Error() ||
		errors.Is(got, kernel.ErrNotFound) != errors.Is(want, kernel.ErrNotFound) {
		t.Errorf("%s = %v, want %v as it is", what, got, want)
	}
}

// canceledContext returns a canceled child of ctx, which every in-memory
// store call refuses, so a binding's store error can be read.
func canceledContext(ctx context.Context) context.Context {
	c, cancel := context.WithCancel(ctx)
	cancel()
	return c
}

// second returns the error of a call that returns a value and an error.
func second[T any](_ T, err error) error { return err }

// scripted wraps a store: it records the targets that permission reads name
// and the pages they ask for, and fails the reads a test names.
type scripted struct {
	store.Store
	targets *[]string
	pages   *[]store.Page
	// permErr fails ForPrincipal, List and WithTarget, the last only for
	// failTarget when that is set.
	permErr    error
	failTarget string
	// settingsErr fails reading a setting.
	settingsErr error
}

func (s scripted) Permissions() store.PermissionStore {
	return scriptedPermissions{PermissionStore: s.Store.Permissions(), s: s}
}

func (s scripted) Settings() store.SettingsStore {
	return scriptedSettings{SettingsStore: s.Store.Settings(), err: s.settingsErr}
}

type scriptedPermissions struct {
	store.PermissionStore
	s scripted
}

func (p scriptedPermissions) WithTarget(
	ctx context.Context,
	target string,
) ([]types.Permission, error) {
	if p.s.targets != nil {
		*p.s.targets = append(*p.s.targets, target)
	}
	if p.s.permErr != nil && (p.s.failTarget == "" || p.s.failTarget == target) {
		return nil, p.s.permErr
	}
	return p.PermissionStore.WithTarget(
		ctx,
		target,
	) //nolint:wrapcheck // a pass-through test double
}

func (p scriptedPermissions) List(
	ctx context.Context,
	page store.Page,
) ([]types.Permission, string, error) {
	if p.s.pages != nil {
		*p.s.pages = append(*p.s.pages, page)
	}
	if p.s.permErr != nil {
		return nil, "", p.s.permErr
	}
	return p.PermissionStore.List(ctx, page) //nolint:wrapcheck // a pass-through test double
}

func (p scriptedPermissions) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]types.Permission, error) {
	if p.s.permErr != nil {
		return nil, p.s.permErr
	}
	return p.PermissionStore.ForPrincipal(
		ctx,
		user,
		groups,
	) //nolint:wrapcheck // a pass-through test double
}

type scriptedSettings struct {
	store.SettingsStore
	err error
}

func (s scriptedSettings) Get(ctx context.Context, key string, into any) error {
	if s.err != nil {
		return s.err
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // a pass-through test double
}
