package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func runBowerRegistrations(t *testing.T, newStore Factory) {
	t.Run("BowerRegistrations", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		for _, key := range []string{"bower", "other"} {
			storekit.Must(
				t,
				s.Repos().
					Create(ctx, repo.Config{Key: key, Class: repo.Remote, PackageType: repo.Bower, URL: "https://git.example"}),
			)
		}
		rows := []model.BowerRegistration{
			{RepoKey: "bower", Name: "alpha", URL: "ssh://git@git.example/team/alpha.git"},
			{RepoKey: "bower", Name: "beta", URL: "https://git.example/team/beta.git"},
		}
		for _, v := range rows {
			storekit.Must(t, s.BowerRegistrations().Create(ctx, v))
		}
		storekit.Same(
			t,
			"stored locator",
			rows[0],
			storekit.Get[model.BowerRegistration](
				t,
			)(
				s.BowerRegistrations().Get(ctx, "bower", "alpha"),
			),
		)
		storekit.WantErr(t, s.BowerRegistrations().Create(ctx, rows[0]), kernel.ErrConflict)
		storekit.WantErr(
			t,
			s.BowerRegistrations().Create(ctx, model.BowerRegistration{}),
			kernel.ErrInvalid,
		)
		storekit.WantErr(
			t,
			s.BowerRegistrations().
				Create(ctx, model.BowerRegistration{RepoKey: "missing", Name: "a", URL: "https://example/a.git"}),
			kernel.ErrNotFound,
		)
		_, err := s.BowerRegistrations().Get(ctx, "other", "alpha")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Same(
			t,
			"ordered paged registrations",
			rows,
			storekit.Pages(t, 1, func(p kernel.Page) ([]model.BowerRegistration, string, error) {
				return s.BowerRegistrations().List(ctx, "bower", p)
			}),
		)
		_, _, err = s.BowerRegistrations().List(ctx, "", kernel.Page{})
		storekit.WantErr(t, err, kernel.ErrInvalid)
		_, _, err = s.BowerRegistrations().List(ctx, "bower", kernel.Page{After: "not-base64!!"})
		if err == nil {
			t.Fatal("invalid cursor accepted")
		}
		rollback := errors.New("rollback") //nolint:err113 // the test matches it by identity
		err = s.Tx(ctx, func(ctx context.Context) error {
			if err := s.BowerRegistrations().
				Create(ctx, model.BowerRegistration{RepoKey: "bower", Name: "rollback", URL: "https://example/rollback.git"}); err != nil {
				return err //nolint:wrapcheck // the transaction returns the store's own error
			}
			return rollback
		})
		storekit.WantErr(t, err, rollback)
		_, err = s.BowerRegistrations().Get(ctx, "bower", "rollback")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		var wg sync.WaitGroup
		results := make(chan error, 4)
		for range 4 {
			wg.Go(func() {
				results <- s.BowerRegistrations().Create(ctx, model.BowerRegistration{RepoKey: "other", Name: "same", URL: "https://example/same.git"})
			})
		}
		wg.Wait()
		close(results)
		created := 0
		for err := range results {
			if err == nil {
				created++
			} else {
				storekit.WantErr(t, err, kernel.ErrConflict)
			}
		}
		if created != 1 {
			t.Fatalf("concurrent registrations: %d successes", created)
		}
		storekit.Must(t, s.Repos().Delete(ctx, "bower"))
		storekit.Must(
			t,
			s.Repos().
				Create(ctx, repo.Config{Key: "bower", Class: repo.Remote, PackageType: repo.Bower, URL: "https://git.example"}),
		)
		_, err = s.BowerRegistrations().Get(ctx, "bower", "alpha")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		_, err = s.BowerRegistrations().Get(ctx, "other", "same")
		storekit.Must(t, err)
	})
}
