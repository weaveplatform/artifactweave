package access_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactshttp "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type publicationSQLStore struct {
	*appsql.Store
	attempt func()
}

type publicationSQLEnd func()

func (end publicationSQLEnd) Read([]byte) (int, error) {
	end()
	return 0, io.EOF
}

func (s publicationSQLStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	if !s.InTx(ctx) {
		s.attempt()
	}
	if err := s.Store.Tx(ctx, fn); err != nil {
		return fmt.Errorf("publication transaction: %w", err)
	}
	return nil
}

func TestAccProjectPublicationWaitsForCurrentSQLAuthority(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store {
		return appsql.New(s.DB(), s.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		for _, change := range []string{"unchanged", "role revoked", "membership removed", "group removed", "repository moved"} {
			t.Run(change, func(t *testing.T) {
				db := newStore(t)
				support.Seed(t, db)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				for _, key := range []string{"alpha", "beta"} {
					if err := db.Projects().Create(ctx, accesstest.Project{Key: key}); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Groups().Create(ctx, accesstest.Group{Name: "publishers"}); err != nil {
					t.Fatal(err)
				}
				if err := db.Users().Create(ctx, accesstest.User{Name: "publisher", PasswordHash: support.CheapHash, Groups: []string{"publishers"}}); err != nil {
					t.Fatal(err)
				}
				role := accesstest.Role{
					Name:         "publisher",
					ProjectKey:   "alpha",
					Type:         accesstest.RoleCustom,
					Actions:      []string{"DEPLOY_CACHE_REPOSITORY"},
					Environments: []string{"DEV"},
				}
				if err := db.Projects().PutRole(ctx, role); err != nil {
					t.Fatal(err)
				}
				if err := db.Projects().
					PutMember(ctx, accesstest.ProjectMember{ProjectKey: "alpha", Type: accesstest.PrincipalGroup, Name: "publishers", Roles: []string{"publisher"}}); err != nil {
					t.Fatal(err)
				}
				c := *repo.Defaults(&repo.Config{Key: "project-local", Class: repo.Local, PackageType: repo.Generic, ProjectKey: "alpha"})
				if err := db.Repos().Create(ctx, c); err != nil {
					t.Fatal(err)
				}
				access := accesstest.Over(t, db, accesstest.WithProjectRepositories(repos.NewProjectRepositoryReader(db))).API()
				p, err := access.Authenticator.Basic(ctx, "publisher", "pw")
				if err != nil {
					t.Fatal(err)
				}
				p, old, err := access.Authorizer.Authorize(ctx, p, false)
				if err != nil {
					t.Fatal(err)
				}
				ctx = accessapi.WithCaller(ctx, p, old)
				attempt, ready, writer := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				var once sync.Once
				st := publicationSQLStore{Store: db, attempt: func() { once.Do(func() { close(attempt) }) }}
				blobs, err := fs.New(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				svc := artifactshttp.New(artifactshttp.Deps{
					Store: st,
					Deps:  itemkit.Deps{Blobs: blobs, Access: access, Private: artifactshttp.PrivateItem},
				})
				router := chi.NewRouter()
				svc.RegisterItemRoutes(router)
				// The writer starts only at body EOF, after request-side lookups.
				// It commits when publication attempts its outer transaction. This
				// works for SQLite's BEGIN lock and PostgreSQL's guard row alike,
				// without holding the publication transaction over client streaming.
				body := io.MultiReader(strings.NewReader("payload"), publicationSQLEnd(func() {
					if db.InTx(ctx) {
						t.Fatal("client streaming is inside publication transaction")
					}
					go func() {
						err := db.Tx(ctx, func(tx context.Context) error {
							if err := db.Projects().Guard(tx); err != nil {
								return err
							}
							switch change {
							case "role revoked":
								role.Actions = nil
								err = db.Projects().PutRole(tx, role)
							case "membership removed":
								err = db.Projects().DeleteMember(tx, "alpha", accesstest.PrincipalGroup, "publishers")
							case "group removed":
								var user accesstest.User
								user, err = db.Users().Get(tx, "publisher")
								if err == nil {
									user.Groups = nil
									err = db.Users().Update(tx, user)
								}
							case "repository moved":
								c.ProjectKey = "beta"
								err = db.Repos().Update(tx, c)
							}
							if err != nil {
								return err
							}
							close(ready)
							select {
							case <-attempt:
								return nil
							case <-tx.Done():
								return tx.Err()
							}
						})
						writer <- err
					}()
					select {
					case <-ready:
					case err := <-writer:
						t.Fatal("revocation writer failed", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}))
				r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/artifactory/project-local/nested/file", body)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if err := <-writer; err != nil {
					t.Fatal("revocation commit", err)
				}
				want := http.StatusForbidden
				if change == "unchanged" {
					want = http.StatusCreated
				}
				if w.Code != want {
					t.Fatalf("publication after blocked authority change: status=%d body=%s want=%d", w.Code, w.Body.String(), want)
				}
				for _, path := range []string{"nested", "nested/file"} {
					_, err := db.Items().Get(ctx, c.Key, false, path)
					if change == "unchanged" && err != nil || change != "unchanged" && !errors.Is(err, kernel.ErrNotFound) {
						t.Fatalf("publication metadata %s: %v", path, err)
					}
				}
			})
		}
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}
