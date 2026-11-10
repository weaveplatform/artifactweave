package maven_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type matrixFixture struct {
	h       http.Handler
	db      *inmem.Store
	items   *item.Service
	secrets map[string]string
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	var tokens *token.Service
	var blobs blob.Store
	h, db, _ := mavenServer(t, func(cfg *harness.Config) {
		tokens, blobs = cfg.Tokens, cfg.Blobs
		for _, c := range []repo.Config{
			{Key: "libs-local", Class: repo.Local},
			mavenRepos[3],
			{Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven, URL: "https://up.example"},
			{
				Key: "maven-virtual", Class: repo.Virtual, PackageType: repo.Maven,
				Repositories: []string{"maven-local"}, DefaultDeploymentRepo: "maven-local",
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
			user := model.User{Name: name, PasswordHash: cheapHash}
			if name == "resmgr" {
				user.Roles.ResourcesManager = true
			}
			if err := cfg.Store.Users().Create(t.Context(), user); err != nil {
				t.Fatal(err)
			}
		}
		for _, group := range []model.Group{
			{Name: "team", Members: []string{"member"}},
			{Name: "admins", Members: []string{"gadmin"}, AdminPrivileges: true},
			{Name: "res-managers", Members: []string{"gresmgr"}, Roles: model.RoleFlags{ResourcesManager: true}},
		} {
			if err := cfg.Store.Groups().Create(t.Context(), group); err != nil {
				t.Fatal(err)
			}
		}
		for _, grant := range []struct {
			name, under string
			users       map[string]api.Actions
			groups      map[string]api.Actions
		}{
			{"reader", "**", map[string]api.Actions{"reader": api.ActionRead}, nil},
			{
				"writer", "work/**",
				map[string]api.Actions{"writer": api.ActionRead | api.ActionWrite | api.ActionDelete},
				nil,
			},
			{
				"team", "**",
				nil,
				map[string]api.Actions{"team": api.ActionRead | api.ActionWrite | api.ActionDelete | api.ActionAnnotate},
			},
		} {
			targets := map[string]model.Target{}
			for _, key := range []string{"maven-local", "maven-remote", "maven-virtual", "maven-releases"} {
				targets[key] = model.Target{Include: []string{grant.under}}
			}
			if err := cfg.Store.Permissions().Create(t.Context(), model.Permission{
				Name: "maven-matrix-" + grant.name,
				Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
					Actions: model.Grants{
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
	f := &matrixFixture{h: h, db: db, items: &item.Service{
		Store: db, Blobs: blobs, Formats: ecosystem.For,
	}}
	f.secrets = seedMavenMatrixTokens(t, db, tokens)
	mavenMatrixPart.seed(t, f)
	return f
}

func seedMavenMatrixTokens(t *testing.T, db *inmem.Store, svc *token.Service) map[string]string {
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
		scope, err := kernel.ParseScope(p.scope)
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
		Put(t.Context(), settings.SettingAnonymousAccess, p.anonymousOn); err != nil {
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

func TestAccMavenRoutePrincipalMatrix(t *testing.T) {
	for _, row := range mavenMatrixPart.rows() {
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
