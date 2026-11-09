package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// buildRun is a run with a row in every build table.
func buildRun(number string) model.Build {
	return model.Build{
		Name: "app", Number: number, Started: time.Unix(100, 0),
		Properties: model.Properties{"k": {"v"}},
		Modules: []model.BuildModule{
			{
				ID:         "m",
				Properties: model.Properties{"mk": {"mv"}},
				Artifacts: []model.BuildFile{
					{Name: "a.jar", Checksums: kernel.Checksums{SHA1: "a1"}},
				},
				Dependencies: []model.BuildFile{
					{ID: "d", Scopes: []string{"compile"}, Checksums: kernel.Checksums{SHA1: "d1"}},
				},
			},
		},
	}
}

// buildsStore opens a SQLite store holding run 1 of buildRun with a
// promotion and a retention.
func buildsStore(t *testing.T) *artsql.Store {
	t.Helper()
	ctx := context.Background()
	s := mustOpenFile(t, filepath.Join(t.TempDir(), "aw.db"), nil)
	bs := s.Builds()
	run, err := bs.Put(ctx, buildRun("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.AddPromotion(
		ctx,
		model.BuildPromotion{BuildID: run.ID, Status: "s"},
	); err != nil {
		t.Fatal(err)
	}
	if err := bs.PutRetention(
		ctx,
		model.BuildRetention{Name: "app", KeepNumbers: []string{"1"}},
	); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAccBuildsFaults injects a failure into a statement after a successful
// one, or corrupts a stored document, and checks every build write and read
// reports it.
func TestAccBuildsFaults(t *testing.T) {
	bs := func(s port.Store) port.BuildStore { return s.Builds() }
	put := func(b model.Build) func(context.Context, port.Store) error {
		return func(ctx context.Context, s port.Store) error {
			_, err := bs(s).Put(ctx, b)
			return err
		}
	}
	replace := func(ctx context.Context, s port.Store) error {
		r := buildRun("1")
		r.ID = 1
		return bs(s).Replace(ctx, r)
	}
	byID := func(ctx context.Context, s port.Store) error {
		_, err := bs(s).ByID(ctx, 1)
		return err
	}
	// Module properties without build properties reach the module-level
	// property insert.
	moduleProps := model.Build{
		Name: "app", Number: "2", Started: time.Unix(1, 0),
		Modules: []model.BuildModule{{ID: "m", Properties: model.Properties{"k": {"v"}}}},
	}
	cases := []struct {
		name   string
		inject string
		op     func(context.Context, port.Store) error
	}{
		{"put property", sqltest.FailTrigger("INSERT", "build_properties"), put(buildRun("2"))},
		{
			"put module property",
			sqltest.FailTrigger("INSERT", "build_properties"),
			put(moduleProps),
		},
		{"put module", sqltest.FailTrigger("INSERT", "build_modules"), put(buildRun("2"))},
		{"put artifact", sqltest.FailTrigger("INSERT", "build_artifacts"), put(buildRun("2"))},
		{"put dependency", sqltest.FailTrigger("INSERT", "build_dependencies"), put(buildRun("2"))},
		{"replace run", sqltest.FailTrigger("UPDATE", "builds"), replace},
		{"replace clear", sqltest.FailTrigger("DELETE", "build_properties"), replace},
		{"replace content", sqltest.FailTrigger("INSERT", "build_modules"), replace},
		{"read properties", sqltest.DropTable("build_properties"), byID},
		{"read artifacts", sqltest.DropTable("build_artifacts"), byID},
		{"read dependencies", sqltest.DropTable("build_dependencies"), byID},
		{"corrupt scopes", "UPDATE build_dependencies SET scopes = 'not json'", byID},
		{
			"find corrupt scopes", "UPDATE build_dependencies SET scopes = 'not json'",
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).FindDependencies(ctx, kernel.Checksums{SHA1: "d1"})
				return err
			},
		},
		{
			"delete number",
			sqltest.FailTrigger("DELETE", "builds"),
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).DeleteNumbers(ctx, "", "app", []string{"1"})
				return err
			},
		},
		{
			"delete all runs",
			sqltest.FailTrigger("DELETE", "builds"),
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).DeleteAll(ctx, "", "app")
				return err
			},
		},
		{
			"delete all retention",
			sqltest.FailTrigger("DELETE", "build_retention"),
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).DeleteAll(ctx, "", "app")
				return err
			},
		},
		{
			"rename runs",
			sqltest.FailTrigger("UPDATE", "builds"),
			func(ctx context.Context, s port.Store) error {
				return bs(s).Rename(ctx, "", "app", "new")
			},
		},
		{
			"rename retention",
			sqltest.FailTrigger("UPDATE", "build_retention"),
			func(ctx context.Context, s port.Store) error {
				return bs(s).Rename(ctx, "", "app", "new")
			},
		},
		{
			"add promotion",
			sqltest.FailTrigger("INSERT", "build_promotions"),
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).AddPromotion(ctx, model.BuildPromotion{BuildID: 1})
				return err
			},
		},
		{
			"read promotions",
			sqltest.DropTable("build_promotions"),
			func(ctx context.Context, s port.Store) error {
				_, err := bs(s).Promotions(ctx, 1)
				return err
			},
		},
		{
			"corrupt retention", "UPDATE build_retention SET keep_numbers = 'not json'",
			func(ctx context.Context, s port.Store) error {
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
			if err := c.op(ctx, s); err == nil || errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccBuildsUnmigrated runs every build method against a database without
// the schema: each must surface the driver error, never a nil error or a
// misleading ErrNotFound.
func TestAccBuildsUnmigrated(t *testing.T) {
	ctx := context.Background()
	bs := rawFile(t, filepath.Join(t.TempDir(), "raw.db"), nil).Builds()
	errOf := func(_ any, err error) error { return err }
	errOf3 := func(_ any, _ string, err error) error { return err }
	sum := kernel.Checksums{SHA1: "a"}
	for name, err := range map[string]error{
		"put":               errOf(bs.Put(ctx, buildRun("1"))),
		"replace":           bs.Replace(ctx, model.Build{ID: 1}),
		"get":               errOf(bs.Get(ctx, "", "app", "1", time.Unix(1, 0))),
		"by id":             errOf(bs.ByID(ctx, 1)),
		"latest":            errOf(bs.Latest(ctx, "", "app")),
		"runs":              errOf3(bs.Runs(ctx, "", "app", kernel.Page{})),
		"names":             errOf3(bs.Names(ctx, "", kernel.Page{})),
		"delete numbers":    errOf(bs.DeleteNumbers(ctx, "", "app", []string{"1"})),
		"delete all":        errOf(bs.DeleteAll(ctx, "", "app")),
		"rename":            bs.Rename(ctx, "", "app", "new"),
		"add promotion":     errOf(bs.AddPromotion(ctx, model.BuildPromotion{BuildID: 1})),
		"promotions":        errOf(bs.Promotions(ctx, 1)),
		"find artifacts":    errOf(bs.FindArtifacts(ctx, sum)),
		"find dependencies": errOf(bs.FindDependencies(ctx, sum)),
		"put retention":     bs.PutRetention(ctx, model.BuildRetention{Name: "app"}),
		"retention":         errOf(bs.Retention(ctx, "", "app")),
	} {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
