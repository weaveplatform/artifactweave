package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestContract(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := postgres.Open(context.Background(), dsn,
			postgres.Options{Options: sqlcommon.Options{Keyring: k}, MaxOpenConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		sqltest.Reset(t, s.DB(), postgres.Dialect)
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := postgres.Open(
		ctx,
		"",
		postgres.Options{},
	); !errors.Is(
		err,
		postgres.ErrDSNRequired,
	) {
		t.Fatalf("empty DSN: %v", err)
	}
	if _, err := postgres.Open(ctx, "postgres://%zz", postgres.Options{}); err == nil {
		t.Fatal("a malformed DSN parsed")
	}
	// Nothing listens on port 1, so the ping fails fast.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := postgres.Open(ctx, "postgres://aw@127.0.0.1:1/aw?connect_timeout=2",
		postgres.Options{ConnMaxLifetime: time.Minute}); err == nil {
		t.Fatal("connected to a closed port")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if !postgres.IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("23505 is a unique violation")
	}
	if postgres.IsUniqueViolation(&pgconn.PgError{Code: "23503"}) ||
		postgres.IsUniqueViolation(errors.New("x")) {
		t.Fatal("other errors are not")
	}
}

func TestKeyRotation(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	first := true
	sqltest.Rotation(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := postgres.Open(
			context.Background(),
			dsn,
			postgres.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			sqltest.Reset(t, s.DB(), postgres.Dialect)
			first = false
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// TestPropertyValuesMigration upgrades a database written at migration 0012,
// whose property keys hold the whole value, to 0013, and back again.
func TestPropertyValuesMigration(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqltest.Reset(t, s.DB(), postgres.Dialect)
	if _, err := sqlcommon.Rollback(ctx, s.DB(), postgres.Dialect, 12); err != nil {
		t.Fatal(err)
	}
	// The store reads and writes items.props_modified_at, which migration
	// 0014 adds. The 0012 schema borrows the column while the store seeds
	// it, and gives it back before the upgrade adds it again.
	lend := func(stmt string) {
		t.Helper()
		if _, err := s.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	lend("ALTER TABLE items ADD COLUMN props_modified_at BIGINT")

	short := types.Properties{"k": {"v1", "v2"}, "other": {"x"}}
	it, err := s.Items().Put(ctx, types.Item{RepoKey: "r", Name: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Items().SetProperties(ctx, it.ID, short, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	run, err := s.Builds().Put(ctx, types.Build{
		Name: "app", Number: "1", Started: time.Unix(1, 0).UTC(), Properties: short,
	})
	if err != nil {
		t.Fatal(err)
	}
	long := storetest.LongText(types.PropertyValueMax)
	err = s.Items().
		PatchProperties(ctx, it.ID, types.Properties{"long": {long}}, nil, time.Unix(2, 0))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "54000" {
		t.Fatalf(
			"migration 0012 stored a long value (%v); this test no longer shows the upgrade",
			err,
		)
	}
	lend("ALTER TABLE items DROP COLUMN props_modified_at")

	if _, err := sqlcommon.Migrate(ctx, s.DB(), postgres.Dialect); err != nil {
		t.Fatal(err)
	}
	props := func() types.Properties {
		t.Helper()
		p, err := s.Items().Properties(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if diff := cmp.Diff(short, props()); diff != "" {
		t.Fatalf("item properties after the upgrade (-want +got):\n%s", diff)
	}
	b, err := s.Builds().ByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(short, b.Properties); diff != "" {
		t.Fatalf("build properties after the upgrade (-want +got):\n%s", diff)
	}
	if err := s.Items().
		PatchProperties(ctx, it.ID, types.Properties{"long": {long}}, nil, time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}

	// Rolling back drops the rows the old keys cannot index and keeps the
	// rest.
	if _, err := sqlcommon.Rollback(ctx, s.DB(), postgres.Dialect, 12); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(short, props()); diff != "" {
		t.Fatalf("item properties after the rollback (-want +got):\n%s", diff)
	}
	if _, err := sqlcommon.Migrate(ctx, s.DB(), postgres.Dialect); err != nil {
		t.Fatal(err)
	}
}
