package oidc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/auth/idtoken/idtokentest"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/service/oidc"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faulty fails the configuration-store operation named in fail.
type faulty struct {
	*inmem.Store
	fail string
}

func (f *faulty) Configs() store.ConfigStore { return faultyConfigs{f.Store.Configs(), f} }

type faultyConfigs struct {
	store.ConfigStore
	f *faulty
}

func (c faultyConfigs) Get(
	ctx context.Context,
	k types.ConfigKind,
	key string,
) (types.ConfigDocument, error) {
	if c.f.fail == "get "+string(k) {
		return types.ConfigDocument{}, errBoom
	}
	return c.ConfigStore.Get(ctx, k, key) //nolint:wrapcheck // a test double
}

func (c faultyConfigs) List(
	ctx context.Context,
	k types.ConfigKind,
) ([]types.ConfigDocument, error) {
	if c.f.fail == "list "+string(k) {
		return nil, errBoom
	}
	return c.ConfigStore.List(ctx, k) //nolint:wrapcheck // a test double
}

func (c faultyConfigs) Put(ctx context.Context, d types.ConfigDocument) error {
	if c.f.fail == "put "+string(d.Kind) {
		return errBoom
	}
	return c.ConfigStore.Put(ctx, d) //nolint:wrapcheck // a test double
}

func (c faultyConfigs) Delete(ctx context.Context, k types.ConfigKind, key string) error {
	if c.f.fail == "delete "+string(k) {
		return errBoom
	}
	return c.ConfigStore.Delete(ctx, k, key) //nolint:wrapcheck // a test double
}

func ptr[T any](v T) *T { return &v }

func provider(name, issuer string) oidc.Provider {
	return oidc.Provider{Name: ptr(name), IssuerURL: ptr(issuer)}
}

func mapping(name string, spec oidc.TokenSpec, claims map[string]any) oidc.Mapping {
	return oidc.Mapping{Name: ptr(name), TokenSpec: &spec, Claims: claims}
}

// world is a service over a fresh store with a fake provider registered as
// ci, with audience jfrog.
type world struct {
	svc    oidc.Service
	db     *faulty
	issuer *idtokentest.Issuer
}

func newWorld(t *testing.T) world {
	t.Helper()
	ctx := context.Background()
	db := &faulty{Store: inmem.New()}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := idtokentest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(issuer.Close)
	w := world{
		svc:    oidc.Service{Store: db, Tokens: tokens, Keys: idtoken.New(idtoken.Options{})},
		db:     db,
		issuer: issuer,
	}
	p := provider("ci", issuer.URL())
	p.Audience = ptr("jfrog")
	if _, err := w.svc.CreateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w world) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := w.issuer.Mint("jfrog", time.Hour, claims)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (w world) exchange(
	subject string,
	mod ...func(*oidc.ExchangeRequest),
) (oidc.Exchanged, error) {
	r := oidc.ExchangeRequest{
		GrantType: oidc.GrantTokenExchange, SubjectTokenType: oidc.SubjectIDToken,
		SubjectToken: subject, ProviderName: "ci",
	}
	for _, m := range mod {
		m(&r)
	}
	return w.svc.Exchange(context.Background(), r)
}

func TestCanonicalType(t *testing.T) {
	for in, want := range map[string]string{
		"": oidc.TypeGeneric, "generic": oidc.TypeGeneric, "GenericOidc": oidc.TypeGeneric,
		"Generic OpenID Connect": oidc.TypeGeneric, "GitHub": oidc.TypeGitHub,
		"GitHubEnterprise": oidc.TypeGitHubEnterprise, "github enterprise": oidc.TypeGitHubEnterprise,
		"azure": oidc.TypeAzure,
	} {
		if got, ok := oidc.CanonicalType(in); !ok || got != want {
			t.Errorf("%q = %q, %v", in, got, ok)
		}
	}
	if _, ok := oidc.CanonicalType("okta"); ok {
		t.Error("okta accepted")
	}
}

