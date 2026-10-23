package golang_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type matrixFixture struct {
	h       http.Handler
	db      store.Store
	items   *item.Service
	secrets map[string]string
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	var tokens *token.Service
	var blobs blob.Store
	h, db, _ := goServer(t, func(cfg *server.Config) {
		tokens, blobs = cfg.Tokens, cfg.Blobs
		for _, c := range []repo.Config{
			{Key: "libs-local", Class: repo.Local},
			{Key: "go-remote", Class: repo.Remote, PackageType: repo.Go, URL: "https://up.example"},
			{
				Key: "go-virtual", Class: repo.Virtual, PackageType: repo.Go,
				Repositories: []string{"go-local"}, DefaultDeploymentRepo: "go-local",
			},
		} {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{
			"nobody", "reader", "writer", "member", "charter", "deployer",
			"gadmin", "resmgr", "gresmgr",
		} {
			user := types.User{Name: name, PasswordHash: cheapHash}
			if name == "resmgr" {
				user.Roles.ResourcesManager = true
			}
			if err := cfg.Store.Users().Create(t.Context(), user); err != nil {
				t.Fatal(err)
			}
		}
		for _, group := range []types.Group{
			{Name: "team", Members: []string{"member"}},
			{Name: "admins", Members: []string{"gadmin"}, AdminPrivileges: true},
			{Name: "res-managers", Members: []string{"gresmgr"}, Roles: types.RoleFlags{ResourcesManager: true}},
		} {
			if err := cfg.Store.Groups().Create(t.Context(), group); err != nil {
				t.Fatal(err)
			}
		}
		for _, grant := range []struct {
			name, under string
			users       map[string]types.Actions
			groups      map[string]types.Actions
		}{
			{"reader", "**", map[string]types.Actions{"reader": types.ActionRead}, nil},
			{
				"writer", "work/**",
				map[string]types.Actions{"writer": types.ActionRead | types.ActionWrite | types.ActionDelete},
				nil,
			},
			{
				"team", "**",
				nil,
				map[string]types.Actions{"team": types.ActionRead | types.ActionWrite | types.ActionDelete | types.ActionAnnotate},
			},
		} {
			targets := map[string]types.Target{}
			for _, key := range []string{"go-local", "go-remote", "go-virtual"} {
				targets[key] = types.Target{Include: []string{grant.under}}
			}
			if err := cfg.Store.Permissions().Create(t.Context(), types.Permission{
				Name: "go-matrix-" + grant.name,
				Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
					Actions: types.Grants{
						Users:  grant.users,
						Groups: grant.groups,
					},
					Targets: targets,
				}},
			}); err != nil {
				t.Fatal(err)
			}
		}
	})
	f := &matrixFixture{h: h, db: db, items: &item.Service{Store: db, Blobs: blobs}}
	f.secrets = seedGoMatrixTokens(t, db, tokens)
	goMatrixPart.seed(t, f)
	return f
}

func seedGoMatrixTokens(t *testing.T, db store.Store, svc *token.Service) map[string]string {
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
			issued, err := svc.IssueRegistry(ctx, token.RegistryRequest{
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
		hour := time.Hour
		issuer := svc
		if p.name == "tok-expired" {
			issuer = past
		}
		issued, err := issuer.Issue(ctx, token.Request{
			Username: p.user, Scope: scope, ExpiresIn: &hour,
			ByAdmin: true, ForceRevocable: true, Identity: p.identity,
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.name == "tok-revoked" {
			if err := svc.Revoke(ctx, issued.Record.ID); err != nil {
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
	header := map[string]string{}
	switch {
	case p.token:
		header = bearerAuth(f.secrets[p.name])
	case !p.anonymous:
		header = basic(p.user, "pw")
	}
	fill := strings.NewReplacer("{p}", p.name, "{u}", p.user)
	body := fill.Replace(r.body)
	if body != "" {
		header["Content-Type"], body = contentTypeOf(body)
	}
	rec := do(t, f.h, r.method(), fill.Replace(r.path), header, body)
	return rec.Code, rec.Body.String()
}

func TestGoRoutePrincipalMatrix(t *testing.T) {
	for _, row := range goMatrixPart.rows() {
		t.Run(row.route, func(t *testing.T) {
			var f *matrixFixture
			for _, p := range matrixPrincipals {
				if f == nil || row.fresh {
					f = newMatrixFixture(t)
					for _, seed := range matrixPrincipals {
						if row.seed != nil {
							row.seed(t, f, seed.name)
						}
					}
				}
				if got, body := f.call(t, row, p); got != row.want(p) {
					t.Errorf("%s %s as %s: status %d, want %d: %s",
						row.method(), row.path, p.name, got, row.want(p), body)
				}
			}
		})
	}
}
