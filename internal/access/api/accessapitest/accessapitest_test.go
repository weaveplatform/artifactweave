package accessapitest_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var (
	errBoom = errors.New("boom")
	libs    = accessapi.Repo{Key: "libs-local", Class: "local"}
)

// answers is every answer a Decider gives about libs, in method order.
func answers(d accessapi.Decider, want accessapi.Actions) [5]bool {
	return [5]bool{
		d.IsAdmin(),
		d.IsRepoVisible(libs),
		d.Allows(accessapi.ResourceArtifact, libs, "a/b.jar", want),
		d.AllowsFolder(accessapi.ResourceArtifact, libs, "a", want),
		d.System("info", kernel.ScopeRead),
	}
}

// A Decider's fields answer; Admin answers what no function narrows, and no
// field answers a question that asks for no action.
func TestUnitDecider(t *testing.T) {
	if got := answers(&accessapitest.Decider{}, accessapi.ActionRead); got != [5]bool{} {
		t.Errorf("zero Decider answers %v, want nothing allowed", got)
	}
	if g := (&accessapitest.Decider{}).Groups(); g != nil {
		t.Errorf("zero Decider Groups = %v, want nil", g)
	}

	admin := &accessapitest.Decider{Admin: true, GroupNames: []string{"admins"}}
	if got := answers(admin, accessapi.ActionRead); got != [5]bool{true, true, true, true, true} {
		t.Errorf("administrator answers %v, want everything allowed", got)
	}
	if got := answers(admin, 0); got[2] || got[3] {
		t.Errorf(
			"administrator asked for no action answers %v, want Allows and AllowsFolder false",
			got,
		)
	}
	groups := admin.Groups()
	groups[0] = "changed"
	if g := admin.Groups(); !reflect.DeepEqual(g, []string{"admins"}) {
		t.Errorf("Groups after the caller changed its copy = %v, want [admins]", g)
	}

	// Each function sees its question and overrides Admin.
	var asked []string
	scripted := &accessapitest.Decider{
		Admin: true,
		IsRepoVisibleFunc: func(r accessapi.Repo) bool {
			asked = append(asked, "visible "+r.Key)
			return false
		},
		AllowsFunc: func(rt accessapi.ResourceType, r accessapi.Repo, p string, a accessapi.Actions) bool {
			asked = append(asked, fmt.Sprintf("allows %s %s %s %d", rt, r.Key, p, a))
			return false
		},
		AllowsFolderFunc: func(rt accessapi.ResourceType, r accessapi.Repo, p string, a accessapi.Actions) bool {
			asked = append(asked, fmt.Sprintf("folder %s %s %s %d", rt, r.Key, p, a))
			return false
		},
		SystemFunc: func(area string, a kernel.ScopeActions) bool {
			asked = append(asked, fmt.Sprintf("system %s %d", area, a))
			return false
		},
	}
	if got := answers(scripted, accessapi.ActionWrite); got != [5]bool{true} {
		t.Errorf("scripted Decider answers %v, want only IsAdmin", got)
	}
	answers(scripted, 0)
	want := []string{
		"visible libs-local",
		fmt.Sprintf("allows artifact libs-local a/b.jar %d", accessapi.ActionWrite),
		fmt.Sprintf("folder artifact libs-local a %d", accessapi.ActionWrite),
		fmt.Sprintf("system info %d", kernel.ScopeRead),
		// Asked for no action, Allows and AllowsFolder do not consult the
		// functions.
		"visible libs-local",
		fmt.Sprintf("system info %d", kernel.ScopeRead),
	}
	if !reflect.DeepEqual(asked, want) {
		t.Errorf("questions asked = %q, want %q", asked, want)
	}
}

