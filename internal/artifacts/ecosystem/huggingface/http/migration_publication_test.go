package huggingfacehttp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	huggingfacehttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type migrationPublicationEnd func()

func (end migrationPublicationEnd) Read([]byte) (int, error) {
	end()
	return 0, io.EOF
}

func TestAccLegacyMigrationRechecksAdministratorDespiteRetainedContentGrants(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "revoked"}[revoked], func(t *testing.T) {
			f, _, _ := migrationScopeFixture(t)
			txState, ok := f.db.(interface{ InTx(context.Context) bool })
			if !ok {
				t.Fatal("migration fixture does not expose transaction state")
			}
			access := accesstest.Over(t, f.db).API()
			if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
				Name: "retained-content", Resources: map[accessapi.ResourceType]accesstest.Resource{
					accessapi.ResourceArtifact: {
						Targets: map[string]accesstest.Target{accessapi.TargetAnyLocal: {}},
						Actions: accesstest.Grants{
							Users: map[string]accessapi.Actions{"admin": accessapi.ActionRead | accessapi.ActionWrite | accessapi.ActionDelete | accessapi.ActionAnnotate},
						},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			initial := kernel.Principal{Name: "admin", Admin: true}
			decider, err := access.Authorizer.For(t.Context(), initial)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			current := false
			pkgroute.Mount(router, pkgroute.Options{
				Store: f.db, Items: func() *item.Service { return f.items },
				ItemError: func(_ context.Context, _ string, err error) error { return err },
				CurrentAccess: func(ctx context.Context) (kernel.Principal, accessapi.Decider, error) {
					if !txState.InTx(ctx) {
						t.Fatal("migration administrator check is outside publication")
					}
					current = true
					u, err := f.db.Users().Get(ctx, "admin")
					if err != nil {
						return kernel.Principal{}, nil, err
					}
					p := kernel.Principal{Name: u.Name, Admin: u.Admin}
					d, err := access.Authorizer.For(ctx, p)
					return p, d, err
				},
			}, huggingfacehttp.Protocol(huggingfacehttp.Options{Grants: access.Grants}))
			ended := false
			body := io.MultiReader(bytes.NewReader(migrationBody("models", "1700000000", sha, "main", false)), migrationPublicationEnd(func() {
				if txState.InTx(t.Context()) {
					t.Fatal("migration JSON streaming holds a transaction")
				}
				ended = true
				if revoked {
					u, err := f.db.Users().Get(t.Context(), "admin")
					if err != nil {
						t.Fatal(err)
					}
					u.Admin = false
					if err := f.db.Users().Update(t.Context(), u); err != nil {
						t.Fatal(err)
					}
				}
			}))
			ctx := accessapi.WithCaller(t.Context(), initial, decider)
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/artifactory/api/huggingfaceml/hf/"+migrationEndpoint, body)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			want := http.StatusOK
			if revoked {
				want = http.StatusForbidden
			}
			if !ended || !current || w.Code != want {
				t.Fatalf("ended=%t current=%t migration=%d %s, want %d", ended, current, w.Code, w.Body.String(), want)
			}
			_, err = f.db.Items().Get(t.Context(), "hf", false, "models--acme--demo/snapshots/"+sha+"/config.json")
			if revoked && !errors.Is(err, kernel.ErrNotFound) || !revoked && err != nil {
				t.Fatal("migration publication escaped current administrator authority", err)
			}
		})
	}
}
