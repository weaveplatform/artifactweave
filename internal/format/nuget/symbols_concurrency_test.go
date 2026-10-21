package nuget_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Both moves pause before flushing work. Each transaction must lock its
// work rows in sorted order, even though it visits destination then source.
func TestSymbolsOppositeMovesOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	sqltest.Reset(t, db.DB(), postgres.Dialect)
	w := newWorldOn(t, db, nil)
	other := w.repo("nuget-other", repo.Local, nil)
	w.deploy("A.1.0.0.snupkg", pdbPackage("A", zipFile{"lib/app.pdb", string(portablePDB())}), nil)
	if _, err := w.tryDeploy(
		other,
		"B.1.0.0.snupkg",
		pdbPackage("B", zipFile{"lib/app.pdb", string(portablePDB())}),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	resume := make(chan struct{})
	done := make(chan error, 2)
	for _, move := range []struct {
		from, to repo.Config
		name     string
	}{{w.c, other, "A"}, {other, w.c, "B"}} {
		svc := *w.items
		svc.Work = nil
		svc.Now = func() time.Time { return time.Unix(100, 0) }
		svc.Store = enqueuePauseStore{
			Store: db,
			pause: &enqueuePause{entered: entered, resume: resume},
		}
		go func() {
			_, err := svc.Transfer(
				ctx,
				item.Transfer{
					From: item.Target{Repo: move.from, Path: move.name + ".1.0.0.snupkg"},
					To:   item.Target{Repo: move.to, Path: move.name + ".1.0.0.snupkg"},
					Move: true,
					By:   "tester",
				},
			)
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("move before barrier: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(resume)
	for range 2 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	for _, c := range []repo.Config{w.c, other} {
		if err := w.q.EnsureFresh(ctx, nuget.KindSymbols, c.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Items().
			Get(ctx, c.Key, false, ".symbols/app.pdb/"+symbolGUID+"FFFFFFFF/app.pdb"); err != nil {
			t.Fatal(err)
		}
	}
}

type enqueuePause struct {
	last    string
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}
type enqueuePauseStore struct {
	store.Store
	pause *enqueuePause
}

func (s enqueuePauseStore) WorkQueue() store.WorkQueue {
	return enqueuePauseQueue{WorkQueue: s.Store.WorkQueue(), pause: s.pause}
}

type enqueuePauseQueue struct {
	store.WorkQueue
	pause *enqueuePause
}

func (q enqueuePauseQueue) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	at time.Time,
) error {
	q.pause.once.Do(func() {
		q.pause.entered <- struct{}{}
		select {
		case <-q.pause.resume:
		case <-ctx.Done():
		}
	})
	if q.pause.last > key {
		return fmt.Errorf("work locked out of order: %s before %s", q.pause.last, key)
	}
	q.pause.last = key
	if err := q.WorkQueue.Enqueue(ctx, kind, key, payload, at); err != nil {
		return err
	}
	return nil
}
