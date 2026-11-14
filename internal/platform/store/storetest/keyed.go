package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// errReload is a reload that refuses the committed resource.
var errReload = errors.New("reload refused")

// keyedReplay is the shared replay over s, as the router builds it over its
// store.
func keyedReplay(s Store) idempotency.Replay {
	return idempotency.Replay{Store: s.Replays(), Tx: s}
}

// keyedEffect writes the setting name through its context, counts its runs
// in calls and reports resource 42; its reload counts its own runs.
func keyedEffect(s Store, name string, calls, reloads *atomic.Int64) idempotency.Effect {
	return idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			calls.Add(1)
			return 42, s.Settings().Put(ctx, name, true)
		},
		Reload: func(context.Context, idempotency.ReplayReceipt) error {
			reloads.Add(1)
			return nil
		},
	}
}

func wantSetting(t *testing.T, s Store, name string, want bool) {
	t.Helper()
	var value bool
	err := s.Settings().Get(t.Context(), name, &value)
	if want {
		storekit.Must(t, err)
		return
	}
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

// runKeyedReplays is the contract of the replay keyed operations share
// (idempotency.Replay) over a store's receipts and unit of work: concurrent
// identical requests, a changed fingerprint, expiry, pruning, rollback,
// nested transactions and cancellation.
func runKeyedReplays(t *testing.T, factory func(*testing.T) Store) {
	t.Run("KeyedReplays", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := replayRequest()
		var calls, reloads atomic.Int64
		e := keyedEffect(s, "keyed-effect", &calls, &reloads)
		const workers = 12
		var wg sync.WaitGroup
		var replayed atomic.Int64
		errs := make(chan error, workers)
		for range workers {
			wg.Go(func() {
				out, err := keyedReplay(s).Do(ctx, r, e)
				if err == nil && out.ResourceID != 42 {
					err = fmt.Errorf("%w: %+v", errReceipt, out)
				}
				if out.Replayed {
					replayed.Add(1)
				}
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			storekit.Must(t, err)
		}
		// One effect, and every request, the replays too, confirmed by the
		// reload.
		if calls.Load() != 1 || reloads.Load() != workers || replayed.Load() != workers-1 {
			t.Fatalf(
				"effects %d, reloads %d, replays %d",
				calls.Load(),
				reloads.Load(),
				replayed.Load(),
			)
		}
		wantSetting(t, s, "keyed-effect", true)
		changed := r
		changed.Fingerprint = replayDigest("different body")
		_, err := keyedReplay(s).Do(ctx, changed, e)
		storekit.WantErr(t, err, idempotency.ErrReplayMismatch)
		if calls.Load() != 1 {
			t.Fatal("a changed fingerprint ran the effect")
		}
		// At expiry the key performs a new effect.
		expired := r
		expired.Now, expired.Expires = r.Expires, storekit.At(120)
		out := storekit.Get[idempotency.ReplayReceipt](t)(keyedReplay(s).Do(ctx, expired, e))
		if out.Replayed || calls.Load() != 2 {
			t.Fatalf("expired key: %+v, %d effects", out, calls.Load())
		}
	})
	t.Run("KeyedReplayPrune", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		ok := func(context.Context) (int64, error) { return 1, nil }
		// One more expired receipt than a keyed request removes.
		for i := range idempotency.PruneLimit + 1 {
			r := replayRequest()
			r.Key = replayDigest(fmt.Sprint("prune-", i))
			storekit.Get[idempotency.ReplayReceipt](t)(s.Replays().Do(ctx, r, ok))
		}
		var calls, reloads atomic.Int64
		later := replayRequest()
		later.Key = replayDigest("later")
		later.Now, later.Expires = storekit.At(120), storekit.At(180)
		storekit.Get[idempotency.ReplayReceipt](t)(
			keyedReplay(s).Do(ctx, later, keyedEffect(s, "prune-effect", &calls, &reloads)),
		)
		if n := storekit.Get[int64](t)(s.Replays().Prune(ctx, later.Now, kernel.MaxLimit)); n != 1 {
			t.Fatalf("a keyed request left %d expired receipts, want 1", n)
		}
	})
	t.Run("KeyedReplayRollback", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := replayRequest()
		var calls, reloads atomic.Int64
		e := keyedEffect(s, "keyed-rollback", &calls, &reloads)
		failing := e
		failing.Run = func(ctx context.Context) (int64, error) {
			if _, err := e.Run(ctx); err != nil {
				return 0, fmt.Errorf("keyed effect: %w", err)
			}
			return 0, kernel.ErrConflict
		}
		_, err := keyedReplay(s).Do(ctx, r, failing)
		storekit.WantErr(t, err, kernel.ErrConflict)
		wantSetting(t, s, "keyed-rollback", false)
		// A reload that refuses the first run's resource rolls back the
		// effect and its receipt.
		refused := e
		refused.Reload = func(context.Context, idempotency.ReplayReceipt) error { return errReload }
		_, err = keyedReplay(s).Do(ctx, r, refused)
		storekit.WantErr(t, err, errReload)
		wantSetting(t, s, "keyed-rollback", false)
		out := storekit.Get[idempotency.ReplayReceipt](t)(keyedReplay(s).Do(ctx, r, e))
		if out.Replayed {
			t.Fatal("a rolled-back receipt survived")
		}
		// A reload that refuses a replay keeps the committed receipt.
		_, err = keyedReplay(s).Do(ctx, r, refused)
		storekit.WantErr(t, err, errReload)
		out = storekit.Get[idempotency.ReplayReceipt](t)(keyedReplay(s).Do(ctx, r, e))
		if !out.Replayed || calls.Load() != 3 {
			t.Fatalf("refused replay: %+v, %d effects", out, calls.Load())
		}
	})
	t.Run("KeyedReplayNested", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := replayRequest()
		var calls, reloads atomic.Int64
		e := keyedEffect(s, "keyed-nested", &calls, &reloads)
		// Inside a caller's transaction the replay joins it: the caller's
		// rollback takes the claim, the effect and the receipt with it.
		err := s.Tx(ctx, func(ctx context.Context) error {
			if _, err := keyedReplay(s).Do(ctx, r, e); err != nil {
				return err //nolint:wrapcheck // WantErr reports any error but ErrConflict
			}
			return kernel.ErrConflict
		})
		storekit.WantErr(t, err, kernel.ErrConflict)
		wantSetting(t, s, "keyed-nested", false)
		storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
			_, err := keyedReplay(s).Do(ctx, r, e)
			return err //nolint:wrapcheck // Must reports it
		}))
		wantSetting(t, s, "keyed-nested", true)
		out := storekit.Get[idempotency.ReplayReceipt](t)(keyedReplay(s).Do(ctx, r, e))
		if !out.Replayed || calls.Load() != 2 {
			t.Fatalf("committed nested receipt: %+v, %d effects", out, calls.Load())
		}
	})
	t.Run("KeyedReplayCancellation", func(t *testing.T) {
		s := factory(t)
		r := replayRequest()
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			_, err := keyedReplay(s).Do(t.Context(), r, idempotency.Effect{
				Run: func(ctx context.Context) (int64, error) {
					close(entered)
					<-release
					return 1, s.Settings().Put(ctx, "keyed-original", true)
				},
				Reload: func(context.Context, idempotency.ReplayReceipt) error { return nil },
			})
			done <- err
		}()
		<-entered
		duplicate := idempotency.Effect{
			Run: func(context.Context) (int64, error) {
				t.Error("an in-progress duplicate ran its effect")
				return 2, nil
			},
			Reload: func(context.Context, idempotency.ReplayReceipt) error { return nil },
		}
		// A duplicate waits for the original until its request's deadline,
		// then until the replay's own budget.
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := keyedReplay(s).Do(ctx, r, duplicate)
		storekit.WantErr(t, err, context.DeadlineExceeded)
		budgeted := keyedReplay(s)
		budgeted.Budget = 50 * time.Millisecond
		_, err = budgeted.Do(t.Context(), r, duplicate)
		storekit.WantErr(t, err, context.DeadlineExceeded)
		close(release)
		storekit.Must(t, <-done)
		out := storekit.Get[idempotency.ReplayReceipt](
			t,
		)(
			keyedReplay(s).Do(t.Context(), r, duplicate),
		)
		if !out.Replayed || out.ResourceID != 1 {
			t.Fatalf("after the original committed: %+v", out)
		}
	})
}
