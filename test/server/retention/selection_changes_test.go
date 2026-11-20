package retention_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type selectionReadHook struct {
	artifactsapi.PackageIndex
	read func()
}

func (h *selectionReadHook) Files(ctx context.Context, v artifactsapi.IndexVersion, properties map[string][]string, p kernel.Page) ([]artifactsapi.IndexFile, string, error) {
	rows, next, err := h.PackageIndex.Files(ctx, v, properties, p)
	if err == nil && h.read != nil {
		h.read()
	}
	return rows, next, err
}

func selectionChangesContract(t *testing.T, newStore func(*testing.T) app.RouterStore) {
	t.Helper()
	setup := func(t *testing.T) (*selectionWorld, artifactsapi.IndexVersion) {
		t.Helper()
		w := newSelectionWorld(t, newStore(t))
		w.repository("alpha-main", "alpha", repo.NPM)
		v := w.version("alpha-main", repo.NPM, "@scope/widget", "1.0.0", []string{npm.CanonicalPath("@scope/widget", "1.0.0"), "readme.txt"})
		w.version("alpha-main", repo.NPM, "@scope/widget", "2.0.0", []string{npm.CanonicalPath("@scope/widget", "2.0.0")})
		p := selectionInput(repo.NPM)
		if _, err := w.service.Create(t.Context(), p.Key, p); err != nil {
			t.Fatal(err)
		}
		return w, v
	}
	t.Run("frozen rule and clock", func(t *testing.T) {
		w, v := setup(t)
		selection, err := w.service.Select(t.Context(), "selection-policy")
		if err != nil {
			t.Fatal(err)
		}
		original := selection.Snapshot()
		copy := selection.Snapshot()
		copy.Policy.Criteria.IncludedProperties["release"][0] = "tampered"
		copy.Policy.Criteria.Repos[0] = "foreign"
		p := selectionInput(repo.NPM)
		p.Criteria.ExcludedPackageVersions = []string{v.Version}
		if _, err := w.service.Update(t.Context(), p.Key, p); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(365 * 24 * time.Hour)
		page, err := selection.Candidates(t.Context(), v, kernel.Page{Limit: 1})
		if err != nil || page.Reason != cleanup.ReasonSelected || len(page.Items) != 1 || !page.At.Equal(original.At) || page.PolicyRevision != original.Policy.Revision {
			t.Fatal("an admitted rule/clock was changed by policy edit or caller mutation", page, err)
		}
		fresh, err := w.service.Select(t.Context(), p.Key)
		if err != nil {
			t.Fatal(err)
		}
		page, err = fresh.Candidates(t.Context(), v, kernel.Page{Limit: 1})
		if err != nil || page.Reason != cleanup.ReasonVersionExcluded || len(page.Items) != 0 {
			t.Fatal("new selection ignored the changed policy", page, err)
		}
	})
	for _, change := range []string{"download", "overwrite", "delete", "property", "revoke"} {
		t.Run("during evaluation/"+change, func(t *testing.T) {
			w, v := setup(t)
			original := w.service.Authorize
			read, changed := false, false
			w.service.PackageIndex = &selectionReadHook{PackageIndex: w.service.PackageIndex, read: func() { read = true }}
			w.service.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
				// This call starts after the first lead transaction committed.
				if read && !changed {
					changed = true
					lead := w.items[0]
					var err error
					switch change {
					case "download":
						err = w.db.Items().RecordDownload(ctx, lead.ID, port.Download{At: w.now, By: "reader"})
					case "overwrite":
						lead.Size, lead.Actual.SHA256, lead.Modified = 99, "replacement-content", w.now
						_, err = w.db.Items().Put(ctx, lead)
					case "delete":
						_, err = w.db.Items().Delete(ctx, lead.RepoKey, false, lead.FullPath())
					case "property":
						err = w.db.Items().PatchProperties(ctx, lead.ID, artifactmodel.Properties{"hold": {"true"}}, nil, w.now)
					case "revoke":
						w.authority = "none"
					}
					if err != nil {
						return nil, err
					}
				}
				return original(ctx)
			}
			selection, err := w.service.Select(t.Context(), "selection-policy")
			if err != nil {
				t.Fatal(err)
			}
			page, err := selection.Candidates(t.Context(), v, kernel.Page{Limit: 1})
			if change == "revoke" {
				if !errors.Is(err, kernel.ErrForbidden) || len(page.Items) != 0 {
					t.Fatal("revoked authority disclosed a candidate page", page, err)
				}
				return
			}
			if err != nil || page.Reason != cleanup.ReasonChanged || len(page.Items) != 0 {
				t.Fatal("changed lead evidence produced candidates", page, err)
			}
		})
	}
	t.Run("between pages", func(t *testing.T) {
		w, v := setup(t)
		selection, err := w.service.Select(t.Context(), "selection-policy")
		if err != nil {
			t.Fatal(err)
		}
		first, err := selection.Candidates(t.Context(), v, kernel.Page{Limit: 1})
		if err != nil || first.Next == "" || len(first.Items) != 1 {
			t.Fatal("missing first bounded page", first, err)
		}
		if err := w.db.Items().RecordDownload(t.Context(), w.items[0].ID, port.Download{At: w.now, By: "reader"}); err != nil {
			t.Fatal(err)
		}
		second, err := selection.Candidates(t.Context(), v, kernel.Page{Limit: 1, After: first.Next})
		if err != nil || second.Reason != cleanup.ReasonDownloaded || len(second.Items) != 0 {
			t.Fatal("download between pages remained eligible", second, err)
		}
	})
	for _, change := range []string{"owner", "environment", "delete", "revoke"} {
		t.Run("current repository/"+change, func(t *testing.T) {
			w, v := setup(t)
			selection, err := w.service.Select(t.Context(), "selection-policy")
			if err != nil {
				t.Fatal(err)
			}
			c, err := w.db.Repos().Get(t.Context(), "alpha-main")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "owner":
				c.ProjectKey = "beta"
				err = w.db.Repos().Update(t.Context(), c)
			case "environment":
				c.Environments = []string{"PROD"}
				err = w.db.Repos().Update(t.Context(), c)
			case "delete":
				err = w.db.Repos().Delete(t.Context(), c.Key)
			case "revoke":
				w.authority = "none"
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := selection.Candidates(t.Context(), v, kernel.Page{Limit: 1})
			if !errors.Is(err, kernel.ErrForbidden) || len(page.Items) != 0 {
				t.Fatal("stale repository scope disclosed candidates", page, err)
			}
		})
	}
}