func TestUnitAuthenticator(t *testing.T) {
	ctx := t.Context()
	ann := kernel.Principal{Name: "ann", Groups: []string{"readers"}}
	ref := kernel.DownloadCredential{Kind: "token", TokenID: "t1"}

	zero := &accessapitest.Authenticator{}
	for name, err := range map[string]error{
		"Basic":           second(zero.Basic(ctx, "ann", "pw")),
		"Token":           second(zero.Token(ctx, "raw", "")),
		"RestoreDownload": second(zero.RestoreDownload(ctx, "ann", ref)),
	} {
		if !errors.Is(err, kernel.ErrBadCredentials) {
			t.Errorf("zero Authenticator %s = %v, want ErrBadCredentials", name, err)
		}
	}
	if on, err := zero.AnonymousAllowed(ctx); on || err != nil {
		t.Errorf("zero AnonymousAllowed = %v, %v; want false, nil", on, err)
	}
	for name, recheck := range map[string]func(context.Context, kernel.Principal) (kernel.Principal, error){
		"Current": zero.Current, "CurrentRegistry": zero.CurrentRegistry,
	} {
		if p, err := recheck(ctx, ann); err != nil || !reflect.DeepEqual(p, ann) {
			t.Errorf("zero %s = %+v, %v; want the principal unchanged", name, p, err)
		}
	}

	var got []string
	answer := func(call string) (kernel.Principal, error) {
		got = append(got, call)
		return kernel.Principal{Name: call}, errBoom
	}
	scripted := &accessapitest.Authenticator{
		BasicFunc: func(_ context.Context, name, password string) (kernel.Principal, error) {
			return answer("basic " + name + " " + password)
		},
		TokenFunc: func(_ context.Context, raw, user string) (kernel.Principal, error) {
			return answer("token " + raw + " " + user)
		},
		AnonymousAllowedFunc: func(context.Context) (bool, error) {
			_, err := answer("anonymous")
			return true, err
		},
		CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
			return answer("current " + p.Name)
		},
		CurrentRegistryFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
			return answer("registry " + p.Name)
		},
		RestoreDownloadFunc: func(
			_ context.Context,
			name string,
			r kernel.DownloadCredential,
		) (kernel.Principal, error) {
			return answer("download " + name + " " + r.TokenID)
		},
	}
	errs := []error{
		second(scripted.Basic(ctx, "ann", "pw")),
		second(scripted.Token(ctx, "raw", "ann")),
		second(scripted.AnonymousAllowed(ctx)),
		second(scripted.Current(ctx, ann)),
		second(scripted.CurrentRegistry(ctx, ann)),
		second(scripted.RestoreDownload(ctx, "ann", ref)),
	}
	want := []string{
		"basic ann pw",
		"token raw ann",
		"anonymous",
		"current ann",
		"registry ann",
		"download ann t1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("functions called with %q, want %q", got, want)
	}
	for i, err := range errs {
		if err != errBoom { //nolint:errorlint // the function's own error, returned as it is
			t.Errorf("call %d error = %v, want the function's", i, err)
		}
	}
	if p, _ := scripted.Basic(ctx, "ann", "pw"); p.Name != "basic ann pw" {
		t.Errorf("Basic returned %+v, want the function's principal", p)
	}
}

