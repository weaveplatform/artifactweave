package postgres_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/postgres"
	appstoretest "github.com/weaveplatform/artifactweave/migration/app/storetest"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

func TestAccReplayProcessCrash(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	s, err := postgres.Open(t.Context(), dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
	appstoretest.ReplayCrash(t, s, []string{"ARTIFACTWEAVE_REPLAY_CRASH_POSTGRES=" + dsn})
}

func TestAccReplayCrashHelper(t *testing.T) {
	dsn := os.Getenv("ARTIFACTWEAVE_REPLAY_CRASH_POSTGRES")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	s, err := postgres.Open(
		t.Context(),
		dsn,
		postgres.Options{Options: appsql.Options{SkipMigrate: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	appstoretest.ReplayCrashChild(t, s)
}

func TestAccReplayPruneDoesNotDeleteRenewal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := postgres.Open(ctx, platsqltest.PostgresDSN(t), postgres.Options{MaxOpenConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
	now := time.Now().UTC()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("prune-race")))
	r := idempotency.ReplayRequest{
		Scope:       digest,
		Key:         digest,
		Fingerprint: digest,
		Now:         now,
		Expires:     now.Add(time.Minute),
	}
	if _, err := s.Replays().
		Do(ctx, r, func(context.Context) (int64, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	r.Now, r.Expires = now.Add(2*time.Minute), now.Add(3*time.Minute)
	entered, release := make(chan struct{}), make(chan struct{})
	renewed := make(chan error, 1)
	go func() {
		_, err := s.Replays().Do(ctx, r, func(ctx context.Context) (int64, error) {
			close(entered)
			select {
			case <-release:
				return 2, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
		renewed <- err
	}()
	<-entered
	pruned := make(chan error, 1)
	go func() {
		n, err := s.Replays().Prune(ctx, r.Now, 1)
		if err == nil && n != 0 {
			err = fmt.Errorf("prune deleted %d renewed receipts", n)
		}
		pruned <- err
	}()
	// Prove the DELETE selected the old expired row and is waiting for its
	// lock before allowing the renewal to commit. This catches READ COMMITTED
	// snapshot races without relying on a scheduling delay.
	blocked := false
	for ctx.Err() == nil {
		rows, err := s.DB().
			QueryContext(ctx, "SELECT query FROM pg_stat_activity WHERE wait_event_type = 'Lock'")
		if err != nil {
			break
		}
		for rows.Next() {
			var query string
			if err := rows.Scan(
				&query,
			); err == nil &&
				strings.HasPrefix(query, "DELETE FROM api_replays") {
				blocked = true
			}
		}
		_ = rows.Close()
		if blocked {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
		}
	}
	close(release)
	if err := <-renewed; err != nil {
		t.Fatal(err)
	}
	if err := <-pruned; err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("prune did not block on the renewing receipt")
	}
	out, err := s.Replays().Do(ctx, r, func(context.Context) (int64, error) {
		t.Error("pruning a renewed receipt repeated its effect")
		return 3, nil
	})
	if err != nil || !out.Replayed || out.ResourceID != 2 {
		t.Fatalf("renewed receipt: %+v, %v", out, err)
	}
}
