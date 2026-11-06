package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func runBowerRegistrations(t *testing.T, newStore Factory) {
	t.Run("BowerRegistrations", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		for _, key := range []string{"bower", "other"} {
			must(
				t,
				s.Repos().
					Create(ctx, repo.Config{Key: key, Class: repo.Remote, PackageType: repo.Bower, URL: "https://git.example"}),
			)
		}
		rows := []types.BowerRegistration{
			{RepoKey: "bower", Name: "alpha", URL: "ssh://git@git.example/team/alpha.git"},
			{RepoKey: "bower", Name: "beta", URL: "https://git.example/team/beta.git"},
		}
		for _, v := range rows {
			must(t, s.BowerRegistrations().Create(ctx, v))
		}
		same(
			t,
			"stored locator",
			rows[0],
			get[types.BowerRegistration](t)(s.BowerRegistrations().Get(ctx, "bower", "alpha")),
		)
		wantErr(t, s.BowerRegistrations().Create(ctx, rows[0]), store.ErrConflict)
		wantErr(t, s.BowerRegistrations().Create(ctx, types.BowerRegistration{}), store.ErrInvalid)
		wantErr(
			t,
			s.BowerRegistrations().
				Create(ctx, types.BowerRegistration{RepoKey: "missing", Name: "a", URL: "https://example/a.git"}),
			store.ErrNotFound,
		)
		_, err := s.BowerRegistrations().Get(ctx, "other", "alpha")
		wantErr(t, err, store.ErrNotFound)
		same(
			t,
			"ordered paged registrations",
			rows,
			pages(t, 1, func(p store.Page) ([]types.BowerRegistration, string, error) {
				return s.BowerRegistrations().List(ctx, "bower", p)
			}),
		)
		_, _, err = s.BowerRegistrations().List(ctx, "", store.Page{})
		wantErr(t, err, store.ErrInvalid)
		_, _, err = s.BowerRegistrations().List(ctx, "bower", store.Page{After: "not-base64!!"})
		if err == nil {
			t.Fatal("invalid cursor accepted")
		}
		rollback := errors.New("rollback")
		err = s.Tx(ctx, func(ctx context.Context) error {
			if err := s.BowerRegistrations().
				Create(ctx, types.BowerRegistration{RepoKey: "bower", Name: "rollback", URL: "https://example/rollback.git"}); err != nil {
				return err
			}
			return rollback
		})
		wantErr(t, err, rollback)
		_, err = s.BowerRegistrations().Get(ctx, "bower", "rollback")
		wantErr(t, err, store.ErrNotFound)
		var wg sync.WaitGroup
		results := make(chan error, 4)
		for range 4 {
			wg.Go(func() {
				results <- s.BowerRegistrations().Create(ctx, types.BowerRegistration{RepoKey: "other", Name: "same", URL: "https://example/same.git"})
			})
		}
		wg.Wait()
		close(results)
		created := 0
		for err := range results {
			if err == nil {
				created++
			} else {
				wantErr(t, err, store.ErrConflict)
			}
		}
		if created != 1 {
			t.Fatalf("concurrent registrations: %d successes", created)
		}
		must(t, s.Repos().Delete(ctx, "bower"))
		must(
			t,
			s.Repos().
				Create(ctx, repo.Config{Key: "bower", Class: repo.Remote, PackageType: repo.Bower, URL: "https://git.example"}),
		)
		_, err = s.BowerRegistrations().Get(ctx, "bower", "alpha")
		wantErr(t, err, store.ErrNotFound)
		_, err = s.BowerRegistrations().Get(ctx, "other", "same")
		must(t, err)
	})
}