func selectionCredentialsContract(t *testing.T, newStore func(*testing.T) app.RouterStore) {
	t.Helper()
	for _, change := range []string{"unchanged", "membership removed", "disabled user", "content token ceiling", "revoked token"} {
		t.Run("current credential/"+change, func(t *testing.T) {
			w := newSelectionWorld(t, newStore(t))
			ctx := t.Context()
			support.Seed(t, w.db)
			w.repository("alpha-main", "alpha", repo.NPM)
			v := w.version("alpha-main", repo.NPM, "@scope/widget", "1.0.0", []string{npm.CanonicalPath("@scope/widget", "1.0.0")})
			w.version("alpha-main", repo.NPM, "@scope/widget", "2.0.0", []string{npm.CanonicalPath("@scope/widget", "2.0.0")})
			p := selectionInput(repo.NPM)
			if _, err := w.service.Create(ctx, p.Key, p); err != nil {
				t.Fatal(err)
			}
			if err := w.db.Users().Create(ctx, accesstest.User{Name: "selector", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.db.Projects().PutRole(ctx, accesstest.Role{ProjectKey: "alpha", Name: "cleanup-admin", Type: "ADMIN"}); err != nil {
				t.Fatal(err)
			}
			if err := w.db.Projects().
				PutMember(ctx, accesstest.ProjectMember{ProjectKey: "alpha", Type: accesstest.PrincipalUser, Name: "selector", Roles: []string{"cleanup-admin"}}); err != nil {
				t.Fatal(err)
			}
			access := accesstest.Over(t, w.db, accesstest.WithTokens(), accesstest.WithProjectRepositories(repos.NewProjectRepositoryReader(w.db))).API()
			principal, err := access.Authenticator.Basic(ctx, "selector", "pw")
			if err != nil {
				t.Fatal(err)
			}
			var issued accessapi.IssuedToken
			if change == "content token ceiling" || change == "revoked token" {
				scope := "applied-permissions/user"
				if change == "content token ceiling" {
					scope = "artifact:alpha-main:r"
				}
				parsed, err := kernel.ParseScope(scope)
				if err != nil {
					t.Fatal(err)
				}
				issued, err = access.Tokens.Issue(ctx, accessapi.TokenRequest{Username: "selector", Scope: parsed, ByAdmin: true, ForceRevocable: true})
				if err != nil {
					t.Fatal(err)
				}
				principal, err = access.Authenticator.Token(ctx, issued.AccessToken, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			m, err := retention.New(retention.Deps{Store: w.db, Access: access, Repositories: w.service.Repositories, PackageIndex: w.service.PackageIndex})
			if err != nil {
				t.Fatal(err)
			}
			service := m.Policies()
			service.Now = func() time.Time { return w.now }
			ctx = kernel.WithPrincipal(ctx, principal)
			selection, err := service.Select(ctx, p.Key)
			if change == "content token ceiling" {
				if !errors.Is(err, kernel.ErrForbidden) || selection != nil {
					t.Fatal("a content-only token borrowed project administration", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "membership removed":
				err = w.db.Projects().DeleteMember(ctx, "alpha", accesstest.PrincipalUser, "selector")
			case "disabled user":
				var user accesstest.User
				user, err = w.db.Users().Get(ctx, "selector")
				if err == nil {
					user.Status = accesstest.UserDisabled
					err = w.db.Users().Update(ctx, user)
				}
			case "revoked token":
				err = access.Tokens.RevokeValue(ctx, issued.AccessToken)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := selection.Candidates(ctx, v, kernel.Page{Limit: 1})
			want := error(nil)
			switch change {
			case "membership removed":
				want = kernel.ErrForbidden
			case "disabled user", "revoked token":
				want = kernel.ErrBadCredentials
			}
			if !errors.Is(err, want) || want != nil && len(page.Items) != 0 || want == nil && (page.Reason != cleanup.ReasonSelected || len(page.Items) != 1) {
				t.Fatal("selection did not refresh the actual current credential/role", page, err)
			}
		})
	}
}
