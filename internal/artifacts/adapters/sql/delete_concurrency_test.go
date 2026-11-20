package sql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccDeleteAccountsForTheRowsItActuallyDeletes(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		db := newStore(t)
		if db.Dialect().Name != "postgres" {
			t.Skip("PostgreSQL READ COMMITTED row replacement")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "r", Class: repo.Local, PackageType: repo.Generic})); err != nil {
			t.Fatal(err)
		}
		old, err := db.Items().Put(ctx, model.Item{RepoKey: "r", Name: "file.txt", Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		old.Actual.SHA256 = strings.Repeat("a", 64)
		if _, err := db.Items().Put(ctx, old); err != nil {
			t.Fatal(err)
		}
		held := make(chan int, 1)
		release := make(chan struct{})
		writerDone := make(chan error, 1)
		go func() {
			writerDone <- db.Tx(ctx, func(ctx context.Context) error {
				var pid int
				if err := db.Q(ctx).QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return fmt.Errorf("writer pid: %w", err)
				}
				replacement := old
				replacement.Size = 9
				replacement.Actual.SHA256 = strings.Repeat("b", 64)
				if _, err := db.Items().Put(ctx, replacement); err != nil {
					return err
				}
				held <- pid
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		var holder int
		select {
		case holder = <-held:
		case err := <-writerDone:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		waiterStarted := make(chan int, 1)
		type deleted struct {
			result port.DeleteResult
			err    error
		}
		deleteDone := make(chan deleted, 1)
		go func() {
			var result port.DeleteResult
			err := db.Tx(ctx, func(ctx context.Context) error {
				var pid int
				if err := db.Q(ctx).QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return fmt.Errorf("deleter pid: %w", err)
				}
				waiterStarted <- pid
				var err error
				result, err = db.Items().Delete(ctx, "r", false, "")
				return err
			})
			deleteDone <- deleted{result, err}
		}()
		var waiter int
		select {
		case waiter = <-waiterStarted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if err := db.DB().QueryRowContext(ctx, "SELECT $1::integer = ANY(pg_blocking_pids($2::integer))", holder, waiter).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			select {
			case result := <-deleteDone:
				t.Fatalf("delete completed before writer: %+v", result)
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		close(release)
		if err := <-writerDone; err != nil {
			t.Fatal(err)
		}
		var result deleted
		select {
		case result = <-deleteDone:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.result.Files != 1 || result.result.Bytes != 9 || result.result.BlobRefs[strings.Repeat("b", 64)] != 1 || result.result.BlobRefs[strings.Repeat("a", 64)] != 0 {
			t.Fatalf("delete reported stale facts: %+v", result.result)
		}
		if len(result.result.Removed) != 1 || result.result.Removed[0].ID != old.ID || result.result.Removed[0].Size != 9 ||
			result.result.Removed[0].Actual.SHA256 != strings.Repeat("b", 64) {
			t.Fatalf("deleted event facts=%+v", result.result.Removed)
		}
	}, sqltest.WithPostgres())
}
