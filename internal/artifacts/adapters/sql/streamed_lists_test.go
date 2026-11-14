package sql_test

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// streamedScale is how many items, and how many dependency rows, the plan
// tests seed: enough that a sort would show in the plan.
const streamedScale = 5000

// TestAccStreamedListsAtScale reads a late page of a deep file list, of a
// dependency search and of the trash can's tree on SQLite and PostgreSQL
// and keeps each query's plan: a seek on items_full_path,
// build_dependencies_sha1_order or trash_path, with no sort of every item,
// dependency or trash entry (#1409).
func TestAccStreamedListsAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			streamedListsAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

// TestAccStreamedListsClosedDatabase: the page reads report the driver's
// error on a closed pool.
func TestAccStreamedListsClosedDatabase(t *testing.T) {
	s := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _, byPath := s.Items().DescendantsByFullPath(ctx, "r", false, "a", kernel.Page{})
	_, _, deps := s.Builds().DependencyPage(ctx, kernel.Checksums{SHA1: "x"}, kernel.Page{})
	_, _, trash := s.Trash().BelowByPath(ctx, "r", kernel.Page{})
	sqltest.Fails(t, map[string]error{
		"Items.DescendantsByFullPath": byPath,
		"Builds.DependencyPage":       deps,
		"Trash.BelowByPath":           trash,
	})
}

func streamedListsAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	const sha1 = "0123456789012345678901234567890123456789"
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range streamedScale {
			for _, full := range []string{
				fmt.Sprintf("a/f%04d", i), fmt.Sprintf("a-%04d", i), fmt.Sprintf("b/g%04d", i),
			} {
				if _, err := s.Items().
					Put(ctx, model.Item{RepoKey: "big", Name: full}); err != nil {
					return fmt.Errorf("seed %s: %w", full, err)
				}
				if _, err := s.Trash().Add(ctx, model.TrashEntry{
					Record: model.TrashRecord{Item: model.Item{RepoKey: "big", Name: full}},
				}); err != nil {
					return fmt.Errorf("seed trash %s: %w", full, err)
				}
			}
		}
		for i := range streamedScale / 100 {
			deps := make([]model.BuildFile, 0, 100)
			for j := range 100 {
				deps = append(deps, model.BuildFile{
					ID: fmt.Sprintf("d%d", j), Checksums: kernel.Checksums{SHA1: sha1},
				})
			}
			if _, err := s.Builds().Put(ctx, model.Build{
				Name: "app", Number: fmt.Sprint(i), Started: time.Unix(int64(i), 0).UTC(),
				Modules: []model.BuildModule{{ID: "m", Dependencies: deps}},
			}); err != nil {
				return fmt.Errorf("seed build %d: %w", i, err)
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

	late := kernel.Page{After: kernel.Cursor("a/f4988"), Limit: 11}
	start := time.Now()
	got, next, err := s.Items().DescendantsByFullPath(ctx, "big", false, "a", late)
	if err != nil || len(got) != 11 || got[0].FullPath() != "a/f4989" ||
		got[10].FullPath() != "a/f4999" || next != "" {
		t.Fatalf("late page of a = %d items, next %q, %v", len(got), next, err)
	}
	t.Logf("%s: 11 of %d below a after a/f4988: %s", s.Dialect().Name, streamedScale,
		time.Since(start))
	for _, folder := range []string{"a", ""} {
		query, args, err := artsql.FullPathQuery(s, "big", false, folder, late)
		if err != nil {
			t.Fatal(err)
		}
		streamedPlan(t, s, "file list of "+folder, "items_full_path", query, args)
	}

	runs, _, err := s.Builds().Runs(ctx, "", "app", kernel.Page{Limit: 1})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	after := kernel.Page{Limit: 11, After: kernel.Cursor(fmt.Sprint(runs[0].ID), "0", "50")}
	refs, _, err := s.Builds().DependencyPage(ctx, kernel.Checksums{SHA1: sha1}, after)
	if err != nil || len(refs) != 11 || refs[0].File.ID != "d51" || refs[10].File.ID != "d61" {
		t.Fatalf("late dependency page = %v, %v", refs, err)
	}
	query, args, err := artsql.DependencyPageQuery(kernel.Checksums{SHA1: sha1}, after)
	if err != nil {
		t.Fatal(err)
	}
	streamedPlan(t, s, "dependency search", "build_dependencies_sha1_order", query, args)

	// The cursor's ID is past every entry at big/a/f4988.
	lateTrash := kernel.Page{
		After: kernel.Cursor("big/a/f4988", strconv.FormatInt(math.MaxInt64, 10)), Limit: 11,
	}
	start = time.Now()
	entries, next, err := s.Trash().BelowByPath(ctx, "big/a", lateTrash)
	if err != nil || len(entries) != 11 || entries[0].TrashPath() != "big/a/f4989" ||
		entries[10].TrashPath() != "big/a/f4999" || next != "" {
		t.Fatalf("late trash page of big/a = %d entries, next %q, %v", len(entries), next, err)
	}
	t.Logf("%s: 11 of %d trash entries below big/a after big/a/f4988: %s", s.Dialect().Name,
		streamedScale, time.Since(start))
	for _, dir := range []string{"big/a", ""} {
		query, args, err := artsql.BelowByPathQuery(s, dir, lateTrash)
		if err != nil {
			t.Fatal(err)
		}
		streamedPlan(t, s, "trash can tree below "+dir, "trash_path", query, args)
	}
}

// streamedPlan explains query and requires it to read index in order, with
// no sort: a sort would read every item below the folder or every
// dependency with the checksum for each page.
func streamedPlan(t *testing.T, s *artsql.Store, what, index, query string, args []any) {
	t.Helper()
	text := explain(t, s, query, args...)
	t.Logf("%s: late %s page plan:\n%s", s.Dialect().Name, what, text)
	if !strings.Contains(text, index) || strings.Contains(text, "TEMP B-TREE") ||
		strings.Contains(text, "Sort") {
		t.Fatalf("the late %s page does not seek %s in order:\n%s", what, index, text)
	}
}
