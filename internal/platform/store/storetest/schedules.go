package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errScheduleFault = errors.New("injected schedule write failure")

var scheduleAt = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)

// RunSchedules proves scheduler positions, queue effects and worker fencing
// against the same actual backend. The clock never sleeps.
func RunSchedules(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()
	t.Run("State", func(t *testing.T) { scheduleState(t, newStore(t)) })
	t.Run("CatchUpRestart", func(t *testing.T) { scheduleCatchUp(t, newStore(t)) })
	t.Run("TwoSchedulers", func(t *testing.T) { scheduleConcurrent(t, newStore(t)) })
	for _, mode := range []string{"disable", "edit", "delete", "recreate"} {
		t.Run("FenceTick/"+mode, func(t *testing.T) { scheduleFencedTick(t, newStore(t), mode) })
		t.Run("FenceWorker/"+mode, func(t *testing.T) { scheduleFencedWorker(t, newStore(t), mode) })
	}
	for _, mode := range []string{"advance", "enqueue"} {
		t.Run("Rollback/"+mode, func(t *testing.T) { scheduleRollback(t, newStore(t), mode) })
	}
	t.Run("LeaseGeneration", func(t *testing.T) { scheduleGeneration(t, newStore(t)) })
}

func scheduler(s Store, now *time.Time) *schedule.Service {
	return &schedule.Service{Store: s, Now: func() time.Time { return *now }}
}

func configureSchedule(t *testing.T, svc *schedule.Service, key string) schedule.State {
	t.Helper()
	return storekit.Get[schedule.State](t)(svc.Configure(t.Context(), schedule.Reference{Kind: "cleanup", Key: key}, "0 * * * * ?", "", true))
}

func scheduleState(t *testing.T, s Store) {
	ctx := t.Context()
	now := scheduleAt
	svc := scheduler(s, &now)
	ref := schedule.Reference{Kind: "cleanup", Key: "key"}
	_, err := s.Schedules().Get(ctx, ref)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = s.Schedules().Get(ctx, schedule.Reference{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	storekit.WantErr(t, s.Schedules().Delete(ctx, schedule.Reference{}), kernel.ErrInvalid)
	_, err = svc.Configure(ctx, ref, "bad", "", true)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = s.Schedules().Get(ctx, ref)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = svc.Configure(ctx, ref, "0 0 0 1 1 ? 2020", "", true)
	storekit.WantErr(t, err, schedule.ErrNoFuture)
	state := configureSchedule(t, svc, "key")
	if state.Next != now.Add(time.Minute) || state.Zone != "UTC" || state.Revision == "" {
		t.Fatalf("configuration=%+v", state)
	}
	again := configureSchedule(t, svc, "key")
	storekit.Same(t, "identical configuration keeps position/revision", state, again)
	storekit.WantErr(t, s.Schedules().Advance(ctx, state, now.Add(2*time.Minute)), kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		bad := state
		bad.Revision = "stale"
		storekit.WantErr(t, s.Schedules().Advance(ctx, bad, now.Add(2*time.Minute)), kernel.ErrConflict)
		return nil
	}))
	// A nested SQL conflict deliberately poisons its transaction only when Tx
	// itself returned an error; the direct CAS above is a normal tested refusal.
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return s.Schedules().Advance(ctx, state, time.Time{}) }))
	exhausted := storekit.Get[schedule.State](t)(s.Schedules().Get(ctx, ref))
	if !exhausted.Next.IsZero() || !exhausted.Enabled {
		t.Fatalf("exhausted state=%+v", exhausted)
	}
	if due := storekit.Get[[]schedule.State](t)(s.Schedules().Due(ctx, now.Add(time.Hour), 1)); len(due) != 0 {
		t.Fatalf("exhausted schedule is due: %+v", due)
	}
	storekit.Must(t, s.Schedules().Delete(ctx, ref))
	storekit.WantErr(t, s.Schedules().Delete(ctx, ref), kernel.ErrNotFound)
	recreated := configureSchedule(t, svc, "key")
	if recreated.Revision == state.Revision {
		t.Fatal("delete/recreate reused a stale revision")
	}
	for i, key := range []string{"z", "é", "a", "A"} {
		v := recreated
		v.Key = key
		v.Next = now.Add(time.Duration(i%2+1) * time.Minute)
		storekit.Must(t, s.Schedules().Put(ctx, v))
	}
	states := storekit.Get[[]schedule.State](t)(s.Schedules().Due(ctx, now.Add(3*time.Minute), 2))
	if len(states) != 2 || states[0].Key != "a" || states[1].Key != "key" {
		t.Fatalf("bounded due order=%+v", states)
	}
	bad := recreated
	bad.Revision = ""
	storekit.WantErr(t, s.Schedules().Put(ctx, bad), kernel.ErrInvalid)
	bad = recreated
	bad.Enabled = false
	storekit.WantErr(t, s.Schedules().Put(ctx, bad), kernel.ErrInvalid)
	bad = recreated
	bad.Cron = "invalid"
	storekit.WantErr(t, s.Schedules().Put(ctx, bad), kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		storekit.WantErr(t, s.Schedules().Advance(ctx, recreated, recreated.Next), kernel.ErrInvalid)
		return nil
	}))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Schedules().Get(cancelled, ref)
	storekit.WantErr(t, err, context.Canceled)
	_, err = s.Schedules().Due(cancelled, now, 0)
	storekit.WantErr(t, err, context.Canceled)
	storekit.WantErr(t, s.Schedules().Put(cancelled, recreated), context.Canceled)
	storekit.WantErr(t, s.Schedules().Delete(cancelled, ref), context.Canceled)
}

