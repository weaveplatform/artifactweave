package nuget_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// These requests use independent database pools, as two registry nodes do.
// The first writer pauses after acquiring its transaction lock, whose lease
// timestamp is already expired. pg_blocking_pids proves the other writer is
// blocked by PostgreSQL itself, not by a mutex or lease polling in this process.
func TestAccNuGetWriteTransactionsOnPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	open := func() *sqlcommon.Store {
		t.Helper()
		db, err := postgres.Open(context.Background(), dsn, postgres.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db, other := open(), open()
	for _, mode := range []string{"commit", "rollback", "cancel owner", "cancel waiter", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			sqltest.Reset(t, db.DB(), postgres.Dialect)
			f := newFixtureOn(t, db)
			f.items.Store = other
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			c, err := db.Repos().Get(ctx, "nuget-local")
			if err != nil {
				t.Fatal(err)
			}
			pkg := nupkg("Acme.Race", "1.0.0", "")
			ownerCtx, cancelOwner := context.WithCancel(ctx)
			defer cancelOwner()
			paused := pausedWrite{
				Store:   db,
				entered: make(chan struct{}),
				resume:  make(chan struct{}),
			}
			if mode == "rollback" {
				paused.fail = errBroken
			}
			owner := *f.items
			owner.Store = paused
			ownerDone := make(chan error, 1)
			go func() {
				_, err := owner.Deploy(ownerCtx, item.Deploy{
					Target: item.Target{Repo: c, Path: "rest/Acme.Race.1.0.0.nupkg"},
					Body:   bytes.NewReader(pkg), By: "admin",
				})
				ownerDone <- err
			}()
			select {
			case <-paused.entered:
			case err := <-ownerDone:
				t.Fatalf("owner did not reach write: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Always unblock our writer even if an assertion fails.
			defer func() {
				select {
				case <-paused.resume:
				default:
					close(paused.resume)
				}
			}()
			waitCtx, cancelWait := context.WithCancel(ctx)
			defer cancelWait()
			waitDone := make(chan error, 1)
			if mode == "copy" || mode == "move" {
				source, err := db.Repos().Get(ctx, "libs-local")
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.items.Deploy(ctx, item.Deploy{
					Target: item.Target{
						Repo: source,
						Path: "Acme.Race.1.0.0.nupkg",
					},
					Body: bytes.NewReader(pkg),
				})
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					res, err := f.items.Transfer(waitCtx, item.Transfer{
						From: item.Target{Repo: source, Path: "Acme.Race.1.0.0.nupkg"},
						To: item.Target{
							Repo: c,
							Path: "transfer/Acme.Race.1.0.0.nupkg",
						},
						Move: mode == "move",
					})
					if err == nil &&
						(res.Failed != 1 || res.Files != 0 || !strings.Contains(fmt.Sprint(res.Messages), "already contains")) {
						err = fmt.Errorf("transfer result: %+v", res)
					}
					waitDone <- err
				}()
			} else {
				go func() {
					body, ct := pushForm(nupkg("acme.race", "1.0.0.0", ""))
					r := httptest.NewRequestWithContext(
						waitCtx,
						http.MethodPut,
						v2+"/native/",
						body,
					)
					r.Header.Set("Content-Type", ct)
					r.Header.Set("X-Test-User", "admin")
					rec := httptest.NewRecorder()
					f.h.ServeHTTP(rec, r)
					want := http.StatusConflict
					if mode == "rollback" || mode == "cancel owner" {
						want = http.StatusCreated
					}
					if mode == "cancel waiter" {
						want = http.StatusInternalServerError
					}
					var err error
					if rec.Code != want {
						err = fmt.Errorf("push status %d, want %d: %s", rec.Code, want, rec.Body)
					}
					waitDone <- err
				}()
			}
			waitForBlocked(t, ctx, other, waitDone)
			if mode == "cancel waiter" {
				cancelWait()
				if err := <-waitDone; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancel owner" {
				cancelOwner()
			}
			close(paused.resume)
			ownerErr := <-ownerDone
			if mode == "rollback" || mode == "cancel owner" {
				if ownerErr == nil {
					t.Fatal("cancelled/failed owner committed")
				}
			} else if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			if mode != "cancel waiter" {
				if err := <-waitDone; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "move" && !existsAt(ctx, db, "libs-local", "Acme.Race.1.0.0.nupkg") {
				t.Fatal("refused move removed its source")
			}
			files := 0
			for _, p := range []string{"rest/Acme.Race.1.0.0.nupkg", "native/acme.race.1.0.0.nupkg", "transfer/Acme.Race.1.0.0.nupkg"} {
				if existsAt(ctx, db, c.Key, p) {
					files++
				}
			}
			if files != 1 {
				t.Fatalf("stored %d packages", files)
			}
			leases, err := db.Leases().List(ctx)
			if err != nil || len(leases) != 0 {
				t.Fatalf("leases left: %v, %v", leases, err)
			}
		})
	}
}

func existsAt(ctx context.Context, db store.Store, repo, path string) bool {
	_, err := db.Items().Get(ctx, repo, false, path)
	return err == nil
}

func waitForBlocked(t *testing.T, ctx context.Context, db *sqlcommon.Store, done <-chan error) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.DB().QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
			AND cardinality(pg_blocking_pids(pid)) > 0)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("writer escaped transaction lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}

type pausedWrite struct {
	store.Store
	entered, resume chan struct{}
	fail            error
}

func (s pausedWrite) Items() store.ItemStore {
	return pausedItems{ItemStore: s.Store.Items(), pause: s}
}

type pausedItems struct {
	store.ItemStore
	pause pausedWrite
}

func (s pausedItems) Put(ctx context.Context, it types.Item) (types.Item, error) {
	if !it.Folder && strings.HasSuffix(it.Name, ".nupkg") {
		close(s.pause.entered)
		select {
		case <-ctx.Done():
			return types.Item{}, ctx.Err()
		case <-s.pause.resume:
		}
		if s.pause.fail != nil {
			return types.Item{}, s.pause.fail
		}
	}
	return s.ItemStore.Put(ctx, it) //nolint:wrapcheck // test pass-through
}
