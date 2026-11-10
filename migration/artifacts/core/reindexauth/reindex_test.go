package reindexauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitQueuedTokenKeepsScopeAndCurrentAuthority(t *testing.T) {
	for _, change := range []string{"allowed", "read scope", "expired", "revoked", "permission", "disabled", "anonymous off", "missing reference", "wrong name", "registry"} {
		t.Run(change, func(t *testing.T) {
			st := inmem.New()
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			if err := st.Users().Create(t.Context(), types.User{Name: "manager"}); err != nil {
				t.Fatal(err)
			}
			if err := st.Permissions().
				Create(t.Context(), types.Permission{Name: "manage", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"manager": types.ActionManage}}, Targets: map[string]types.Target{"remote": {Include: []string{"**"}}}}}}); err != nil {
				t.Fatal(err)
			}
			scopeText := "applied-permissions/user"
			if change == "read scope" {
				scopeText = "applied-permissions/groups:readers"
			}
			scope, err := types.ParseScope(scopeText)
			if err != nil {
				t.Fatal(err)
			}
			tok := types.Token{
				ID:        "opaque-id",
				Subject:   "jfac@test/users/manager",
				Scope:     scope,
				ExpiresAt: now.Add(time.Hour),
			}
			if err = st.Tokens().Create(t.Context(), tok); err != nil {
				t.Fatal(err)
			}
			ref := kernel.CredentialReference{Name: "manager", TokenID: tok.ID}
			by := "manager"
			switch change {
			case "expired":
				now = now.Add(2 * time.Hour)
			case "revoked":
				if err = st.Tokens().Delete(t.Context(), tok.ID); err != nil {
					t.Fatal(err)
				}
			case "permission":
				if err = st.Permissions().Delete(t.Context(), "manage"); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err = st.Users().
					Update(t.Context(), types.User{Name: "manager", Status: types.UserDisabled}); err != nil {
					t.Fatal(err)
				}
			case "anonymous off":
				ref = kernel.Anonymous.CredentialReference()
				by = kernel.AnonymousUser
			case "wrong name":
				by = "other"
			case "registry":
				ref.Registry = &kernel.RegistryReference{}
			}
			raw, err := json.Marshal(ref)
			if err != nil {
				t.Fatal(err)
			}
			if change == "missing reference" {
				raw = nil
			}
			req := format.WorkRequest{By: by, Credential: raw}
			c := repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NuGet}
			api := bind(t, st, clock)
			check := reindexauth.New(api.Authenticator, api.Authorizer)
			err = check(t.Context(), c, req)
			if change == "allowed" {
				if err != nil {
					t.Fatal(err)
				}
				absent := bind(t, st, clock)
				absent.Authenticator = nil
				if err = reindexauth.New(
					absent.Authenticator,
					absent.Authorizer,
				)(
					t.Context(),
					c,
					req,
				); !errors.Is(
					err,
					work.ErrPermanent,
				) {
					t.Fatal("absent authenticator", err)
				}
				failing := bind(t, permissionFailure{st}, clock)
				if err = reindexauth.New(
					failing.Authenticator,
					failing.Authorizer,
				)(
					t.Context(),
					c,
					req,
				); !errors.Is(err, errStore) ||
					errors.Is(err, work.ErrPermanent) {
					t.Fatal("permission storage fault must retry", err)
				}
				failing = bind(t, tokenFailure{st}, clock)
				if err = reindexauth.New(
					failing.Authenticator,
					failing.Authorizer,
				)(
					t.Context(),
					c,
					req,
				); !errors.Is(err, errStore) ||
					errors.Is(err, work.ErrPermanent) {
					t.Fatal("credential storage fault must retry", err)
				}
			} else if !errors.Is(err, work.ErrPermanent) {
				t.Fatal("revoked or narrowed caller accepted", err)
			}
		})
	}
}

func TestUnitReindexRefusesCallerThatLostManage(t *testing.T) {
	c := repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NuGet}
	raw, err := json.Marshal(kernel.CredentialReference{Name: "manager", TokenID: "opaque-id"})
	if err != nil {
		t.Fatal(err)
	}
	req := format.WorkRequest{By: "manager", Credential: raw}
	manage := true
	authz := &accessapitest.Authorizer{DecideFunc: func(
		_ context.Context, s accessapi.Subject, _ bool,
	) (accessapi.Decider, error) {
		return &accessapitest.Decider{AllowsFunc: func(
			rt accessapi.ResourceType, r accessapi.Repo, path string, want accessapi.Actions,
		) bool {
			return manage && s.Name == "manager" && rt == types.ResourceArtifact &&
				r == accessapi.Repo{Key: c.Key, Class: string(c.Class)} && path == "" &&
				want == types.ActionManage
		}}, nil
	}}
	check := reindexauth.New(&accessapitest.Authenticator{}, authz)
	if err := check(t.Context(), c, req); err != nil {
		t.Fatalf("caller holding Manage on the repository refused: %v", err)
	}
	manage = false
	if err := check(t.Context(), c, req); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("caller that lost Manage accepted: %v", err)
	}
}

// bind binds the access module over st on the clock now, as the application
// binds its store and authenticator. The API reads everything from st, so a
// store that fails permissions or tokens fails the Authorizer or the
// credential check.
func bind(t *testing.T, st accesstest.Store, now func() time.Time) accessapi.API {
	t.Helper()
	return accesstest.Over(t, st, accesstest.WithNow(now)).API()
}

var errStore = errors.New("permission storage failure")

type permissionFailure struct{ accesstest.Store }

func (s permissionFailure) Permissions() store.PermissionStore {
	return brokenPermissions{s.Store.Permissions()}
}

type brokenPermissions struct{ store.PermissionStore }

func (p brokenPermissions) ForPrincipal(
	context.Context, string, []string,
) ([]types.Permission, error) {
	return nil, errStore
}

type tokenFailure struct{ accesstest.Store }

func (s tokenFailure) Tokens() store.TokenStore { return brokenTokens{s.Store.Tokens()} }

type brokenTokens struct{ store.TokenStore }

func (t brokenTokens) Get(context.Context, string) (types.Token, error) {
	return types.Token{}, errStore
}