func scheduleCatchUp(t *testing.T, s Store) {
	now := scheduleAt
	state := configureSchedule(t, scheduler(s, &now), "catch-up")
	now = now.Add(3 * time.Minute)
	for range 3 {
		// A new service per pass simulates a restart between durable positions.
		n, err := scheduler(s, &now).Tick(t.Context(), 1)
		storekit.Must(t, err)
		if n != 1 {
			t.Fatalf("catch-up enqueued %d; want1", n)
		}
	}
	if n := storekit.Get[int](t)(scheduler(s, &now).Tick(t.Context(), 1)); n != 0 {
		t.Fatalf("repeated tick enqueued %d", n)
	}
	items := leaseSchedules(t, s, now, "worker")
	if len(items) != 3 {
		t.Fatalf("missed logical occurrences coalesced: %d", len(items))
	}
	fires := make([]time.Time, 0, len(items))
	ids := map[string]bool{}
	for _, item := range items {
		var trigger schedule.Trigger
		storekit.Must(t, json.Unmarshal(item.Payload, &trigger))
		if trigger.Revision != state.Revision || trigger.ID != item.Key || ids[trigger.ID] {
			t.Fatalf("unstable/duplicate trigger=%+v", trigger)
		}
		ids[trigger.ID] = true
		fires = append(fires, trigger.Fire)
		storekit.Must(t, s.WorkQueue().Complete(t.Context(), item.Kind, item.Key, item.Generation, now))
	}
	slices.SortFunc(fires, func(a, b time.Time) int { return a.Compare(b) })
	for i, fire := range fires {
		if fire != state.Next.Add(time.Duration(i)*time.Minute) {
			t.Fatalf("logical fire=%v", fire)
		}
	}
	if n := storekit.Get[int](t)(scheduler(s, &now).Tick(t.Context(), 10)); n != 0 {
		t.Fatal("completed queue rows were scheduled again")
	}
}

func scheduleConcurrent(t *testing.T, s Store) {
	now := scheduleAt
	configureSchedule(t, scheduler(s, &now), "concurrent")
	now = now.Add(time.Minute)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			<-start
			n, err := scheduler(s, &now).Tick(t.Context(), 10)
			if err != nil {
				t.Error(err)
			}
			results <- n
		})
	}
	close(start)
	wg.Wait()
	close(results)
	total := 0
	for n := range results {
		total += n
	}
	if total != 1 || len(leaseSchedules(t, s, now, "worker")) != 1 {
		t.Fatalf("two schedulers produced %d logical runs", total)
	}
}

func leaseSchedules(t *testing.T, s Store, now time.Time, holder string) []work.WorkItem {
	t.Helper()
	return storekit.Get[[]work.WorkItem](
		t,
	)(
		s.WorkQueue().Lease(t.Context(), work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: holder, Limit: 100, Now: now, Until: now.Add(time.Minute)}),
	)
}

func changeSchedule(t *testing.T, s Store, now *time.Time, state schedule.State, mode string) {
	t.Helper()
	svc := scheduler(s, now)
	switch mode {
	case "disable":
		storekit.Get[schedule.State](t)(svc.Configure(t.Context(), state.Reference, state.Cron, state.Zone, false))
	case "edit":
		storekit.Get[schedule.State](t)(svc.Configure(t.Context(), state.Reference, "30 * * * * ?", state.Zone, true))
	case "delete":
		storekit.Must(t, s.Schedules().Delete(t.Context(), state.Reference))
	case "recreate":
		storekit.Must(t, s.Schedules().Delete(t.Context(), state.Reference))
		configureSchedule(t, svc, state.Key)
	}
}

type scheduleBackend struct {
	Store
	schedules schedule.Store
	queue     work.WorkQueue
}

func (s scheduleBackend) Schedules() schedule.Store {
	if s.schedules != nil {
		return s.schedules
	}
	return s.Store.Schedules()
}

func (s scheduleBackend) WorkQueue() work.WorkQueue {
	if s.queue != nil {
		return s.queue
	}
	return s.Store.WorkQueue()
}

type pausedDue struct {
	schedule.Store
	read, resume chan struct{}
}

func (s pausedDue) Due(ctx context.Context, now time.Time, limit int) ([]schedule.State, error) {
	states, err := s.Store.Due(ctx, now, limit)
	if err != nil {
		return nil, fmt.Errorf("read before pause: %w", err)
	}
	close(s.read)
	select {
	case <-s.resume:
		return states, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("paused due read: %w", ctx.Err())
	}
}