func TestUnitAuthorizer(t *testing.T) {
	ctx := t.Context()
	ann := kernel.Principal{
		Name:                 "ann",
		Groups:               []string{"readers"},
		IsTokenAuthenticated: true,
		Scope: kernel.Scope{
			Entries: []kernel.ScopeEntry{{Kind: kernel.ScopeGroups, Names: []string{"ops"}}},
		},
	}

	zero := &accessapitest.Authorizer{}
	p, d, err := zero.Authorize(ctx, ann, true)
	if err != nil || d == nil || answers(d, accessapi.ActionRead) != [5]bool{} {
		t.Fatalf("zero Authorize = %v, %v; want an empty Decider", d, err)
	}
	if p.Admin || p.Groups != nil || p.Name != "ann" {
		t.Errorf("zero Authorize principal = %+v, want ann with the empty Decider's groups", p)
	}
	if d, err := zero.For(ctx, ann); err != nil || answers(d, accessapi.ActionRead) != [5]bool{} {
		t.Errorf("zero For = %v, %v; want an empty Decider", d, err)
	}
	if d, err := zero.Decide(ctx, accessapi.Subject{Name: "bob"}, false); err != nil ||
		answers(d, accessapi.ActionRead) != [5]bool{} {
		t.Errorf("zero Decide = %v, %v; want an empty Decider", d, err)
	}

	// Authorize and For evaluate the principal's subject; Authorize takes
	// Admin and Groups from the Decider.
	type evaluation struct {
		s    accessapi.Subject
		anon bool
	}
	var seen []evaluation
	admin := &accessapitest.Decider{Admin: true, GroupNames: []string{"admins"}}
	a := &accessapitest.Authorizer{
		AnonymousEnabled: true,
		DecideFunc: func(_ context.Context, s accessapi.Subject, anon bool) (accessapi.Decider, error) {
			seen = append(seen, evaluation{s, anon})
			return admin, nil
		},
	}
	p, d, err = a.Authorize(ctx, ann, false)
	if err != nil || d != admin || !p.Admin || !reflect.DeepEqual(p.Groups, []string{"admins"}) {
		t.Errorf("Authorize = %+v, %v, %v; want ann as an administrator in admins", p, d, err)
	}
	if d, err := a.For(ctx, ann); err != nil || d != admin {
		t.Errorf("For = %v, %v; want the function's Decider", d, err)
	}
	bob := accessapi.Subject{Name: "bob", Anonymous: true}
	if d, err := a.Decide(ctx, bob, false); err != nil || d != admin {
		t.Errorf("Decide = %v, %v; want the function's Decider", d, err)
	}
	subject := accessapi.SubjectOf(ann)
	want := []evaluation{{subject, false}, {subject, true}, {bob, false}}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("evaluated %+v, want %+v", seen, want)
	}

	// An evaluation error comes with the contract's wraps and a nil Decider,
	// even when the function returned one with it.
	failing := &accessapitest.Authorizer{
		DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
			return admin, errBoom
		},
	}
	p, d, err = failing.Authorize(ctx, ann, true)
	if d != nil || !reflect.DeepEqual(p, kernel.Principal{}) || !errors.Is(err, errBoom) ||
		err.Error() != "auth: permissions: boom" {
		t.Errorf(
			"failing Authorize = %+v, %v, %v; want zero, nil, auth: permissions: boom",
			p,
			d,
			err,
		)
	}
	d, err = failing.For(ctx, ann)
	if d != nil || !errors.Is(err, errBoom) || err.Error() != "evaluate permissions: boom" {
		t.Errorf("failing For = %v, %v; want nil, evaluate permissions: boom", d, err)
	}
	d, err = failing.Decide(ctx, bob, true)
	if d != nil || err != errBoom { //nolint:errorlint // Decide returns the error unwrapped
		t.Errorf("failing Decide = %v, %v; want nil, boom unwrapped", d, err)
	}
}

