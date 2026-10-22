package reindexauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestQueuedTokenKeepsScopeAndCurrentAuthority(t *testing.T) {
	for _, change := range []string{"allowed", "read scope", "expired", "revoked", "permission", "disabled", "anonymous off", "missing reference", "wrong name", "registry"} {
		t.Run(change, func(t *testing.T) {
			st := inmem.New()
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			a, err := auth.NewAuthenticator(
				st.Users(),
				st.Settings(),
				auth.Options{
					Hash: auth.HashParams{MemoryKiB: 8, Time: 1, Threads: 1},
					Now:  func() time.Time { return now },
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.Users().Create(t.Context(), types.User{Name: "manager"}); err != nil {
				t.Fatal(err)
			}
			if err = st.Permissions().
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
			ref := auth.CredentialReference{Name: "manager", TokenID: tok.ID}
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
				ref = auth.Anonymous.CredentialReference()
				by = auth.AnonymousUser
			case "wrong name":
				by = "other"
			case "registry":
				ref.Registry = &auth.RegistryReference{}
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
			check := reindexauth.New(st, a)
			err = check(t.Context(), c, req)
			if change == "allowed" {
				if err != nil {
					t.Fatal(err)
				}
				if err = reindexauth.New(
					st,
					nil,
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
				if err = reindexauth.New(
					permissionFailure{st},
					a,
				)(
					t.Context(),
					c,
					req,
				); !errors.Is(err, errStore) ||
					errors.Is(err, work.ErrPermanent) {
					t.Fatal("permission storage fault must retry", err)
				}
			} else if !errors.Is(err, work.ErrPermanent) {
				t.Fatal("revoked or narrowed caller accepted", err)
			}
		})
	}
}

var errStore = errors.New("permission storage failure")

type permissionFailure struct{ store.Store }

func (s permissionFailure) Permissions() store.PermissionStore {
	return brokenPermissions{s.Store.Permissions()}
}

type brokenPermissions struct{ store.PermissionStore }

func (p brokenPermissions) ForPrincipal(
	context.Context, string, []string,
) ([]types.Permission, error) {
	return nil, errStore
}