func scheduleFencedTick(t *testing.T, s Store, mode string) {
	now := scheduleAt
	state := configureSchedule(t, scheduler(s, &now), "fenced")
	now = now.Add(time.Minute)
	paused := pausedDue{Store: s.Schedules(), read: make(chan struct{}), resume: make(chan struct{})}
	svc := &schedule.Service{Store: scheduleBackend{Store: s, schedules: paused}, Now: func() time.Time { return now }}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		n, err := svc.Tick(ctx, 10)
		if n != 0 && err == nil {
			err = errScheduleFault
		}
		done <- err
	}()
	select {
	case <-paused.read:
	case <-ctx.Done():
		t.Fatal("tick did not reach due snapshot")
	}
	changeSchedule(t, s, &now, state, mode)
	close(paused.resume)
	select {
	case err := <-done:
		storekit.Must(t, err)
	case <-ctx.Done():
		t.Fatal("tick did not finish")
	}
	if got := leaseSchedules(t, s, now, "worker"); len(got) != 0 {
		t.Fatalf("obsolete tick queued work: %+v", got)
	}
}

func scheduleFencedWorker(t *testing.T, s Store, mode string) {
	now := scheduleAt
	svc := scheduler(s, &now)
	state := configureSchedule(t, svc, "worker-fence")
	now = now.Add(time.Minute)
	storekit.Get[int](t)(svc.Tick(t.Context(), 10))
	items := leaseSchedules(t, s, now, "worker")
	if len(items) != 1 {
		t.Fatalf("queued work=%d", len(items))
	}
	changeSchedule(t, s, &now, state, mode)
	ran := false
	storekit.Must(t, svc.Execute(t.Context(), items[0], map[string]schedule.Handler{"cleanup": func(context.Context, schedule.Trigger) error { ran = true; return nil }}))
	if ran {
		t.Fatal("obsolete schedule reached business worker")
	}
}

type failedScheduleAdvance struct{ schedule.Store }

func (s failedScheduleAdvance) Advance(ctx context.Context, state schedule.State, next time.Time) error {
	if err := s.Store.Advance(ctx, state, next); err != nil {
		return fmt.Errorf("advance before fault: %w", err)
	}
	return errScheduleFault
}

type failedScheduleEnqueue struct{ work.WorkQueue }

func (q failedScheduleEnqueue) Enqueue(ctx context.Context, kind, key string, payload []byte, now time.Time) error {
	if err := q.WorkQueue.Enqueue(ctx, kind, key, payload, now); err != nil {
		return fmt.Errorf("enqueue before fault: %w", err)
	}
	return errScheduleFault
}

func scheduleRollback(t *testing.T, s Store, mode string) {
	now := scheduleAt
	state := configureSchedule(t, scheduler(s, &now), "rollback")
	now = now.Add(time.Minute)
	backend := scheduleBackend{Store: s}
	if mode == "advance" {
		backend.schedules = failedScheduleAdvance{s.Schedules()}
	} else {
		backend.queue = failedScheduleEnqueue{s.WorkQueue()}
	}
	svc := &schedule.Service{Store: backend, Now: func() time.Time { return now }}
	_, err := svc.Tick(t.Context(), 10)
	storekit.WantErr(t, err, errScheduleFault)
	storekit.Same(t, "failed transaction retains logical position", state, storekit.Get[schedule.State](t)(s.Schedules().Get(t.Context(), state.Reference)))
	if len(leaseSchedules(t, s, now, "worker")) != 0 {
		t.Fatal("failed tick left queued work")
	}
	if n := storekit.Get[int](t)(scheduler(s, &now).Tick(t.Context(), 10)); n != 1 {
		t.Fatal("failed occurrence could not be retried")
	}
}

func scheduleGeneration(t *testing.T, s Store) {
	now := scheduleAt
	svc := scheduler(s, &now)
	configureSchedule(t, svc, "generation")
	now = now.Add(time.Minute)
	storekit.Get[int](t)(svc.Tick(t.Context(), 10))
	old := leaseSchedules(t, s, now, "old")[0]
	now = now.Add(2 * time.Minute)
	current := leaseSchedules(t, s, now, "new")[0]
	runs := 0
	handlers := map[string]schedule.Handler{"cleanup": func(ctx context.Context, _ schedule.Trigger) error {
		runs++
		// A fresh transaction inside business work proves admission released
		// its metadata gate before calling the handler.
		return s.Tx(ctx, func(ctx context.Context) error { return s.Settings().Put(ctx, "scheduled-run", runs) })
	}}
	storekit.WantErr(t, svc.Execute(t.Context(), old, handlers), kernel.ErrConflict)
	if runs != 0 {
		t.Fatal("expired lease executed")
	}
	storekit.Must(t, svc.Execute(t.Context(), current, handlers))
	if runs != 1 {
		t.Fatal("current lease did not execute")
	}
}