// Tokens records each call, and its numbered tokens follow the request.
// Calls from several goroutines are all recorded.
func TestUnitTokens(t *testing.T) {
	ctx := t.Context()
	tk := &accessapitest.Tokens{}
	hour := time.Hour
	plain, err := tk.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	if err != nil || !reflect.DeepEqual(plain, accessapi.IssuedToken{
		AccessToken: "access-token-1", TokenID: "token-1",
	}) {
		t.Errorf("Issue = %+v, %v; want the first numbered token", plain, err)
	}
	reg, err := tk.IssueRegistry(
		ctx,
		accessapi.RegistryTokenRequest{Username: "ann", ExpiresIn: 90 * time.Second},
	)
	if err != nil || !reflect.DeepEqual(reg, accessapi.IssuedToken{
		AccessToken: "access-token-2", TokenID: "token-2", ExpiresIn: 90,
	}) {
		t.Errorf("IssueRegistry = %+v, %v; want the second numbered token for 90s", reg, err)
	}
	full, _ := tk.Issue(ctx, accessapi.TokenRequest{
		Username: "bob", ExpiresIn: &hour, Refreshable: true, IncludeReference: true,
	})
	purposed, _ := tk.Issue(
		ctx,
		accessapi.TokenRequest{Username: "bob", Purpose: "encrypted_password"},
	)
	if !reflect.DeepEqual(full, accessapi.IssuedToken{
		AccessToken: "access-token-3", RefreshToken: "refresh-token-3",
		ReferenceToken: "reference-token-3", TokenID: "token-3", ExpiresIn: 3600,
	}) || purposed.ReferenceToken != "reference-token-4" || purposed.RefreshToken != "" {
		t.Errorf(
			"Issue = %+v and %+v; want the refresh and reference tokens asked for",
			full,
			purposed,
		)
	}
	if err := tk.RevokeValue(ctx, "access-token-1"); err != nil {
		t.Errorf("RevokeValue = %v", err)
	}
	for range 2 {
		if pw, err := tk.EncryptedPassword(
			ctx,
			"ann",
		); pw != "encrypted-password-ann" ||
			err != nil {
			t.Errorf("EncryptedPassword = %q, %v; want encrypted-password-ann", pw, err)
		}
	}
	cutoff := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if n, err := tk.DeleteExpired(ctx, cutoff); n != 0 || err != nil {
		t.Errorf("DeleteExpired = %d, %v; want 0, nil", n, err)
	}
	calls := tk.Calls()
	want := accessapitest.TokenCalls{
		Issue: []accessapi.TokenRequest{
			{Username: "ann"},
			{Username: "bob", ExpiresIn: &hour, Refreshable: true, IncludeReference: true},
			{Username: "bob", Purpose: "encrypted_password"},
		},
		IssueRegistry: []accessapi.RegistryTokenRequest{
			{Username: "ann", ExpiresIn: 90 * time.Second},
		},
		RevokeValue:       []string{"access-token-1"},
		EncryptedPassword: []string{"ann", "ann"},
		DeleteExpired:     []time.Time{cutoff},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("Calls = %+v, want %+v", calls, want)
	}
	calls.RevokeValue[0] = "changed"
	if tk.Calls().RevokeValue[0] != "access-token-1" {
		t.Error("changing the copy Calls returned changed the record")
	}

	// The functions answer, and may read the record while they do.
	scripted := &accessapitest.Tokens{}
	scripted.IssueFunc = func(context.Context, accessapi.TokenRequest) (accessapi.IssuedToken, error) {
		return accessapi.IssuedToken{TokenID: fmt.Sprint(len(scripted.Calls().Issue))}, errBoom
	}
	scripted.IssueRegistryFunc = func(
		context.Context,
		accessapi.RegistryTokenRequest,
	) (accessapi.IssuedToken, error) {
		return accessapi.IssuedToken{TokenID: "registry"}, errBoom
	}
	scripted.RevokeValueFunc = func(context.Context, string) error { return errBoom }
	scripted.EncryptedPasswordFunc = func(context.Context, string) (string, error) { return "pw", errBoom }
	scripted.DeleteExpiredFunc = func(context.Context, time.Time) (int64, error) { return 7, errBoom }
	issued, err1 := scripted.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
	registry, err2 := scripted.IssueRegistry(ctx, accessapi.RegistryTokenRequest{Username: "ann"})
	err3 := scripted.RevokeValue(ctx, "raw")
	pw, err4 := scripted.EncryptedPassword(ctx, "ann")
	n, err5 := scripted.DeleteExpired(ctx, cutoff)
	if issued.TokenID != "1" || registry.TokenID != "registry" || pw != "pw" || n != 7 {
		t.Errorf("functions' answers = %q, %q, %q, %d", issued.TokenID, registry.TokenID, pw, n)
	}
	for i, err := range []error{err1, err2, err3, err4, err5} {
		if err != errBoom { //nolint:errorlint // the function's own error, returned as it is
			t.Errorf("call %d error = %v, want the function's", i, err)
		}
	}

	concurrent := &accessapitest.Tokens{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, _ = concurrent.Issue(ctx, accessapi.TokenRequest{Username: "ann"})
			_, _ = concurrent.IssueRegistry(ctx, accessapi.RegistryTokenRequest{Username: "ann"})
			_ = concurrent.Calls()
		})
	}
	wg.Wait()
	if c := concurrent.Calls(); len(c.Issue) != 8 || len(c.IssueRegistry) != 8 {
		t.Errorf(
			"concurrent calls recorded %d and %d, want 8 and 8",
			len(c.Issue),
			len(c.IssueRegistry),
		)
	}
}

