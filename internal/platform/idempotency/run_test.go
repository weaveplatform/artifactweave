package idempotency_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var errEffect = errors.New("effect failed")

func digestOf(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func request(now time.Time) idempotency.ReplayRequest {
	return idempotency.ReplayRequest{
		Scope: digestOf("scope"), Key: digestOf("key"), Fingerprint: digestOf("body"),
		Now: now, Expires: now.Add(time.Hour),
	}
}

func newReplay() (idempotency.Replay, *inmem.Store) {
	s := inmem.New(memtx.New())
	return idempotency.Replay{Store: s.Replays(), Tx: s}, s
}

func noReload(context.Context, idempotency.ReplayReceipt) error { return nil }

func TestUnitReplayDoRefusesBadRequests(t *testing.T) {
	p, _ := newReplay()
	run := func(context.Context) (int64, error) { return 1, nil }
	for _, c := range []struct {
		r idempotency.ReplayRequest
		e idempotency.Effect
	}{
		{idempotency.ReplayRequest{}, idempotency.Effect{Run: run, Reload: noReload}},
		{request(time.Now()), idempotency.Effect{Reload: noReload}},
		{request(time.Now()), idempotency.Effect{Run: run}},
	} {
		if _, err := p.Do(t.Context(), c.r, c.e); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("Do(%+v): %v", c.r, err)
		}
	}
}

// failingPrune is a receipt store whose prune fails, as a store that has
// lost its database does.
type failingPrune struct{ idempotency.ReplayStore }

func (failingPrune) Prune(context.Context, time.Time, int) (int64, error) { return 0, errEffect }

func TestUnitReplayDoPruneFailure(t *testing.T) {
	p, _ := newReplay()
	p.Store = failingPrune{p.Store}
	ran := false
	_, err := p.Do(t.Context(), request(time.Now()), idempotency.Effect{
		Run:    func(context.Context) (int64, error) { ran = true; return 1, nil },
		Reload: noReload,
	})
	if !errors.Is(err, errEffect) || ran {
		t.Fatalf("prune failure: %v, ran %v", err, ran)
	}
}

// The budget bounds the effect: it runs with a deadline the budget away,
// thirty seconds by default.
func TestUnitReplayDoBudget(t *testing.T) {
	p, _ := newReplay()
	var left time.Duration
	_, err := p.Do(t.Context(), request(time.Now()), idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return 0, errEffect
			}
			left = time.Until(deadline)
			return 1, nil
		},
		Reload: noReload,
	})
	if err != nil || left <= idempotency.Budget-time.Second || left > idempotency.Budget {
		t.Fatalf("default budget: %v left, %v", left, err)
	}
}

// An effect that maps the error its spent budget caused into one that no
// longer wraps it still leaves Do's error wrapping the deadline, which is
// what tells the caller to retry.
func TestUnitReplayDoDeadlineSurvivesMapping(t *testing.T) {
	p, s := newReplay()
	p.Budget = 10 * time.Millisecond
	_, err := p.Do(t.Context(), request(time.Now()), idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			if err := s.Settings().Put(ctx, "mapped", true); err != nil {
				return 0, err //nolint:wrapcheck // the test's own store
			}
			<-ctx.Done()
			return 0, errEffect
		},
		Reload: noReload,
	})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errEffect) {
		t.Fatalf("mapped deadline: %v", err)
	}
	var v bool
	if err := s.Settings().Get(t.Context(), "mapped", &v); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("timed-out effect survived: %v, %v", v, err)
	}
}
