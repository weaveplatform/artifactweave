package sqlcommon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildRun is a run with a row in every build table.
func buildRun(number string) types.Build {
	return types.Build{
		Name: "app", Number: number, Started: time.Unix(100, 0),
		Properties: types.Properties{"k": {"v"}},
		Modules: []types.BuildModule{
			{
				ID:         "m",
				Properties: types.Properties{"mk": {"mv"}},
				Artifacts: []types.BuildFile{
					{Name: "a.jar", Checksums: types.Checksums{SHA1: "a1"}},
				},
				Dependencies: []types.BuildFile{
					{ID: "d", Scopes: []string{"compile"}, Checksums: types.Checksums{SHA1: "d1"}},
				},
			},
		},
	}
}

// buildsStore opens a SQLite store holding run 1 of buildRun with a
// promotion and a retention.
func buildsStore(t *testing.T) *sqlcommon.Store {
	t.Helper()
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bs := s.Builds()
	run, err := bs.Put(ctx, buildRun("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.AddPromotion(
		ctx,
		types.BuildPromotion{BuildID: run.ID, Status: "s"},
	); err != nil {
		t.Fatal(err)
	}
	if err := bs.PutRetention(
		ctx,
		types.BuildRetention{Name: "app", KeepNumbers: []string{"1"}},
	); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBuildsFaults injects a failure into a statement after a successful
// one, or corrupts a stored document, and checks every build write and read
// reports it.
func TestBuildsFaults(t *testing.T) {
	bs := func(s store.Store) store.BuildStore { return s.Builds() }
	put := func(b types.Build) func(context.Context, store.Store) error {
		return func(ctx context.Context, s store.Store) error {
			_, err := bs(s).Put(ctx, b)
			return err
		}
	}
	replace := func(ctx context.Context, s store.Store) error {
		r := buildRun("1")
		r.ID = 1
		return bs(s).Replace(ctx, r)
	}
	byID := func(ctx context.Context, s store.Store) error {
		_, err := bs(s).ByID(ctx, 1)
		return err
	}
	// Module properties without build properties reach the module-level
	// property insert.
	moduleProps := types.Build{
		Name: "app", Number: "2", Started: time.Unix(1, 0),
		Modules: []types.BuildModule{{ID: "m", Properties: types.Properties{"k": {"v"}}}},
	}
	cases := []struct {
		name   string
		inject string
		op     func(context.Context, store.Store) error
	}{
		{"put property", opsFail("INSERT", "build_properties"), put(buildRun("2"))},
		{"put module property", opsFail("INSERT", "build_properties"), put(moduleProps)},
		{"put module", opsFail("INSERT", "build_modules"), put(buildRun("2"))},
		{"put artifact", opsFail("INSERT", "build_artifacts"), put(buildRun("2"))},
		{"put dependency", opsFail("INSERT", "build_dependencies"), put(buildRun("2"))},
		{"replace run", opsFail("UPDATE", "builds"), replace},
		{"replace clear", opsFail("DELETE", "build_properties"), replace},
		{"replace content", opsFail("INSERT", "build_modules"), replace},
		{"read properties", opsDrop("build_properties"), byID},
		{"read artifacts", opsDrop("build_artifacts"), byID},
		{"read dependencies", opsDrop("build_dependencies"), byID},
		{"corrupt scopes", "UPDATE build_dependencies SET scopes = 'not json'", byID},
		{
			"find corrupt scopes", "UPDATE build_dependencies SET scopes = 'not json'",
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).FindDependencies(ctx, types.Checksums{SHA1: "d1"})
				return err
			},
		},
		{
			"delete number",
			opsFail("DELETE", "builds"),
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).DeleteNumbers(ctx, "", "app", []string{"1"})
				return err
			},
		},
		{
			"delete all runs",
			opsFail("DELETE", "builds"),
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).DeleteAll(ctx, "", "app")
				return err
			},
		},
		{
			"delete all retention",
			opsFail("DELETE", "build_retention"),
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).DeleteAll(ctx, "", "app")
				return err
			},
		},
		{
			"rename runs",
			opsFail("UPDATE", "builds"),
			func(ctx context.Context, s store.Store) error {
				return bs(s).Rename(ctx, "", "app", "new")
			},
		},
		{
			"rename retention",
			opsFail("UPDATE", "build_retention"),
			func(ctx context.Context, s store.Store) error {
				return bs(s).Rename(ctx, "", "app", "new")
			},
		},
		{
			"add promotion",
			opsFail("INSERT", "build_promotions"),
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).AddPromotion(ctx, types.BuildPromotion{BuildID: 1})
				return err
			},
		},
		{
			"read promotions",
			opsDrop("build_promotions"),
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).Promotions(ctx, 1)
				return err
			},
		},
		{
			"corrupt retention", "UPDATE build_retention SET keep_numbers = 'not json'",
			func(ctx context.Context, s store.Store) error {
				_, err := bs(s).Retention(ctx, "", "app")
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := buildsStore(t)
			if _, err := s.DB().ExecContext(ctx, c.inject); err != nil {
				t.Fatal(err)
			}
			if err := c.op(ctx, s); err == nil || errors.Is(err, store.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestBuildsUnmigrated runs every build method against a database without
// the schema: each must surface the driver error, never a nil error or a
// misleading ErrNotFound.
func TestBuildsUnmigrated(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "raw.db"),
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	bs := s.Builds()
	errOf := func(_ any, err error) error { return err }
	errOf3 := func(_ any, _ string, err error) error { return err }
	sum := types.Checksums{SHA1: "a"}
	for name, err := range map[string]error{
		"put":               errOf(bs.Put(ctx, buildRun("1"))),
		"replace":           bs.Replace(ctx, types.Build{ID: 1}),
		"get":               errOf(bs.Get(ctx, "", "app", "1", time.Unix(1, 0))),
		"by id":             errOf(bs.ByID(ctx, 1)),
		"latest":            errOf(bs.Latest(ctx, "", "app")),
		"runs":              errOf3(bs.Runs(ctx, "", "app", store.Page{})),
		"names":             errOf3(bs.Names(ctx, "", store.Page{})),
		"delete numbers":    errOf(bs.DeleteNumbers(ctx, "", "app", []string{"1"})),
		"delete all":        errOf(bs.DeleteAll(ctx, "", "app")),
		"rename":            bs.Rename(ctx, "", "app", "new"),
		"add promotion":     errOf(bs.AddPromotion(ctx, types.BuildPromotion{BuildID: 1})),
		"promotions":        errOf(bs.Promotions(ctx, 1)),
		"find artifacts":    errOf(bs.FindArtifacts(ctx, sum)),
		"find dependencies": errOf(bs.FindDependencies(ctx, sum)),
		"put retention":     bs.PutRetention(ctx, types.BuildRetention{Name: "app"}),
		"retention":         errOf(bs.Retention(ctx, "", "app")),
	} {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