func TestUnitKeys(t *testing.T) {
	ctx := t.Context()
	k := &accessapitest.Keys{
		Keypairs: map[string]accessapi.Keypair{
			"rsa": {Name: "rsa", Type: accessapi.KeypairRSA},
		},
		Certificates: map[string]accessapi.Certificate{
			"client": {Alias: "client", IssuedTo: "ann"},
		},
	}
	if kp, err := k.Keypair(ctx, "rsa"); err != nil || kp.Type != accessapi.KeypairRSA {
		t.Errorf("Keypair = %+v, %v; want the RSA pair", kp, err)
	}
	if c, err := k.Certificate(ctx, "client"); err != nil || c.IssuedTo != "ann" {
		t.Errorf("Certificate = %+v, %v; want the client certificate", c, err)
	}
	if _, err := k.Keypair(ctx, "gpg"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("missing Keypair = %v, want ErrNotFound", err)
	}
	if _, err := k.Certificate(ctx, "server"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("missing Certificate = %v, want ErrNotFound", err)
	}
	k.Err = errBoom
	if _, err := k.Keypair(ctx, "rsa"); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Keypair = %v, want Err", err)
	}
	if _, err := k.Certificate(ctx, "client"); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Certificate = %v, want Err", err)
	}
}

func TestUnitIdentity(t *testing.T) {
	ctx := t.Context()
	id := &accessapitest.Identity{
		Users: map[string]accessapi.UserInfo{
			"ann": {
				Name:   "ann",
				Email:  "ann@example.com",
				Groups: []string{"readers"},
				Status: "locked",
			},
		},
		AutoJoin: []string{"everyone", "readers"},
	}
	u, err := id.User(ctx, "ann")
	if err != nil || u.Email != "ann@example.com" || u.Enabled() {
		t.Fatalf("User = %+v, %v; want ann, locked", u, err)
	}
	u.Groups[0] = "changed"
	if again, _ := id.User(ctx, "ann"); again.Groups[0] != "readers" {
		t.Error("changing a returned user's groups changed the fake's")
	}
	if _, err := id.User(ctx, "bob"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("missing User = %v, want ErrNotFound", err)
	}
	groups, err := id.AutoJoinGroups(ctx)
	if err != nil || !reflect.DeepEqual(groups, []string{"everyone", "readers"}) {
		t.Fatalf("AutoJoinGroups = %v, %v; want [everyone readers]", groups, err)
	}
	groups[0] = "changed"
	if again, _ := id.AutoJoinGroups(ctx); again[0] != "everyone" {
		t.Error("changing the returned groups changed the fake's")
	}
	id.Err = errBoom
	if _, err := id.User(ctx, "ann"); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing User = %v, want Err", err)
	}
	if g, err := id.AutoJoinGroups(
		ctx,
	); g != nil ||
		err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing AutoJoinGroups = %v, %v; want nil, Err", g, err)
	}
}

func TestUnitProjects(t *testing.T) {
	ctx := t.Context()
	p := &accessapitest.Projects{
		Projects: map[string]accessapitest.Project{
			"ml":    {Environments: []string{"STAGE", "LAB"}, Members: []string{"ann"}},
			"empty": {},
		},
		Global: []string{"QA", "EDGE"},
	}
	for project, want := range map[string][]string{
		"":        {"EDGE", "QA"},
		"ml":      {"LAB", "STAGE"},
		"empty":   {},
		"missing": {},
	} {
		got, err := p.Environments(ctx, project)
		if err != nil || got == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Environments(%q) = %#v, %v; want %#v", project, got, err, want)
		}
	}
	envs, _ := p.Environments(ctx, "ml")
	envs[0] = "changed"
	if p.Projects["ml"].Environments[0] != "STAGE" {
		t.Error("Environments sorted or changed the fake's own slice")
	}
	if err := p.Member(ctx, "ml", "ann"); err != nil {
		t.Errorf("Member(ml, ann) = %v, want nil", err)
	}
	for _, c := range [][2]string{{"ml", "bob"}, {"missing", "ann"}} {
		if err := p.Member(ctx, c[0], c[1]); !errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("Member(%s, %s) = %v, want ErrNotFound", c[0], c[1], err)
		}
	}
	if err := p.Exists(ctx, "empty"); err != nil {
		t.Errorf("Exists(empty) = %v, want nil", err)
	}
	if err := p.Exists(ctx, "missing"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("Exists(missing) = %v, want ErrNotFound", err)
	}
	p.Err = errBoom
	envs, err := p.Environments(ctx, "ml")
	if envs != nil || err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Environments = %v, %v; want nil, Err", envs, err)
	}
	if err := p.Member(ctx, "ml", "ann"); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Member = %v, want Err", err)
	}
	if err := p.Exists(ctx, "ml"); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Exists = %v, want Err", err)
	}
}

