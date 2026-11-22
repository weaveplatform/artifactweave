package nugethttp_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// These requests use independent database pools, as two registry nodes do.
// The first writer pauses after acquiring its transaction lock, whose lease
// timestamp is already expired. pg_blocking_pids proves the other writer is
// blocked by PostgreSQL itself, not by a mutex or lease polling in this process.
func TestAccNuGetWriteTransactionsOnPostgres(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	open := func() *appstoretest.SQLStore {
		t.Helper()
		db, err := appstoretest.OpenPostgres(
			context.Background(),
			dsn,
			appstoretest.PostgresOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db, other := open(), open()
	for _, mode := range []string{"commit", "rollback", "cancel owner", "cancel waiter", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			appstoretest.Reset(t, db.DB(), pgdrv.Dialect)
			f := newFixtureOn(t, db)
			f.items.Store = other
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			c, err := db.Repos().Get(ctx, "nuget-local")
			if err != nil {
				t.Fatal(err)
			}
			pkg := nupkg("Acme.Race", "1.0.0", "")
			// Authorize before the owner takes the root project guard. The
			// cancelled waiter must exercise the upload transaction itself.
			requestCtx, err := accesstest.Over(t, db).Authorize(ctx, kernel.Principal{Name: "admin", Admin: true}, true)
			if err != nil {
				t.Fatal(err)
			}
			source, err := db.Repos().Get(ctx, "libs-local")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "copy" || mode == "move" {
				// Fixture writes need the same root guard as the paused owner.
				if _, err := f.items.Deploy(ctx, item.Deploy{
					Target: item.Target{Repo: source, Path: "Acme.Race.1.0.0.nupkg"},
					Body:   bytes.NewReader(pkg),
				}); err != nil {
					t.Fatal(err)
				}
			}
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
			var writers sync.WaitGroup
			defer func() {
				cancel()
				select {
				case <-paused.resume:
				default:
					close(paused.resume)
				}
				writers.Wait()
			}()
			owner := *f.items
			owner.Store = paused
			ownerDone := make(chan error, 1)
			writers.Add(1)
			go func() {
				defer writers.Done()
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
			waitCtx, cancelWait := context.WithCancel(requestCtx)
			defer cancelWait()
			waitDone := make(chan error, 1)
			writers.Add(1)
			if mode == "copy" || mode == "move" {
				go func() {
					defer writers.Done()
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
					defer writers.Done()
					body, ct := pushForm(nupkg("acme.race", "1.0.0.0", ""))
					r := httptest.NewRequestWithContext(
						waitCtx,
						http.MethodPut,
						v2+"/native/",
						body,
					)
					r.Header.Set("Content-Type", ct)
					rec := httptest.NewRecorder()
					rec.Code = 0
					// This test owns the goroutine net/http would normally abort.
					aborted := func() (caught any) {
						defer func() { caught = recover() }()
						f.h.ServeHTTP(rec, r)
						return nil
					}()
					want := http.StatusConflict
					if mode == "rollback" || mode == "cancel owner" {
						want = http.StatusCreated
					}
					var err error
					switch {
					case mode == "cancel waiter":
						if aborted != http.ErrAbortHandler { //nolint:errorlint // net/http requires this exact sentinel
							err = fmt.Errorf("cancelled push did not abort: %v", aborted)
						} else if rec.Code != 0 || rec.Body.Len() != 0 || len(rec.Header()) != 0 {
							err = fmt.Errorf("cancelled push published a response: %d %s", rec.Code, rec.Body)
						}
					case aborted != nil:
						err = fmt.Errorf("push panicked: %v", aborted)
					case rec.Code != want:
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

func existsAt(ctx context.Context, db pkgroute.Store, repo, path string) bool {
	_, err := db.Items().Get(ctx, repo, false, path)
	return err == nil
}

func waitForBlocked(
	t *testing.T,
	ctx context.Context,
	db *appstoretest.SQLStore,
	done <-chan error,
) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.DB().QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity a WHERE datname = current_database()
			AND query LIKE 'SELECT id FROM project_authority_guard%'
			AND cardinality(pg_blocking_pids(a.pid)) > 0
			AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid
			AND l.relation = 'project_authority_guard'::regclass))`).Scan(&blocked); err != nil {
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
	item.Store
	entered, resume chan struct{}
	fail            error
}

func (s pausedWrite) Items() port.ItemStore {
	return pausedItems{ItemStore: s.Store.Items(), pause: s}
}

type pausedItems struct {
	port.ItemStore
	pause pausedWrite
}

func (s pausedItems) Put(ctx context.Context, it model.Item) (model.Item, error) {
	if !it.Folder && strings.HasSuffix(it.Name, ".nupkg") {
		close(s.pause.entered)
		select {
		case <-ctx.Done():
			return model.Item{}, ctx.Err()
		case <-s.pause.resume:
		}
		if s.pause.fail != nil {
			return model.Item{}, s.pause.fail
		}
	}
	return s.ItemStore.Put(ctx, it) //nolint:wrapcheck // test pass-through
}
