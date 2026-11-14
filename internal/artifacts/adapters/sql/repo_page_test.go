package sql_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccRepoPagesContract runs the repository window's contract on SQLite
// and PostgreSQL.
func TestAccRepoPagesContract(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			artstoretest.RunRepoPages(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

// TestAccRepoPagesAtScale reads the first and a late window of 5000
// repositories on SQLite and PostgreSQL, as an administrator sees them and
// as a caller who sees one class and a few keys does, and keeps each
// statement's plan. Get Repositories pages by offset, which the decision
// accepts for a collection bounded by configuration: a late window still
// steps over the rows before it, so the timings are logged, not bounded.
func TestAccRepoPagesAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			repoPagesAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

const repoScale = 5000

func repoPagesAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range repoScale {
			c := repo.Config{Key: fmt.Sprintf("repo-%05d", i), Class: repo.Local, PackageType: repo.Generic}
			if i%2 == 1 {
				c.Class, c.PackageType = repo.Remote, repo.Maven
			}
			if err := s.Repos().Create(ctx, c); err != nil {
				return fmt.Errorf("seed %s: %w", c.Key, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatal(err)
	}
	partial := []port.RepoSet{{
		Classes: []repo.Class{repo.Local},
		Keys:    []string{"repo-04991", "repo-04993", "repo-04995"},
	}}
	for _, c := range []struct {
		who     string
		visible []port.RepoSet
		total   int
		late    int
		first   string
		lastKey string
	}{
		{"administrator", nil, repoScale, repoScale - 11, "repo-04989", "repo-04998"},
		// 2500 locals and three remotes; the last eleven start at
		// repo-04984, and the remotes fall inside the window.
		{"partial reader", partial, repoScale/2 + 3, repoScale/2 + 3 - 11, "repo-04984", "repo-04996"},
	} {
		for _, offset := range []int{0, c.late} {
			l := port.RepoListing{
				Visible: c.visible, PackageTypes: []repo.PackageType{repo.Generic, repo.Maven},
				Offset: offset, Limit: 10, Count: true,
			}
			start := time.Now()
			page, err := s.Repos().Page(ctx, l)
			if err != nil || len(page.Repos) != 10 || !page.More || page.Total != c.total {
				t.Fatalf("%s at %d = %d repositories, more %v, total %d, %v",
					c.who, offset, len(page.Repos), page.More, page.Total, err)
			}
			if offset > 0 && (page.Repos[0].Key != c.first || page.Repos[9].Key != c.lastKey) {
				t.Fatalf("%s late window %s..%s", c.who, page.Repos[0].Key, page.Repos[9].Key)
			}
			t.Logf("%s: %s, 10 of %d repositories at %d: %s",
				s.Dialect().Name, c.who, c.total, offset, time.Since(start))
			if offset == 0 {
				continue
			}
			count, window, countArgs, windowArgs := artsql.RepoPageQueries(l)
			t.Logf("%s: %s count plan:\n%s", s.Dialect().Name, c.who,
				explain(t, s, count, countArgs...))
			t.Logf("%s: %s late window plan:\n%s", s.Dialect().Name, c.who,
				explain(t, s, window, windowArgs...))
		}
	}
}
