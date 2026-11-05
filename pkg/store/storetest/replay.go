package storetest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

func replayDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func replayRequest() store.ReplayRequest {
	return store.ReplayRequest{
		Scope: replayDigest("principal+operation+resource"), Key: replayDigest("random-key"),
		Fingerprint: replayDigest("request"), Now: base, Expires: at(60),
	}
}

func runReplays(t *testing.T, factory Factory) {
	t.Run("Replays", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := replayRequest()
		var calls atomic.Int64
		effect := func(ctx context.Context) (int64, error) {
			calls.Add(1)
			if err := s.Settings().Put(ctx, "replay-effect", true); err != nil {
				return 0, err
			}
			return 42, nil
		}
		const workers = 12
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for range workers {
			wg.Go(func() {
				out, err := s.Replays().Do(ctx, r, effect)
				if err == nil &&
					(out.ResourceID != 42 || !out.Expires.Equal(store.Normalise(r.Expires))) {
					err = fmt.Errorf("unexpected replay receipt: %+v", out)
				}
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			must(t, err)
		}
		if calls.Load() != 1 {
			t.Fatalf("committed effect called %d times", calls.Load())
		}
		out := get[store.ReplayReceipt](t)(s.Replays().Do(ctx, r, effect))
		if !out.Replayed {
			t.Fatal("duplicate did not replay")
		}
		changed := r
		changed.Fingerprint = replayDigest("different body")
		_, err := s.Replays().Do(ctx, changed, effect)
		wantErr(t, err, store.ErrReplayMismatch)
		changed.Scope = replayDigest("another credential")
		get[store.ReplayReceipt](t)(s.Replays().Do(ctx, changed, effect))
		changed = r
		changed.Now, changed.Expires = r.Expires, at(120)
		out = get[store.ReplayReceipt](t)(s.Replays().Do(ctx, changed, effect))
		if out.Replayed || calls.Load() != 3 {
			t.Fatalf("expired key did not perform new effect: %+v, calls %d", out, calls.Load())
		}
		if n := get[int64](t)(s.Replays().Prune(ctx, at(180), 1)); n != 1 {
			t.Fatalf("bounded prune removed %d", n)
		}
		if n := get[int64](t)(s.Replays().Prune(ctx, at(180), 100)); n != 1 {
			t.Fatalf("remaining prune removed %d", n)
		}
		for _, limit := range []int{0, store.MaxLimit + 1} {
			_, err := s.Replays().Prune(ctx, base, limit)
			wantErr(t, err, store.ErrInvalid)
		}
		for _, bad := range []store.ReplayRequest{
			{},
			{Scope: r.Scope, Key: "bad", Fingerprint: r.Fingerprint, Now: r.Now, Expires: r.Expires},
			{Scope: r.Scope, Key: r.Key, Fingerprint: r.Fingerprint, Now: r.Now, Expires: r.Now},
		} {
			_, err := s.Replays().Do(ctx, bad, effect)
			wantErr(t, err, store.ErrInvalid)
		}
		_, err = s.Replays().Do(ctx, r, nil)
		wantErr(t, err, store.ErrInvalid)
		_, err = s.Replays().Do(ctx, r, func(context.Context) (int64, error) { return 0, nil })
		wantErr(t, err, store.ErrInvalid)
	})
	t.Run("ReplayRollback", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		r := replayRequest()
		write := func(ctx context.Context) (int64, error) {
			return 1, s.Settings().Put(ctx, "replay-rollback", true)
		}
		_, err := s.Replays().Do(ctx, r, func(ctx context.Context) (int64, error) {
			if _, err := write(ctx); err != nil {
				return 0, err
			}
			return 0, store.ErrConflict
		})
		wantErr(t, err, store.ErrConflict)
		var value bool
		wantErr(t, s.Settings().Get(ctx, "replay-rollback", &value), store.ErrNotFound)
		err = s.Tx(ctx, func(ctx context.Context) error {
			if _, err := s.Replays().Do(ctx, r, write); err != nil {
				return err
			}
			return store.ErrConflict
		})
		wantErr(t, err, store.ErrConflict)
		wantErr(t, s.Settings().Get(ctx, "replay-rollback", &value), store.ErrNotFound)
		cancelCtx, cancel := context.WithCancel(ctx)
		_, err = s.Replays().Do(cancelCtx, r, func(ctx context.Context) (int64, error) {
			id, err := write(ctx)
			cancel()
			return id, err
		})
		cancel()
		wantErr(t, err, context.Canceled)
		wantErr(t, s.Settings().Get(ctx, "replay-rollback", &value), store.ErrNotFound)
		out := get[store.ReplayReceipt](t)(s.Replays().Do(ctx, r, write))
		if out.Replayed {
			t.Fatal("rolled-back receipt survived")
		}
	})
	t.Run("ReplayInProgressCancellation", func(t *testing.T) {
		s := factory(t)
		r := replayRequest()
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			_, err := s.Replays().Do(t.Context(), r, func(context.Context) (int64, error) {
				close(entered)
				<-release
				return 1, nil
			})
			done <- err
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := s.Replays().Do(ctx, r, func(context.Context) (int64, error) {
			t.Error("in-progress duplicate ran its effect")
			return 2, nil
		})
		close(release)
		must(t, <-done)
		wantErr(t, err, context.DeadlineExceeded)
	})
}
