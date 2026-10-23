package npm_test

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
	var blobs blob.Store
	var tokens *token.Service
	h, db, _ := npmServer(t, func(cfg *server.Config) {
		blobs, tokens = cfg.Blobs, cfg.Tokens
		for _, c := range append([]repo.Config{{Key: "libs-local", Class: repo.Local}}, npmRepos[1:]...) {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range matrixPrincipals {
			if err := cfg.Store.Users().
				Create(t.Context(), types.User{Name: "v-" + p.name, PasswordHash: cheapHash}); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"nobody", "reader", "writer", "deployer", "member", "gadmin", "charter", "resmgr", "gresmgr"} {
			role := types.RoleFlags{ResourcesManager: name == "resmgr"}
			if err := cfg.Store.Users().
				Create(t.Context(), types.User{Name: name, PasswordHash: cheapHash, Roles: role}); err != nil {
				t.Fatal(err)
			}
		}
		for _, group := range []types.Group{
			{Name: "team", Members: []string{"member"}},
			{Name: "ops", Members: []string{"gadmin"}, AdminPrivileges: true},
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
			{"writer", "work/**", map[string]types.Actions{"writer": rwd}, nil},
			{"team", "**", nil, map[string]types.Actions{"team": rawd}},
		} {
			targets := map[string]types.Target{}
			for _, c := range npmRepos {
				targets[c.Key] = types.Target{Include: []string{grant.under}}
			}
			if err := cfg.Store.Permissions().Create(t.Context(), types.Permission{
				Name: "npm-matrix-" + grant.name,
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
	f.secrets = seedNPMMatrixTokens(t, db, tokens)
	npmMatrixPart.seed(t, f)
	return f
}

func seedNPMMatrixTokens(t *testing.T, db store.Store, svc *token.Service) map[string]string {
	t.Helper()
	ctx := t.Context()
	past, err := token.New(ctx, db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-90 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(s *token.Service, user, scope string, id types.TokenIdentity) token.Issued {
		t.Helper()
		parsed, err := types.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		hour := time.Hour
		out, err := s.Issue(ctx, token.Request{
			Username: user, Scope: parsed, ExpiresIn: &hour,
			ByAdmin: true, ForceRevocable: true, Identity: id,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	secrets := map[string]string{}
	for _, p := range matrixPrincipals {
		switch {
		case p.name == "tok-expired":
			secrets[p.name] = issue(past, p.user, p.scope, p.identity).AccessToken
		case p.registry:
			out, err := svc.IssueRegistry(ctx, token.RegistryRequest{
				Username: p.user, ExpiresIn: time.Hour, Identity: p.identity,
			})
			if err != nil {
				t.Fatal(err)
			}
			secrets[p.name] = out.AccessToken
		case p.name == "tok-revoked":
			out := issue(svc, p.user, p.scope, p.identity)
			if err := svc.Revoke(ctx, out.Record.ID); err != nil {
				t.Fatal(err)
			}
			secrets[p.name] = out.AccessToken
		case p.token:
			secrets[p.name] = issue(svc, p.user, p.scope, p.identity).AccessToken
		}
		secrets["v-"+p.name] = issue(
			svc,
			"v-"+p.name,
			"applied-permissions/user",
			types.IdentityStored,
		).AccessToken
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
	fill := strings.NewReplacer("{p}", p.name, "{u}", p.user, "{token}", f.secrets["v-"+p.name])
	headers := map[string]string{}
	switch {
	case p.token:
		headers = bearerAuth(f.secrets[p.name])
	case !p.anonymous:
		headers = basic(p.user, "pw")
	}
	body := fill.Replace(r.body)
	if body != "" {
		headers["Content-Type"], body = contentTypeOf(body)
	}
	rec := do(t, f.h, r.method(), fill.Replace(r.path), headers, body)
	return rec.Code, rec.Body.String()
}

func TestNPMRoutePrincipalMatrix(t *testing.T) {
	for _, part := range []matrixPart{npmMatrixPart, npmAuthMatrixPart, npmLifecycleMatrixPart} {
		for _, r := range part.rows() {
			t.Run(r.route, func(t *testing.T) {
				var f *matrixFixture
				for _, p := range matrixPrincipals {
					if f == nil || r.fresh {
						f = newMatrixFixture(t)
						for _, q := range matrixPrincipals {
							if r.seed != nil {
								r.seed(t, f, q.name)
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
}
