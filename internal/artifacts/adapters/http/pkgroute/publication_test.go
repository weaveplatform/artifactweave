package pkgroute_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitProtocolTransactionRefreshesAuthorityBeforeEffects(t *testing.T) {
	boom := errors.New("publication authority unavailable")
	for _, tc := range []struct {
		name   string
		fault  error
		nil    bool
		abort  bool
		status int
	}{
		{name: "current policy", status: http.StatusNoContent},
		{name: "missing policy", nil: true, status: http.StatusForbidden},
		{name: "failed authority", fault: boom, status: http.StatusInternalServerError},
		{name: "failed mutation rolls back", abort: true, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKitFixture(t, nil)
			checked, entered := false, false
			o := f.o
			current := kernel.Principal{Name: "reader", Groups: []string{"current"}}
			d := &accessapitest.Decider{GroupNames: current.Groups}
			o.CurrentAccess = func(ctx context.Context) (kernel.Principal, api.Decider, error) {
				checked = true
				if !f.db.InTx(ctx) || entered {
					t.Fatal("authority must be refreshed at transaction entry")
				}
				if tc.nil {
					return current, nil, nil
				}
				return current, d, tc.fault
			}
			f.h = f.router(t, o, pkgroute.Protocol{
				Name: "testfmt", Types: []repo.PackageType{repo.Generic},
				Routes: func(r chi.Router, k *pkgroute.Kit) {
					r.Post("/publish", func(w http.ResponseWriter, r *http.Request) {
						err := k.Tx(r.Context(), func(ctx context.Context) error {
							entered = true
							p, ok := kernel.FromContext(ctx)
							got, _ := api.DeciderFrom(ctx)
							if !checked || !ok || p.Name != current.Name || p.Admin || got != d {
								t.Fatal("mutation retained the request's old caller or decider")
							}
							if err := f.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "published", Class: repo.Local})); err != nil {
								return err
							}
							if tc.abort {
								return boom
							}
							return nil
						})
						if err != nil {
							k.Error(w, r, err)
							return
						}
						w.WriteHeader(http.StatusNoContent)
					})
				},
			})
			w := f.do(http.MethodPost, "/artifactory/api/testfmt/"+local+"/publish", "reader", "")
			if !checked || w.Code != tc.status {
				t.Fatalf("checked=%v status=%d body=%s", checked, w.Code, w.Body)
			}
			_, err := f.db.Repos().Get(t.Context(), "published")
			if tc.status == http.StatusNoContent && err != nil || tc.status != http.StatusNoContent && !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("publication transaction did not commit/roll back correctly: %v", err)
			}
		})
	}
}