func TestUnitAnonymousAccess(t *testing.T) {
	ctx := t.Context()
	if on, err := (&accessapitest.AnonymousAccess{}).Enabled(ctx); on || err != nil {
		t.Errorf("zero Enabled = %v, %v; want off, written", on, err)
	}
	a := &accessapitest.AnonymousAccess{Unwritten: true}
	if on, err := a.Enabled(ctx); on || !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("unwritten Enabled = %v, %v; want ErrNotFound", on, err)
	}
	if err := a.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(ctx); !on || err != nil {
		t.Errorf("Enabled after SetEnabled(true) = %v, %v; want on", on, err)
	}
	a.Err = errBoom
	if err := a.SetEnabled(ctx, false); err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing SetEnabled = %v, want Err", err)
	}
	if on, err := a.Enabled(ctx); on || err != errBoom { //nolint:errorlint // Err, as it is
		t.Errorf("failing Enabled = %v, %v; want false, Err", on, err)
	}
	a.Err = nil
	if on, _ := a.Enabled(ctx); !on {
		t.Error("a failed SetEnabled changed the setting")
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_ = a.SetEnabled(ctx, i%2 == 0)
			_, _ = a.Enabled(ctx)
		})
	}
	wg.Wait()
}

func TestUnitGrants(t *testing.T) {
	ctx := t.Context()
	pairs := []accessapi.PathPair{{Source: "a", Destination: "b", Folder: true}}
	users, groups, err := (&accessapitest.Grants{}).ItemGrants(
		ctx,
		[]string{"libs-local"},
		"a",
		false,
	)
	if err != nil || users == nil || groups == nil || len(users)+len(groups) != 0 {
		t.Errorf("zero ItemGrants = %v, %v, %v; want two empty maps", users, groups, err)
	}
	if name, err := (&accessapitest.Grants{}).AccessChange(
		ctx,
		"hf-local",
		"local",
		pairs,
		3,
	); name != "" ||
		err != nil {
		t.Errorf("zero AccessChange = %q, %v; want no change", name, err)
	}

	var asked []string
	g := &accessapitest.Grants{
		ItemGrantsFunc: func(
			_ context.Context,
			targets []string,
			path string,
			folder bool,
		) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
			asked = append(asked, fmt.Sprint(targets, path, folder))
			return map[string]accessapi.Actions{"ann": accessapi.ActionRead}, nil, nil
		},
		AccessChangeFunc: func(
			_ context.Context,
			repoKey, class string,
			ps []accessapi.PathPair,
			limit int,
		) (string, error) {
			asked = append(asked, fmt.Sprint(repoKey, class, ps, limit))
			return "", accessapi.ErrScanLimit
		},
	}
	users, _, _ = g.ItemGrants(ctx, []string{"libs-local", "ANY"}, "a/b", true)
	if _, err := g.AccessChange(
		ctx,
		"hf-local",
		"local",
		pairs,
		3,
	); !errors.Is(
		err,
		accessapi.ErrScanLimit,
	) {
		t.Errorf("scripted AccessChange = %v, want ErrScanLimit", err)
	}
	want := []string{
		fmt.Sprint([]string{"libs-local", "ANY"}, "a/b", true),
		fmt.Sprint("hf-local", "local", pairs, 3),
	}
	if users["ann"] != accessapi.ActionRead || !reflect.DeepEqual(asked, want) {
		t.Errorf(
			"scripted Grants answered %v after %q, want ann's READ after %q",
			users,
			asked,
			want,
		)
	}
}

func TestUnitRestrictions(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := (&accessapitest.Restrictions{}).Check(ctx, "ann", now); err != nil {
		t.Errorf("zero Check = %v, want nil", err)
	}
	r := &accessapitest.Restrictions{
		CheckFunc: func(_ context.Context, user string, at time.Time) error {
			if user == "ann" && at.Equal(now) {
				return kernel.ErrAPIRestricted
			}
			return nil
		},
	}
	if err := r.Check(ctx, "ann", now); !errors.Is(err, kernel.ErrAPIRestricted) {
		t.Errorf("Check(ann) = %v, want ErrAPIRestricted", err)
	}
	if err := r.Check(ctx, "bob", now); err != nil {
		t.Errorf("Check(bob) = %v, want nil", err)
	}
}

// second returns the error of a call that returns a value and an error.
func second[T any](_ T, err error) error { return err }
