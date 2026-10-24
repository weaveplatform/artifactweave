package docker_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/registryfixture"
)

type matrixFixture struct {
	h       http.Handler
	db      store.Store
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
		f.items = &item.Service{Store: cfg.Store, Blobs: cfg.Blobs}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"nobody", "writer", "member", "charter", "gadmin", "resmgr", "gresmgr"} {
			user := types.User{Name: name, PasswordHash: cheapHash}
			if name == "resmgr" {
				user.Roles.ResourcesManager = true
			}
			must(cfg.Store.Users().Create(ctx, user))
		}
		must(cfg.Store.Groups().Create(ctx, types.Group{
			Name: "team", Members: []string{"member"},
		}))
		must(cfg.Store.Groups().Create(ctx, types.Group{
			Name: "ops", Members: []string{"gadmin"}, AdminPrivileges: true,
		}))
		must(cfg.Store.Groups().Create(ctx, types.Group{
			Name: "res-managers", Members: []string{"gresmgr"},
			Roles: types.RoleFlags{ResourcesManager: true},
		}))
		for _, c := range matrixRegistryRepos() {
			must(cfg.Store.Repos().Create(ctx, *repo.Defaults(&c)))
		}
		for _, grant := range []struct {
			name, under string
			users       map[string]types.Actions
			groups      map[string]types.Actions
		}{
			{"reader", "**", map[string]types.Actions{"reader": types.ActionRead}, nil},
			{"writer", "work/**", map[string]types.Actions{"writer": rwd}, nil},
			{"team", "**", nil, map[string]types.Actions{"team": rawd}},
		} {
			must(cfg.Store.Permissions().Create(ctx, types.Permission{
				Name: "matrix-docker-" + grant.name,
				Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
					Actions: types.Grants{Users: grant.users, Groups: grant.groups},
					Targets: map[string]types.Target{dockerA: {Include: []string{grant.under}}},
				}},
			}))
		}
		seedMatrixRegistry(t, f)
		seedMatrixManifests(t, f)
		must(cfg.Store.Instance().Init(ctx, types.InstanceIdentity{
			InstanceID:      "jfrt@01234567-89ab-4cde-8f01-23456789abcd",
			ProtocolVersion: 1,
		}))
		tokens, err := token.New(ctx, cfg.Store, token.Options{KeyBits: 1024})
		must(err)
		cfg.Tokens = tokens
		cfg.PasswordHash = testHash
		cfg.Auth, err = auth.NewAuthenticator(cfg.Store.Users(), cfg.Store.Settings(), auth.Options{
			Hash: testHash, Tokens: tokens,
		})
		must(err)
		f.secrets = seedDockerMatrixTokens(t, cfg.Store, tokens)
	}
	w := registryfixture.New(t, false, setup)
	f.h = w.Handler
	return f
}

func seedDockerMatrixTokens(t *testing.T, db store.Store, tokens *token.Service) map[string]string {
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
		scope, err := types.ParseScope(p.scope)
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
		Put(t.Context(), types.SettingAnonymousAccess, p.anonymousOn); err != nil {
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
