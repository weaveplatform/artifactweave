package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
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
