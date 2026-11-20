package docker_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
	"github.com/weaveplatform/artifactweave/test/server/support/registryfixture"
)

type matrixFixture struct {
	h       http.Handler
	db      harness.Store
	items   *item.Service
	secrets map[string]string
	uploads map[string]string
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	f := &matrixFixture{uploads: map[string]string{}}
	setup := func(cfg *Config) {
		ctx := t.Context()
		f.db = cfg.Store
		f.items = &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Formats: ecosystem.For}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"nobody", "writer", "member", "charter", "gadmin", "resmgr", "gresmgr"} {
			user := accesstest.User{Name: name, PasswordHash: cheapHash}
			if name == "resmgr" {
				user.Roles.ResourcesManager = true
			}
			must(cfg.Store.Users().Create(ctx, user))
		}
		must(cfg.Store.Groups().Create(ctx, accesstest.Group{
			Name: "team", Members: []string{"member"},
		}))
		must(cfg.Store.Groups().Create(ctx, accesstest.Group{
			Name: "ops", Members: []string{"gadmin"}, AdminPrivileges: true,
		}))
		must(cfg.Store.Groups().Create(ctx, accesstest.Group{
			Name: "res-managers", Members: []string{"gresmgr"},
			Roles: accesstest.RoleFlags{ResourcesManager: true},
		}))
		for _, c := range matrixRegistryRepos() {
			must(cfg.Store.Repos().Create(ctx, *repo.Defaults(&c)))
		}
		for _, grant := range []struct {
			name, under string
			users       map[string]api.Actions
			groups      map[string]api.Actions
		}{
			{"reader", "**", map[string]api.Actions{"reader": api.ActionRead}, nil},
			{"writer", "work/**", map[string]api.Actions{"writer": rwd}, nil},
			{"team", "**", nil, map[string]api.Actions{"team": rawd}},
		} {
			must(cfg.Store.Permissions().Create(ctx, accesstest.Permission{
				Name: "matrix-docker-" + grant.name,
				Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
					Actions: accesstest.Grants{Users: grant.users, Groups: grant.groups},
					Targets: map[string]accesstest.Target{
						dockerA: {Include: []string{grant.under}},
					},
				}},
			}))
		}
		// Seed the token issuer before deployments can lazily create an event origin.
		must(cfg.Store.Instance().Init(ctx, instance.InstanceIdentity{
			InstanceID:      "jfrt@01234567-89ab-4cde-8f01-23456789abcd",
			ProtocolVersion: 1,
		}))
		seedMatrixRegistry(t, f)
		seedMatrixManifests(t, f)
		tokens, err := token.New(ctx, cfg.Store, token.Options{KeyBits: 1024})
		must(err)
		cfg.Tokens = tokens
		cfg.PasswordHash = testHash
		cfg.Auth, err = authn.NewAuthenticator(
			cfg.Store.Users(),
			cfg.Store.Settings(),
			authn.Options{
				Hash: testHash, Tokens: tokens,
			},
		)
		must(err)
		f.secrets = seedDockerMatrixTokens(t, cfg.Store, tokens)
	}
	w := registryfixture.New(t, false, setup)
	f.h = w.Handler
	return f
}

func seedDockerMatrixTokens(
	t *testing.T,
	db harness.Store,
	tokens *token.Service,
) map[string]string {
	t.Helper()
	ctx := t.Context()
	past, err := token.New(ctx, db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-90 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{}
	for _, p := range matrixPrincipals {
		if !p.token {
			continue
		}
		if p.registry {
			issued, err := tokens.IssueRegistry(ctx, token.RegistryRequest{
				Username: p.user, ExpiresIn: time.Hour, Identity: p.identity,
			})
			if err != nil {
				t.Fatal(err)
			}
			secrets[p.name] = issued.AccessToken
			continue
		}
		scope, err := kernel.ParseScope(p.scope)
		if err != nil {
			t.Fatal(err)
		}
		issuer := tokens
		if p.name == "tok-expired" {
			issuer = past
		}
		hour := time.Hour
		issued, err := issuer.Issue(ctx, token.Request{
			Username: p.user, Scope: scope, ExpiresIn: &hour,
			ByAdmin: true, ForceRevocable: true, Identity: p.identity,
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.name == "tok-revoked" {
			if err := tokens.Revoke(ctx, issued.Record.ID); err != nil {
				t.Fatal(err)
			}
		}
		secrets[p.name] = issued.AccessToken
	}
	return secrets
}

func (f *matrixFixture) deploy(t *testing.T, key, path string, body []byte) repo.Config {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *matrixFixture) call(t *testing.T, r matrixRow, p matrixPrincipal) (int, string) {
	t.Helper()
	if err := f.db.Settings().
		Put(t.Context(), settings.SettingAnonymousAccess, p.anonymousOn); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{}
	switch {
	case p.token:
		headers = bearerAuth(f.secrets[p.name])
	case !p.anonymous:
		headers = basic(p.user, "pw")
	}
	fill := strings.NewReplacer(
		"{p}", p.name, "{u}", p.user, "{upload}", f.uploads[p.name],
	)
	body := fill.Replace(r.body)
	if body != "" {
		headers["Content-Type"], body = contentTypeOf(body)
	}
	rec := do(t, f.h, r.method(), fill.Replace(r.path), headers, body)
	return rec.Code, rec.Body.String()
}

func TestAccDockerFixturesKeepSeededEventOrigin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(*testing.T) harness.Store
	}{
		{"principal matrix", func(t *testing.T) harness.Store { return newMatrixFixture(t).db }},
		{"status table", func(t *testing.T) harness.Store {
			var db harness.Store
			newStatusFixture(t, false, func(c *Config) { db = c.Store })
			return db
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := tc.store(t)
			const origin = "jfrt@01234567-89ab-4cde-8f01-23456789abcd"
			if id, err := db.Instance().ID(t.Context()); err != nil || id != origin {
				t.Fatalf("fixture identity = %q, %v; want %q", id, err, origin)
			}
			if _, err := db.Events().RegisterConsumer(t.Context(), origin, "fixture-origin"); err != nil {
				t.Fatal(err)
			}
			events, _, err := db.Events().Next(t.Context(), origin, "fixture-origin", 1000)
			if err != nil {
				t.Fatal(err)
			}
			topics := make(map[string]bool)
			for _, event := range events {
				if event.Origin != origin {
					t.Fatalf("seed event origin = %q; want %q", event.Origin, origin)
				}
				topics[event.Topic] = true
			}
			if !topics[artapi.ArtifactDeployed] || !topics[artapi.DockerPushed] {
				t.Fatalf("seed event topics = %v; want artifact deployment and Docker push", topics)
			}
		})
	}
}

func TestAccDockerRoutePrincipalMatrix(t *testing.T) {
	for _, r := range dockerMatrixRows() {
		t.Run(r.route, func(t *testing.T) {
			t.Parallel()
			var f *matrixFixture
			for _, p := range matrixPrincipals {
				if f == nil || r.fresh {
					f = newMatrixFixture(t)
					for _, seed := range matrixPrincipals {
						if r.seed != nil {
							r.seed(t, f, seed.name)
						}
					}
				}
				if got, body := f.call(t, r, p); got != r.want(p) {
					t.Errorf("%s %s as %s: status %d, want %d: %s",
						r.method(), r.path, p.name, got, r.want(p), body)
				}
			}
		})
	}
}
