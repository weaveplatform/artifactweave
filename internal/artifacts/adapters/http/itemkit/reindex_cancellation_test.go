package itemkit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	chefhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef/http"
	puppethttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

type cancelledReindexTransaction struct {
	*inmem.Store
	cancel context.CancelFunc
	calls  int
}

func (s *cancelledReindexTransaction) Tx(context.Context, func(context.Context) error) error {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return fmt.Errorf("fixture transaction begin: %w", context.Canceled)
}

func TestUnitHandwrittenReindexCancellationClassification(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Chef, repo.Puppet} {
		for _, tc := range []struct {
			name   string
			cancel bool
		}{
			{"client cancellation", true},
			{"live request backend cancellation", false},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				st := &cancelledReindexTransaction{Store: inmem.New()}
				if tc.cancel {
					st.cancel = cancel
				}
				if err := st.Repos().Create(ctx, *repo.Defaults(&repo.Config{
					Key: "packages", Class: repo.Local, PackageType: kind,
				})); err != nil {
					t.Fatal(err)
				}
				kit := itemkit.New(itemkit.Deps{Store: st, Formats: ecosystem.For})
				router := chi.NewRouter()
				if kind == repo.Chef {
					chefhttp.ChefAPI{Kit: kit}.RegisterChefReindex(router)
				} else {
					puppethttp.PuppetAPI{Kit: kit}.RegisterPuppetMetadataRoutes(router, pkgroute.Options{})
				}
				var cause error
				ctx = accessapi.WithCaller(httpkit.WithCause(ctx, &cause),
					kernel.Principal{Name: "admin", Admin: true}, &accessapitest.Decider{Admin: true})
				r := httptest.NewRequestWithContext(ctx, http.MethodPost,
					"/artifactory/api/"+string(kind)+"/packages/reindex", nil)
				rec := httptest.NewRecorder()
				rec.Code = 0
				aborted := func() (caught any) {
					defer func() { caught = recover() }()
					router.ServeHTTP(rec, r)
					return nil
				}()
				if st.calls != 1 {
					t.Fatal("reindex did not reach its transaction boundary", st.calls)
				}
				if tc.cancel {
					if aborted != http.ErrAbortHandler { //nolint:errorlint // net/http requires this exact sentinel
						t.Fatal("cancelled reindex did not abort", aborted)
					}
					if rec.Code != 0 || rec.Body.Len() != 0 || !errors.Is(cause, context.Canceled) {
						t.Fatal("cancelled reindex published a response or lost its cause", rec.Code, cause)
					}
				} else if aborted != nil || rec.Code != http.StatusInternalServerError {
					t.Fatal("live reindex backend failure changed", rec.Code, aborted)
				}
			})
		}
	}
}
