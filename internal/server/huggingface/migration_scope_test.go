package huggingface_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type migrationTokenVerifier struct{ token types.Token }

func (v migrationTokenVerifier) Verify(context.Context, string) (types.Token, error) {
	return v.token, nil
}

type migrationReadCounter struct {
	blob.Store
	opens atomic.Int64
}

func (b *migrationReadCounter) Open(
	ctx context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	b.opens.Add(1)
	return b.Store.Open(ctx, digest)
}

func migrationScopeFixture(t *testing.T) (*fixture, *migrationReadCounter, http.Handler) {
	t.Helper()
	db := inmem.New()
	storage, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs := &migrationReadCounter{Store: storage}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	if err = db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})); err != nil {
		t.Fatal(err)
	}
	if err = db.Users().Create(t.Context(), types.User{Name: "admin", Admin: true}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, items: items}
	f.legacyRevision("models", "1700000000", sha, false)
	grants := accesstest.Over(t, db).API().Grants
	router := chi.NewRouter()
	pkgroute.Mount(router, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error { return err },
	}, huggingface.Protocol(huggingface.Options{Grants: grants}))
	return f, blobs, router
}

func migrationWithScope(
	t *testing.T,
	f *fixture,
	router http.Handler,
	scope string,
	dry bool,
) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := types.ParseScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	kit := accesstest.Over(t, f.db, accesstest.WithTokenVerifier(migrationTokenVerifier{
		token: types.Token{ID: "scoped", Subject: "jfac@fixture/users/admin", Scope: parsed},
	}))
	principal, err := kit.API().Authenticator.Token(t.Context(), "scoped", "")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/huggingfaceml/hf/"+migrationEndpoint,
		bytes.NewReader(migrationBody("models", "1700000000", sha, "main", dry)),
	)
	r.Header.Set("Content-Type", "application/json")
	ctx, err := kit.Authorize(r.Context(), principal, false)
	if err != nil {
		t.Fatal(err)
	}
	r = r.WithContext(ctx)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, r)
	return out
}

func TestAccLegacyMigrationHonorsAdministratorTokenScopes(t *testing.T) {
	const sourceRead = "artifact:hf/models/acme/demo/main/1700000000/**:r"
	const destinationReadWrite = "artifact:hf/models--acme--demo/**:r,w"
	const admin = "applied-permissions/user "
	const allowed = admin + sourceRead + " " + destinationReadWrite
	for _, phase := range []string{"plan", "apply", "completed plan", "completed apply"} {
		t.Run(phase, func(t *testing.T) {
			for _, tc := range []struct {
				name, scope string
				want        int
			}{
				{"destination only", admin + destinationReadWrite, http.StatusForbidden},
				{"source unreadable", admin + "artifact:hf/models/**:w " + destinationReadWrite, http.StatusForbidden},
				{"metadata unreadable", admin + "artifact:hf/models/acme/demo/main/1700000000/config.json:r artifact:hf/models/acme/demo/main/1700000000/sub/**:r " + destinationReadWrite, http.StatusForbidden},
				{"source file unreadable", admin + "artifact:hf/models/acme/demo/main/1700000000/.jfrog_huggingface_model_info.json:r " + destinationReadWrite, http.StatusForbidden},
				{"destination unreadable", admin + sourceRead + " artifact:hf/models--acme--demo/**:w", http.StatusForbidden},
				{"destination unwritable", admin + sourceRead + " artifact:hf/models--acme--demo/**:r", http.StatusForbidden},
				{"destination file unwritable", admin + sourceRead + " artifact:hf/models--acme--demo/**:r artifact:hf/models--acme--demo/snapshots/**/config.json:w artifact:hf/models--acme--demo/refs/main:w", http.StatusForbidden},
				{"ref unreadable", admin + sourceRead + " artifact:hf/models--acme--demo/snapshots/**:r,w artifact:hf/models--acme--demo/refs/main:w", http.StatusForbidden},
				{"ref unwritable", admin + sourceRead + " artifact:hf/models--acme--demo/snapshots/**:r,w artifact:hf/models--acme--demo/refs/main:r", http.StatusForbidden},
				{"not administrative", sourceRead + " " + destinationReadWrite, http.StatusForbidden},
				{"reviewed source and destination", allowed, http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f, blobs, router := migrationScopeFixture(t)
					if phase == "completed plan" || phase == "completed apply" {
						out := migrationWithScope(t, f, router, allowed, false)
						if out.Code != http.StatusOK {
							t.Fatalf("initial migration: %d %s", out.Code, out.Body.String())
						}
					}
					before, _, err := f.db.Items().
						Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
					if err != nil {
						t.Fatal(err)
					}
					blobs.opens.Store(0)
					out := migrationWithScope(
						t,
						f,
						router,
						tc.scope,
						phase == "plan" || phase == "completed plan",
					)
					if out.Code != tc.want {
						t.Fatalf(
							"scoped migration: %d, want %d: %s",
							out.Code,
							tc.want,
							out.Body.String(),
						)
					}
					if tc.want == http.StatusForbidden {
						if blobs.opens.Load() != 0 {
							t.Fatalf("denied migration opened %d blobs", blobs.opens.Load())
						}
						after, _, err := f.db.Items().
							Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
						if err != nil || !slices.Equal(before, after) {
							t.Fatalf("denied migration changed destination: %v", err)
						}
					}
				})
			}
		})
	}
}
