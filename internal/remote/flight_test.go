package remote

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func TestAccFlightCancellationDoesNotPoisonPeers(t *testing.T) {
	var g flightGroup[int]
	var calls atomic.Int32
	gate := new(fakeupstream.Barrier)
	work := func(ctx context.Context) (int, error) { calls.Add(1); return 42, gate.Wait(ctx) }
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := g.do(ctx, "key", work); first <- err }()
	<-gate.Reached()
	second := make(chan error, 1)
	go func() {
		n, err := g.do(t.Context(), "key", work)
		if n != 42 && err == nil {
			err = ErrRequest
		}
		second <- err
	}()
	waitFor(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.active["key"] != nil && g.active["key"].waiters == 2
	})
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	gate.Release()
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestAccLastWaiterCancelsAndNewDemandStartsFresh(t *testing.T) {
	var g flightGroup[int]
	started, stopped := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := g.do(ctx, "key", func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return 0, ctx.Err()
		})
		done <- err
	}()
	<-started
	cancel()
	<-done
	<-stopped
	n, err := g.do(t.Context(), "key", func(context.Context) (int, error) { return 7, nil })
	if err != nil || n != 7 {
		t.Fatal(n, err)
	}
	_, err = g.do(
		ctx,
		"key",
		func(context.Context) (int, error) { t.Error("canceled request started work"); return 0, nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAccAdmissionLimitsQueueAndCancellation(t *testing.T) {
	a := admission{perRepo: 1, global: 2, queue: 1}
	releaseA, err := a.acquire(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := a.acquire(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	waiting := make(chan error, 1)
	go func() {
		release, err := a.acquire(ctx, "a")
		if release != nil {
			release()
		}
		waiting <- err
	}()
	waitFor(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.repos["a"].queued == 1 })
	if _, err = a.acquire(t.Context(), "a"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	cancel()
	if err = <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	third := make(chan func(), 1)
	go func() {
		release, err := a.acquire(t.Context(), "c")
		if err != nil {
			t.Error(err)
		}
		third <- release
	}()
	waitFor(
		t,
		func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.repos["c"] != nil && a.repos["c"].queued == 1 },
	)
	releaseB()
	releaseB()
	releaseC := <-third
	releaseC()
	releaseA()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != 0 || len(a.repos) != 0 {
		t.Fatal(a.active, len(a.repos))
	}
}

func TestAccDefaultAdmissionAndCanceledInitialRequest(t *testing.T) {
	a := new(admission)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.acquire(ctx, "r"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release, err := a.acquire(t.Context(), "r")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if a.perRepo != 8 || a.global != 64 || a.queue != 256 {
		t.Fatal("default bounds")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}
