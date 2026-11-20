package login_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/login"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestUnitLockEventsCommitWithAccount(t *testing.T) { testLockEvents(t, inmem.New()) }

func TestAccLockEventsCommitWithAccount(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			var db *appstoretest.SQLStore
			var err error
			if engine == "sqlite" {
				db, err = appstoretest.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "events.db"), appstoretest.SQLiteOptions{})
			} else {
				db, err = appstoretest.OpenPostgres(t.Context(), sqltest.PostgresDSN(t), appstoretest.PostgresOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			if engine == "postgres" {
				appstoretest.Reset(t, db.DB(), db.Dialect())
			}
			testLockEvents(t, db)
		})
	}
}

func testLockEvents(t *testing.T, db login.Store) {
	t.Helper()
	ctx := t.Context()
	origin, err := instance.EnsureID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Events().RegisterConsumer(ctx, origin, "proof"); err != nil {
		t.Fatal(err)
	}
	read := func() []outbox.Event {
		t.Helper()
		events, _, err := db.Events().Next(ctx, origin, "proof", 100)
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	for _, user := range []model.User{{Name: "alice"}, {Name: "disabled", Status: model.UserDisabled}, {Name: "rollback"}, {Name: "origin-error"}, {Name: "invalid-origin"}} {
		if err := db.Users().Create(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	service := login.Failures{Store: db, Now: func() time.Time { return now }}
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		wg.Go(func() { failures <- service.RecordFailedLogin(ctx, "ALICE", 2) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	events := read()
	if len(events) != 1 || events[0].Topic != api.UserLocked || events[0].At != now {
		t.Fatalf("lock events=%+v", events)
	}
	var facts api.UserLockedEvent
	if err := json.Unmarshal(events[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.User != "alice" || !facts.Actor.System || facts.Version != 1 || facts.Outcome != "committed" {
		t.Fatalf("facts=%+v", facts)
	}
	user, err := db.Users().Get(ctx, "alice")
	if err != nil || user.Status != model.UserLocked || user.FailedLogins != 12 {
		t.Fatalf("account=%+v err=%v", user, err)
	}
	if err := service.RecordFailedLogin(ctx, "disabled", 1); err != nil {
		t.Fatal(err)
	}
	if len(read()) != 1 {
		t.Fatal("disabled user emitted a lock event")
	}
	marker := errors.New("append failed")
	service.Store = failingStore{Store: db, err: marker}
	if err := service.RecordFailedLogin(ctx, "rollback", 1); !errors.Is(err, marker) {
		t.Fatalf("append failure=%v", err)
	}
	user, err = db.Users().Get(ctx, "rollback")
	if err != nil || user.FailedLogins != 0 || user.Status != "" || len(read()) != 1 {
		t.Fatalf("failed append retained account mutation: %+v err=%v", user, err)
	}
	service.Store = db
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if err := service.RecordFailedLogin(ctx, "rollback", 1); err != nil {
			return err
		}
		return marker
	}); !errors.Is(err, marker) {
		t.Fatal(err)
	}
	if len(read()) != 1 {
		t.Fatal("outer rollback emitted an event")
	}
	if err := service.RecordFailedLogin(ctx, "rollback", 1); err != nil {
		t.Fatal(err)
	}
	events = read()
	if len(events) != 2 || events[1].Seq != 2 {
		t.Fatalf("retry=%+v", events)
	}
	for _, check := range []struct {
		name, origin string
		err, want    error
	}{
		{name: "origin-error", err: marker, want: marker},
		{name: "invalid-origin", want: kernel.ErrInvalid},
	} {
		service.Store = originFailureStore{Store: db, origin: check.origin, err: check.err}
		if err := service.RecordFailedLogin(ctx, check.name, 1); !errors.Is(err, check.want) {
			t.Fatalf("%s error=%v", check.name, err)
		}
		account, err := db.Users().Get(ctx, check.name)
		if err != nil || account.FailedLogins != 0 || account.Status != "" || len(read()) != 2 {
			t.Fatalf("%s retained failed mutation: %+v err=%v", check.name, account, err)
		}
	}
	service.Store = db
	if err := service.RecordFailedLogin(ctx, "missing", 1); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing user=%v", err)
	}
}

type failingStore struct {
	login.Store
	err error
}

func (s failingStore) Events() outbox.EventStore {
	return failingEvents{EventStore: s.Store.Events(), err: s.err}
}

type failingEvents struct {
	outbox.EventStore
	err error
}

func (s failingEvents) AppendEvent(ctx context.Context, e outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, e); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}

type originFailureStore struct {
	login.Store
	origin string
	err    error
}

func (s originFailureStore) Instance() instance.InstanceStore {
	return originFailure{InstanceStore: s.Store.Instance(), origin: s.origin, err: s.err}
}

type originFailure struct {
	instance.InstanceStore
	origin string
	err    error
}

func (s originFailure) ID(context.Context) (string, error) { return s.origin, s.err }