func TestProviders(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.svc
	gh := provider("gh", "https://token.actions.githubusercontent.com")
	gh.ProviderType, gh.Organization = ptr("github"), ptr("octo-org")
	got, err := s.CreateProvider(ctx, gh)
	if err != nil || *got.ProviderType != oidc.TypeGitHub {
		t.Fatalf("create = %+v, %v", got, err)
	}
	permissive := provider("gh2", "https://token.actions.githubusercontent.com")
	permissive.ProviderType, permissive.EnablePermissiveConfiguration = ptr("GitHub"), ptr(true)
	if _, err := s.CreateProvider(ctx, permissive); err != nil {
		t.Fatalf("permissive GitHub without an organization: %v", err)
	}
	all, err := s.Providers(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("providers = %v, %v", all, err)
	}
	for name, p := range map[string]oidc.Provider{
		"duplicate":           provider("ci", "https://ci.example"),
		"bad name":            provider("a b", "https://ci.example"),
		"no name":             {IssuerURL: ptr("https://ci.example")},
		"bad issuer":          provider("x", "ci.example"),
		"bad type":            {Name: ptr("x"), IssuerURL: ptr("https://x"), ProviderType: ptr("okta")},
		"GitHub, no org":      {Name: ptr("x"), IssuerURL: ptr("https://x"), ProviderType: ptr("GitHub")},
		"generic with an org": {Name: ptr("x"), IssuerURL: ptr("https://x"), Organization: ptr("o")},
	} {
		if _, err := s.CreateProvider(ctx, p); !errors.Is(err, oidc.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Update replaces; a body without a name takes the path's.
	upd := oidc.Provider{IssuerURL: ptr("https://ci2.example"), Description: ptr("d")}
	if got, err := s.UpdateProvider(ctx, "gh2", upd); err != nil || *got.Name != "gh2" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if _, err := s.UpdateProvider(
		ctx,
		"gh2",
		provider("other", "https://x"),
	); !errors.Is(
		err,
		oidc.ErrInvalid,
	) {
		t.Errorf("renaming update: %v", err)
	}
	if _, err := s.UpdateProvider(
		ctx,
		"gh2",
		oidc.Provider{IssuerURL: ptr("bad")},
	); !errors.Is(
		err,
		oidc.ErrInvalid,
	) {
		t.Errorf("invalid update: %v", err)
	}
	if _, err := s.UpdateProvider(
		ctx,
		"nope",
		oidc.Provider{IssuerURL: ptr("https://x")},
	); !errors.Is(
		err,
		oidc.ErrNotFound,
	) {
		t.Errorf("update of a missing provider: %v", err)
	}
	// Deleting a provider deletes its mappings.
	if _, err := s.CreateMapping(
		ctx,
		"gh2",
		mapping("m", oidc.TokenSpec{Username: ptr("u")}, nil),
	); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProvider(ctx, "gh2"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Store.Configs().
		Get(ctx, types.ConfigOIDCMapping, "gh2/m"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("mapping outlived its provider: %v", err)
	}
	if err := s.DeleteProvider(ctx, "gh2"); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestMappings(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.svc
	roles := mapping("proj", oidc.TokenSpec{
		Username: ptr("dev"), Scope: ptr(`applied-permissions/roles:p1:"Developer"`),
	}, map[string]any{"sub": "x"})
	got, err := s.CreateMapping(ctx, "ci", roles)
	if err != nil || got.ProjectKey == nil || *got.ProjectKey != "p1" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	global := mapping("global", oidc.TokenSpec{GroupsPattern: ptr("{{groups}}")}, nil)
	if _, err := s.CreateMapping(ctx, "ci", global); err != nil {
		t.Fatal(err)
	}
	if all, err := s.Mappings(ctx, "ci", ""); err != nil || len(all) != 2 {
		t.Fatalf("mappings = %v, %v", all, err)
	}
	if p1, err := s.Mappings(ctx, "ci", "p1"); err != nil || len(p1) != 1 || *p1[0].Name != "proj" {
		t.Fatalf("p1 mappings = %v, %v", p1, err)
	}
	if _, err := s.Mappings(ctx, "nope", ""); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("mappings of a missing provider: %v", err)
	}
	if m, err := s.Mapping(ctx, "ci", "proj"); err != nil || *m.Name != "proj" {
		t.Fatalf("mapping = %+v, %v", m, err)
	}
	if _, err := s.Mapping(ctx, "ci", "nope"); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("missing mapping: %v", err)
	}
	if _, err := s.CreateMapping(ctx, "nope", global); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("mapping of a missing provider: %v", err)
	}
	for name, m := range map[string]oidc.Mapping{
		"duplicate":               global,
		"bad name":                mapping("a/b", oidc.TokenSpec{Username: ptr("u")}, nil),
		"priority 0":              {Name: ptr("x"), Priority: ptr(0), TokenSpec: &oidc.TokenSpec{Username: ptr("u")}},
		"no token spec":           {Name: ptr("x")},
		"two names":               mapping("x", oidc.TokenSpec{Username: ptr("u"), UsernamePattern: ptr("{{sub}}")}, nil),
		"pattern without a claim": mapping("x", oidc.TokenSpec{UsernamePattern: ptr("plain")}, nil),
		"bad pattern regex":       mapping("x", oidc.TokenSpec{UsernamePattern: ptr("{{sub}} | ( | $1")}, nil),
		"bad resource pattern":    mapping("x", oidc.TokenSpec{Username: ptr("u"), ResourcePattern: ptr("plain")}, nil),
		"expires_in 0":            mapping("x", oidc.TokenSpec{Username: ptr("u"), ExpiresIn: ptr(int64(0))}, nil),
		"no scope, no user":       mapping("x", oidc.TokenSpec{}, nil),
		"bad scope":               mapping("x", oidc.TokenSpec{Scope: ptr("nonsense")}, nil),
		"user scope, no user":     mapping("x", oidc.TokenSpec{Scope: ptr("applied-permissions/user")}, nil),
	} {
		if _, err := s.CreateMapping(ctx, "ci", m); !errors.Is(err, oidc.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An update keeps the mapping's project, which its scope implies; a
	// scope that implies another project, or none, is refused (#476).
	upd := oidc.Mapping{TokenSpec: &oidc.TokenSpec{
		Username: ptr("u2"), Scope: ptr(`applied-permissions/roles:p1:"Viewer"`),
	}}
	if got, err := s.UpdateMapping(
		ctx,
		"ci",
		"proj",
		upd,
	); err != nil || *got.Name != "proj" ||
		got.ProjectKey == nil || *got.ProjectKey != "p1" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	for name, c := range map[string]struct {
		mapping string
		spec    oidc.TokenSpec
	}{
		"project to none": {"proj", oidc.TokenSpec{Username: ptr("u2")}},
		"project to another": {"proj", oidc.TokenSpec{
			Username: ptr("u2"), Scope: ptr(`applied-permissions/roles:p2:"Viewer"`),
		}},
		"none to a project": {"global", oidc.TokenSpec{
			Scope: ptr(`applied-permissions/roles:p1:"Viewer"`),
		}},
	} {
		_, err := s.UpdateMapping(ctx, "ci", c.mapping, oidc.Mapping{TokenSpec: &c.spec})
		if !errors.Is(err, oidc.ErrInvalid) || !strings.Contains(err.Error(), "cannot change") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if m, err := s.Mapping(ctx, "ci", "proj"); err != nil || *m.TokenSpec.Username != "u2" ||
		*m.ProjectKey != "p1" {
		t.Fatalf("after the refused updates: %+v, %v", m, err)
	}
	if _, err := s.UpdateMapping(
		ctx,
		"ci",
		"proj",
		mapping("other", oidc.TokenSpec{Username: ptr("u")}, nil),
	); !errors.Is(
		err,
		oidc.ErrInvalid,
	) {
		t.Errorf("renaming update: %v", err)
	}
	if _, err := s.UpdateMapping(
		ctx,
		"ci",
		"proj",
		oidc.Mapping{},
	); !errors.Is(
		err,
		oidc.ErrInvalid,
	) {
		t.Errorf("invalid update: %v", err)
	}
	if _, err := s.UpdateMapping(ctx, "ci", "nope", upd); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("update of a missing mapping: %v", err)
	}
	if err := s.DeleteMapping(ctx, "ci", "proj"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMapping(ctx, "ci", "proj"); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

// TestCombinations applies the page's table of the scopes one mapping may
// combine (IDZ-60): admin with nothing, neither a user with a user pattern
// nor groups with a groups pattern, and every other pair.
func TestCombinations(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	const (
		user   = "applied-permissions/user"
		groups = "applied-permissions/groups:ci"
		admin  = "applied-permissions/admin"
	)
	for name, c := range map[string]struct {
		spec oidc.TokenSpec
		ok   bool
	}{
		"user and groups":         {oidc.TokenSpec{Username: ptr("u"), Scope: ptr(user + " " + groups)}, true},
		"user and groups pattern": {oidc.TokenSpec{Username: ptr("u"), GroupsPattern: ptr("{{g}}")}, true},
		"user scope and groups pattern": {
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr(user), GroupsPattern: ptr("{{g}}")}, true,
		},
		"user and resource": {oidc.TokenSpec{Username: ptr("u"), ResourcePattern: ptr("{{r}}")}, true},
		"groups and user pattern": {
			oidc.TokenSpec{Scope: ptr(groups), UsernamePattern: ptr("{{a}}")}, true,
		},
		"groups and resource": {oidc.TokenSpec{Scope: ptr(groups), ResourcePattern: ptr("{{r}}")}, true},
		"user pattern and groups pattern": {
			oidc.TokenSpec{UsernamePattern: ptr("{{a}}"), GroupsPattern: ptr("{{g}}")}, true,
		},
		"every dynamic mapping": {
			oidc.TokenSpec{
				UsernamePattern: ptr("{{a}}"), GroupsPattern: ptr("{{g}}"),
				ResourcePattern: ptr("{{r}}"),
			},
			true,
		},
		"groups pattern and resource": {
			oidc.TokenSpec{GroupsPattern: ptr("{{g}}"), ResourcePattern: ptr("{{r}}")}, true,
		},
		"admin with a user": {oidc.TokenSpec{Username: ptr("u"), Scope: ptr(admin)}, true},
		"user and user pattern": {
			oidc.TokenSpec{Username: ptr("u"), UsernamePattern: ptr("{{a}}")}, false,
		},
		"user scope and user pattern": {
			oidc.TokenSpec{Scope: ptr(user), UsernamePattern: ptr("{{a}}")}, false,
		},
		"groups and groups pattern": {
			oidc.TokenSpec{Scope: ptr(groups), GroupsPattern: ptr("{{g}}")}, false,
		},
		"admin and user scope": {oidc.TokenSpec{Username: ptr("u"), Scope: ptr(admin + " " + user)}, false},
		"admin and groups":     {oidc.TokenSpec{Username: ptr("u"), Scope: ptr(admin + " " + groups)}, false},
		"admin and user pattern": {
			oidc.TokenSpec{Scope: ptr(admin), UsernamePattern: ptr("{{a}}")}, false,
		},
		"admin and groups pattern": {
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr(admin), GroupsPattern: ptr("{{g}}")}, false,
		},
		"admin and resource": {
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr(admin), ResourcePattern: ptr("{{r}}")}, false,
		},
		"resource beside artifact entries": {
			oidc.TokenSpec{
				Username: ptr("u"), Scope: ptr("artifact:a/**:r"), ResourcePattern: ptr("{{r}}"),
			},
			false,
		},
		"resource alone": {oidc.TokenSpec{ResourcePattern: ptr("{{r}}")}, false},
	} {
		_, err := w.svc.CreateMapping(ctx, "ci", mapping("m", c.spec, nil))
		if c.ok != (err == nil) || (!c.ok && !errors.Is(err, oidc.ErrInvalid)) {
			t.Errorf("%s: %v", name, err)
		}
		if err == nil {
			if err := w.svc.DeleteMapping(ctx, "ci", "m"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestPatternSyntax: a pattern that cannot be evaluated is refused when the
// mapping is saved.
func TestPatternSyntax(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	for name, p := range map[string]string{
		"two claims":                 "{{a}}-{{b}}",
		"empty part":                 "{{a}} | | $0",
		"too many parts":             "{{a}}" + strings.Repeat(` | op:replaceAll("x","y")`, 16),
		"bad template":               "({{a}}",
		"bad expression":             "{{a}} | ( | $1",
		"unknown operator":           `{{a}} | op:upper("a","b")`,
		"operator not a call":        "{{a}} | op:replaceAll",
		"unquoted argument":          `{{a}} | op:replaceAll(a,"b")`,
		"unterminated argument":      `{{a}} | op:replaceAll("a)`,
		"one argument":               `{{a}} | op:replaceAll("a")`,
		"one argument and a comma":   `{{a}} | op:replaceAll("a" "b")`,
		"three arguments":            `{{a}} | op:replaceAll("a","b","c")`,
		"empty text to replace":      `{{a}} | op:replaceAll("","b")`,
		"no such group":              "{{a}} | $2",
		"no such group after regex":  "{{a}} | ^(x)$ | $2",
		"no such named group":        "{{a}} | ${nope}",
		"unterminated named group":   "{{a}} | ${1",
		"dollar without a group":     "{{a}} | $x",
		"dollar at the end":          "{{a}} | x$",
		"escape at the end":          `{{a}} | x\`,
		"no such group in a chain":   `{{a}} | op:replaceAll("-","_") | $3`,
		"replacement before its use": "{{a}} | ^(.)(.)$ | $1 | $3",
	} {
		_, err := w.svc.CreateMapping(
			ctx, "ci", mapping("x", oidc.TokenSpec{UsernamePattern: ptr(p)}, nil),
		)
		if !errors.Is(err, oidc.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, p := range map[string]string{
		"spaces in the placeholder": "{{ a }}",
		"named group":               "{{a}} | ^(?P<head>.)(.*)$ | ${head}",
		"numbered group in braces":  "{{a}} | ${1}x",
		"escapes":                   `{{a}} | \$\\$0`,
		"long chain":                "{{a}}" + strings.Repeat(` | op:replaceAll("x","y")`, 15),
		"three replacements":        "{{a}} | ^(.*)$ | $1 | $0",
	} {
		_, err := w.svc.CreateMapping(
			ctx, "ci", mapping("y", oidc.TokenSpec{UsernamePattern: ptr(p)}, nil),
		)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err := w.svc.DeleteMapping(ctx, "ci", "y"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestProjectRolesWithoutUser: a project roles mapping needs no username,
// as the jfrog/platform provider's documented example sends none. Its token
// is the synthetic subject's, which no stored user can be and which joins
// no auto-join group, with the project roles scope alone and the mapping's
// project (#476).
func TestProjectRolesWithoutUser(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	const roles = `applied-permissions/roles:my-project:"Project Admin","Developer"`
	got, err := w.svc.CreateMapping(ctx, "ci", mapping("roles", oidc.TokenSpec{
		Scope: ptr(roles), Audience: ptr("jfrt@* jfac@*"), ExpiresIn: ptr(int64(7200)),
	}, map[string]any{"sub": "repo:octo-org/*"}))
	if err != nil || got.ProjectKey == nil || *got.ProjectKey != "my-project" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	out, err := w.exchange(w.mint(t, map[string]any{"sub": "repo:octo-org/app"}))
	if err != nil {
		t.Fatal(err)
	}
	want, err := types.ParseScope(roles)
	if err != nil {
		t.Fatal(err)
	}
	if out.Username != "oidc-ci-roles" || out.Record.Identity != types.IdentitySynthetic ||
		out.Record.Scope.String() != want.String() || out.Record.ProjectKey != "my-project" {
		t.Fatalf("exchange = %q %+v", out.Username, out.Record)
	}
	// Without a user, nothing but groups and project roles may stand in the
	// scope.
	for name, spec := range map[string]oidc.TokenSpec{
		"roles and an artifact entry": {Scope: ptr(roles + " artifact:libs:r")},
		"roles and the user scope":    {Scope: ptr(roles + " applied-permissions/user")},
	} {
		if _, err := w.svc.CreateMapping(
			ctx,
			"ci",
			mapping("x", spec, nil),
		); !errors.Is(
			err,
			oidc.ErrInvalid,
		) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestMappingNamesOneProject: a mapping's scope may name roles in one project
// only, "Only one project key is allowed per mapping", on create and on
// update alike; several roles entries in that one project are fine (#493).
func TestMappingNamesOneProject(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	const (
		p1Dev    = `applied-permissions/roles:p1:"Developer"`
		p1Viewer = `applied-permissions/roles:p1:"Viewer"`
		p2Viewer = `applied-permissions/roles:p2:"Viewer"`
	)
	got, err := w.svc.CreateMapping(ctx, "ci",
		mapping("one", oidc.TokenSpec{Scope: ptr(p1Dev + " " + p1Viewer)}, nil))
	if err != nil || got.ProjectKey == nil || *got.ProjectKey != "p1" {
		t.Fatalf("two entries in one project = %+v, %v", got, err)
	}
	two := oidc.TokenSpec{Username: ptr("dev"), Scope: ptr(p1Dev + " " + p2Viewer)}
	for name, call := range map[string]func() error{
		"create": func() error {
			_, err := w.svc.CreateMapping(ctx, "ci", mapping("two", two, nil))
			return err
		},
		"update": func() error {
			_, err := w.svc.UpdateMapping(ctx, "ci", "one", oidc.Mapping{TokenSpec: &two})
			return err
		},
	} {
		err := call()
		if !errors.Is(err, oidc.ErrInvalid) ||
			!strings.Contains(err.Error(), "names project roles in 'p1' and 'p2'") {
			t.Errorf("%s with roles in two projects: %v", name, err)
		}
	}
	if _, err := w.svc.Mapping(ctx, "ci", "two"); !errors.Is(err, oidc.ErrNotFound) {
		t.Errorf("the refused mapping was stored: %v", err)
	}
	if m, err := w.svc.Mapping(ctx, "ci", "one"); err != nil || *m.ProjectKey != "p1" ||
		*m.TokenSpec.Scope != p1Dev+" "+p1Viewer {
		t.Fatalf("after the refused update: %+v, %v", m, err)
	}
}

// TestExchangeOrder: with a project, its mappings first, then global ones,
// and never another project's; without one, every mapping competes. Lower
// priorities first, none last, the first created among equals.
func TestExchangeOrder(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	anySub := map[string]any{"sub": "*"}
	for _, m := range []oidc.Mapping{
		{Name: ptr("unranked"), TokenSpec: &oidc.TokenSpec{Username: ptr("unranked")}, Claims: anySub},
		{Name: ptr("second"), Priority: ptr(2), TokenSpec: &oidc.TokenSpec{Username: ptr("second")}, Claims: anySub},
		{
			Name: ptr("p2"), Priority: ptr(1), Claims: anySub,
			TokenSpec: &oidc.TokenSpec{Username: ptr("p2"), Scope: ptr(`applied-permissions/roles:p2:"Viewer"`)},
		},
		{Name: ptr("first-a"), Priority: ptr(1), TokenSpec: &oidc.TokenSpec{Username: ptr("first-a")}, Claims: anySub},
		{Name: ptr("first-b"), Priority: ptr(1), TokenSpec: &oidc.TokenSpec{Username: ptr("first-b")}, Claims: anySub},
		{
			Name: ptr("p1"), Priority: ptr(9), Claims: anySub,
			TokenSpec: &oidc.TokenSpec{Username: ptr("p1"), Scope: ptr(`applied-permissions/roles:p1:"Viewer"`)},
		},
	} {
		if _, err := w.svc.CreateMapping(ctx, "ci", m); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	raw := w.mint(t, map[string]any{"sub": "x"})
	// Without a project, p2 competes on its priority and age with the global
	// mappings and wins (IDZ-64); with p3, p2 is another project's.
	for project, want := range map[string]struct{ user, project string }{
		"":   {"p2", "p2"},
		"p1": {"p1", "p1"},
		"p2": {"p2", "p2"},
		"p3": {"first-a", ""},
	} {
		got, err := w.exchange(raw, func(r *oidc.ExchangeRequest) { r.ProjectKey = project })
		if err != nil || got.Username != want.user || got.Record.ProjectKey != want.project {
			t.Errorf("project %q: %q in %q, %v; want %+v",
				project, got.Username, got.Record.ProjectKey, err, want)
		}
	}
	if got, err := w.exchange(
		raw,
		func(r *oidc.ExchangeRequest) { r.MappingName = "unranked" },
	); err != nil ||
		got.Username != "unranked" {
		t.Errorf("named mapping: %q, %v", got.Username, err)
	}
}

func TestClaimMatching(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		claims map[string]any
		token  map[string]any
		match  bool
	}{
		{
			"exact",
			map[string]any{"repository": "octo-org/app"},
			map[string]any{"repository": "octo-org/app"},
			true,
		},
		{
			"star stays in a segment",
			map[string]any{"repository": "octo-org/*"},
			map[string]any{"repository": "octo-org/a/b"},
			false,
		},
		{
			"star",
			map[string]any{"repository": "octo-org/*"},
			map[string]any{"repository": "octo-org/app"},
			true,
		},
		{
			"double star",
			map[string]any{"repository": "octo-org/**"},
			map[string]any{"repository": "octo-org/a/b"},
			true,
		},
		{
			"case",
			map[string]any{"repository": "Octo-org/app"},
			map[string]any{"repository": "octo-org/app"},
			false,
		},
		{
			"list of values",
			map[string]any{"ref": []any{"refs/heads/main", "refs/tags/*"}},
			map[string]any{"ref": "refs/tags/v1"},
			true,
		},
		{
			"list claim",
			map[string]any{"groups": "ops"},
			map[string]any{"groups": []any{"dev", "ops"}},
			true,
		},
		{"missing claim", map[string]any{"environment": "prod"}, map[string]any{"sub": "x"}, false},
		{
			"number",
			map[string]any{"run_attempt": float64(1)},
			map[string]any{"run_attempt": 1},
			true,
		},
		{"bool", map[string]any{"ci": true}, map[string]any{"ci": false}, false},
		{"string against number", map[string]any{"n": "1"}, map[string]any{"n": 1}, false},
		{
			"nested object",
			map[string]any{"ext": map[string]any{"team": "a*"}},
			map[string]any{"ext": map[string]any{"team": "alpha"}},
			true,
		},
		{
			"nested mismatch",
			map[string]any{"ext": map[string]any{"team": "a*"}},
			map[string]any{"ext": "flat"},
			false,
		},
		{
			"dotted path",
			map[string]any{"custom.department.group": "eng"},
			map[string]any{"custom": map[string]any{"department": map[string]any{"group": "eng"}}},
			true,
		},
		{
			"dotted path, not an object",
			map[string]any{"custom.department": "eng"},
			map[string]any{"custom": "eng"},
			false,
		},
		{
			"special characters",
			map[string]any{"sub": "repo:o/r:ref:refs/heads/(main)"},
			map[string]any{"sub": "repo:o/r:ref:refs/heads/(main)"},
			true,
		},
	}
	for i, c := range cases {
		name := "m" + string(rune('a'+i))
		if _, err := w.svc.CreateMapping(
			ctx,
			"ci",
			mapping(name, oidc.TokenSpec{Username: ptr("u")}, c.claims),
		); err != nil {
			t.Fatal(err)
		}
		_, err := w.exchange(
			w.mint(t, c.token),
			func(r *oidc.ExchangeRequest) { r.MappingName = name },
		)
		if c.match != (err == nil) || (!c.match && !errors.Is(err, oidc.ErrNoMatch)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestExchangeTokens(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	long := mapping(
		"long",
		oidc.TokenSpec{
			Username:  ptr("u"),
			ExpiresIn: ptr(int64(7 * 24 * 3600)),
			Audience:  ptr("jfrt@*"),
		},
		nil,
	)
	short := mapping("default", oidc.TokenSpec{Username: ptr("u")}, nil)
	groups := mapping("groups", oidc.TokenSpec{Scope: ptr("applied-permissions/groups:ci")}, nil)
	for _, m := range []oidc.Mapping{long, short, groups} {
		if _, err := w.svc.CreateMapping(ctx, "ci", m); err != nil {
			t.Fatal(err)
		}
	}
	raw := w.mint(t, nil)
	got, err := w.exchange(raw, func(r *oidc.ExchangeRequest) {
		r.MappingName, r.IncludeReference = "long", true
		r.Repo, r.RunID, r.Branch = "o/r", "42", "main"
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresIn != int64(oidc.MaxExpiry/time.Second) || got.Record.Audience != "jfrt@*" ||
		got.ReferenceToken == "" || got.Record.Identity != types.IdentityTransient ||
		got.Record.Description != "OIDC token exchange through ci/long (repo=o/r, branch=main, run_id=42)" {
		t.Fatalf("long = %+v", got)
	}
	if got, err := w.exchange(
		raw,
		func(r *oidc.ExchangeRequest) { r.MappingName = "default" },
	); err != nil ||
		got.ExpiresIn != int64(oidc.DefaultExpiry/time.Second) ||
		got.Record.Scope.String() != "applied-permissions/user" {
		t.Fatalf("default = %+v, %v", got, err)
	}
	got, err = w.exchange(raw, func(r *oidc.ExchangeRequest) { r.MappingName = "groups" })
	if err != nil || got.Username != "oidc-ci-groups" ||
		got.Record.Identity != types.IdentitySynthetic {
		t.Fatalf("groups = %+v, %v", got, err)
	}
	if !strings.HasSuffix(got.Record.Subject, "/users/oidc-ci-groups") {
		t.Fatalf("subject = %q", got.Record.Subject)
	}
}

func TestExchangeRefuses(t *testing.T) {
	w := newWorld(t)
	raw := w.mint(t, nil)
	for name, c := range map[string]struct {
		mod  func(*oidc.ExchangeRequest)
		want error
	}{
		"grant":          {func(r *oidc.ExchangeRequest) { r.GrantType = "x" }, oidc.ErrInvalid},
		"token type":     {func(r *oidc.ExchangeRequest) { r.SubjectTokenType = "x" }, oidc.ErrInvalid},
		"no token":       {func(r *oidc.ExchangeRequest) { r.SubjectToken = "" }, oidc.ErrInvalid},
		"no provider":    {func(r *oidc.ExchangeRequest) { r.ProviderName = "" }, oidc.ErrInvalid},
		"other provider": {func(r *oidc.ExchangeRequest) { r.ProviderName = "nope" }, oidc.ErrInvalid},
		"no mapping":     {func(*oidc.ExchangeRequest) {}, oidc.ErrNoMatch},
		"named mapping":  {func(r *oidc.ExchangeRequest) { r.MappingName = "nope" }, oidc.ErrInvalid},
		"garbage token":  {func(r *oidc.ExchangeRequest) { r.SubjectToken = "x" }, oidc.ErrUnverified},
	} {
		if _, err := w.exchange(raw, c.mod); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// The message names the cause, not the sentinel.
	_, err := w.exchange(raw, func(r *oidc.ExchangeRequest) { r.ProviderName = "nope" })
	if err == nil || err.Error() != "OIDC provider 'nope' does not exist" {
		t.Errorf("message = %v", err)
	}
}

// TestProviderTypes: GitHub tokens must come from the organisation unless
// the provider is permissive; a generic provider's token_issuer replaces the
// iss a token must carry; Azure asks for the application's keys.
func TestProviderTypes(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.svc
	issuerURL := w.issuer.URL()
	add := func(p oidc.Provider) {
		t.Helper()
		if _, err := s.CreateProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateMapping(
			ctx,
			*p.Name,
			mapping("m", oidc.TokenSpec{Username: ptr("u")}, nil),
		); err != nil {
			t.Fatal(err)
		}
	}
	gh := provider("gh", issuerURL)
	gh.ProviderType, gh.Organization, gh.TokenIssuer = ptr(
		"GitHub",
	), ptr(
		"octo-org",
	), ptr(
		"octo-org",
	)
	add(gh)
	open := provider("open", issuerURL)
	open.ProviderType, open.EnablePermissiveConfiguration = ptr("GitHub"), ptr(true)
	add(open)
	moved := provider("moved", issuerURL)
	moved.TokenIssuer = ptr("https://ci.example")
	add(moved)
	az := provider("az", issuerURL)
	az.ProviderType, az.AzureAppID = ptr("Azure"), ptr("app-1")
	add(az)

	try := func(provider string, claims map[string]any) error {
		raw, err := w.issuer.Mint("any", time.Hour, claims)
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.exchange(raw, func(r *oidc.ExchangeRequest) { r.ProviderName = provider })
		return err
	}
	if err := try("gh", map[string]any{"repository_owner": "octo-org"}); err != nil {
		t.Errorf("GitHub, its organisation: %v", err)
	}
	if err := try(
		"gh",
		map[string]any{"repository_owner": "other"},
	); !errors.Is(
		err,
		oidc.ErrUnverified,
	) {
		t.Errorf("GitHub, another organisation: %v", err)
	}
	if err := try("open", map[string]any{"repository_owner": "other"}); err != nil {
		t.Errorf("permissive GitHub: %v", err)
	}
	if err := try("moved", map[string]any{"iss": "https://ci.example"}); err != nil {
		t.Errorf("token issuer: %v", err)
	}
	if err := try("moved", nil); !errors.Is(err, oidc.ErrUnverified) {
		t.Errorf("token issuer, URL in iss: %v", err)
	}
	if err := try("az", nil); err != nil {
		t.Errorf("Azure: %v", err)
	}
	if ids := w.issuer.AppIDs(); ids[len(ids)-1] != "app-1" {
		t.Errorf("Azure app ids = %v", ids)
	}
}

func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{
		"list oidc", "get oidc", "put oidc", "delete oidc",
		"list oidc_identity_mapping", "get oidc_identity_mapping",
		"put oidc_identity_mapping", "delete oidc_identity_mapping",
	} {
		w := newWorld(t)
		s := w.svc
		if _, err := s.CreateMapping(
			ctx,
			"ci",
			mapping("m", oidc.TokenSpec{Username: ptr("u")}, nil),
		); err != nil {
			t.Fatal(err)
		}
		w.db.fail = op
		raw := w.mint(t, nil)
		errs := []error{}
		_, err := s.Providers(ctx)
		errs = append(errs, err)
		_, err = s.CreateProvider(ctx, provider("new", "https://x"))
		errs = append(errs, err)
		_, err = s.UpdateProvider(ctx, "ci", provider("ci", w.issuer.URL()))
		errs = append(errs, err)
		_, err = s.Mappings(ctx, "ci", "")
		errs = append(errs, err)
		_, err = s.CreateMapping(ctx, "ci", mapping("n", oidc.TokenSpec{Username: ptr("u")}, nil))
		errs = append(errs, err)
		_, err = s.UpdateMapping(
			ctx,
			"ci",
			"m",
			mapping("m", oidc.TokenSpec{Username: ptr("u")}, nil),
		)
		errs = append(errs, err)
		_, err = w.exchange(raw)
		errs = append(errs, err)
		errs = append(errs, s.DeleteMapping(ctx, "ci", "m"), s.DeleteProvider(ctx, "ci"))
		failed := false
		for _, err := range errs {
			if errors.Is(err, errBoom) {
				failed = true
			}
			if err != nil && !errors.Is(err, errBoom) && !errors.Is(err, oidc.ErrNotFound) &&
				!errors.Is(err, oidc.ErrInvalid) && !errors.Is(err, oidc.ErrNoMatch) {
				t.Errorf("%s: unexpected %v", op, err)
			}
		}
		if !failed {
			t.Errorf("%s: no operation surfaced the failure", op)
		}
	}
}

func TestCorruptDocuments(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.svc
	for _, d := range []types.ConfigDocument{
		{Kind: types.ConfigOIDC, Key: "bad", Document: []byte("{")},
		{Kind: types.ConfigOIDCMapping, Key: "ci/bad", Document: []byte("{")},
	} {
		if err := w.db.Store.Configs().Put(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Providers(ctx); err == nil {
		t.Error("Providers read a corrupt document")
	}
	if _, err := s.Provider(ctx, "bad"); err == nil {
		t.Error("Provider read a corrupt document")
	}
	if _, err := s.Mappings(ctx, "ci", ""); err == nil {
		t.Error("Mappings read a corrupt document")
	}
	if _, err := s.Mapping(ctx, "ci", "bad"); err == nil {
		t.Error("Mapping read a corrupt document")
	}
}
