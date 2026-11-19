package schedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errFailure = errors.New("injected scheduling failure")

func TestUnitScheduleRunUsesQuartzAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := inmem.New(memtx.New())
		svc := &schedule.Service{Store: s}
		if _, err := svc.Configure(t.Context(), schedule.Reference{Kind: "cleanup", Key: "policy"}, "* * * * * ?", "", true); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- svc.Run(ctx, time.Second, 1) }()
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		stats, err := s.WorkQueue().Stats(t.Context())
		if err != nil || len(stats) != 1 || stats[0].Pending != 1 {
			t.Fatalf("Quartz poll effects=%+v,error=%v", stats, err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	svc := &schedule.Service{Store: inmem.New(memtx.New())}
	if err := svc.Run(t.Context(), 0, 1); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("invalid polling=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := svc.Run(ctx, time.Second, 1); err != nil {
		t.Fatal(err)
	}
}

type backendFaults struct {
	*inmem.Store
	schedules schedule.Store
	queue     work.WorkQueue
}

func (s backendFaults) Schedules() schedule.Store {
	if s.schedules != nil {
		return s.schedules
	}
	return s.Store.Schedules()
}

func (s backendFaults) WorkQueue() work.WorkQueue {
	if s.queue != nil {
		return s.queue
	}
	return s.Store.WorkQueue()
}

type stateFaults struct {
	schedule.Store
	get, put, due error
	corrupt       bool
	cancel        context.CancelFunc
}

func (s stateFaults) Get(ctx context.Context, r schedule.Reference) (schedule.State, error) {
	if s.get != nil {
		return schedule.State{}, s.get
	}
	v, err := s.Store.Get(ctx, r)
	if s.corrupt {
		v.Cron = "invalid"
	}
	return v, err
}

func (s stateFaults) Put(ctx context.Context, v schedule.State) error {
	if err := s.Store.Put(ctx, v); err != nil {
		return fmt.Errorf("put before fault: %w", err)
	}
	return s.put
}

func (s stateFaults) Due(ctx context.Context, now time.Time, limit int) ([]schedule.State, error) {
	if s.cancel != nil {
		s.cancel()
		return nil, context.Canceled
	}
	if s.due != nil {
		return nil, s.due
	}
	return s.Store.Due(ctx, now, limit)
}

func TestUnitScheduleFailuresPreserveConfigurationAndPosition(t *testing.T) {
	s := inmem.New(memtx.New())
	now := instant(t, "2026-11-19T00:00:00Z")
	svc := &schedule.Service{Store: s, Now: func() time.Time { return now }}
	ref := schedule.Reference{Kind: "cleanup", Key: "policy"}
	state, err := svc.Configure(t.Context(), ref, "0 * * * * ?", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []stateFaults{{Store: s.Schedules(), get: errFailure}, {Store: s.Schedules(), put: errFailure}} {
		bad := &schedule.Service{Store: backendFaults{Store: s, schedules: fault}, Now: svc.Now}
		if _, err := bad.Configure(t.Context(), ref, "30 * * * * ?", "", true); !errors.Is(err, errFailure) {
			t.Fatalf("configure failure=%v", err)
		}
		got, err := s.Schedules().Get(t.Context(), ref)
		if err != nil || got != state {
			t.Fatalf("failed configure changed state: %+v,%v", got, err)
		}
	}
	now = now.Add(time.Minute)
	for _, fault := range []stateFaults{{Store: s.Schedules(), get: errFailure}, {Store: s.Schedules(), due: errFailure}, {Store: s.Schedules(), corrupt: true}} {
		bad := &schedule.Service{Store: backendFaults{Store: s, schedules: fault}, Now: svc.Now}
		if _, err := bad.Tick(t.Context(), 10); err == nil {
			t.Fatal("faulted tick succeeded")
		}
		got, err := s.Schedules().Get(t.Context(), ref)
		if err != nil || got != state {
			t.Fatalf("failed tick advanced state: %+v,%v", got, err)
		}
	}
	bad := &schedule.Service{Store: backendFaults{Store: s, schedules: stateFaults{Store: s.Schedules(), due: errFailure}}}
	if err := bad.Run(t.Context(), time.Second, 1); !errors.Is(err, errFailure) {
		t.Fatalf("poll failure=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	bad.Store = backendFaults{Store: s, schedules: stateFaults{Store: s.Schedules(), cancel: cancel}}
	if err := bad.Run(ctx, time.Second, 1); err != nil {
		t.Fatalf("canceled poll failure=%v", err)
	}
	if _, err := svc.Configure(t.Context(), schedule.Reference{}, state.Cron, "", true); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("invalid reference=%v", err)
	}
}

func TestUnitScheduleDispatcherUsesDurableQueue(t *testing.T) {
	s := inmem.New(memtx.New())
	now := instant(t, "2026-11-19T00:00:00Z")
	svc := &schedule.Service{Store: s, Now: func() time.Time { return now }}
	if _, err := svc.Configure(t.Context(), schedule.Reference{Kind: "cleanup", Key: "policy"}, "0 * * * * ?", "", true); err != nil {
		t.Fatal(err)
	}
	queue, err := work.New(s, work.Options{Now: svc.Now})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	handlers := map[string]schedule.Handler{"cleanup": func(context.Context, schedule.Trigger) error { ran++; return nil }}
	if err := svc.Register(queue, handlers); err != nil {
		t.Fatal(err)
	}
	handlers["cleanup"] = func(context.Context, schedule.Trigger) error { return errFailure }
	if err := svc.Register(queue, handlers); !errors.Is(err, work.ErrDuplicateHandler) {
		t.Fatalf("duplicate dispatcher=%v", err)
	}
	now = now.Add(time.Minute)
	if _, err := svc.Tick(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	items, err := s.WorkQueue().Lease(t.Context(), work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "peek", Limit: 1, Now: now, Until: now.Add(time.Minute)})
	if err != nil || len(items) != 1 {
		t.Fatalf("queued occurrence=%v,%v", items, err)
	}
	item := items[0]
	if err := s.WorkQueue().Release(t.Context(), item.Kind, item.Key, item.Generation, now); err != nil {
		t.Fatal(err)
	}
	if err := queue.EnsureFresh(t.Context(), item.Kind, item.Key); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("dispatcher did not retain registered handler: %d", ran)
	}
	if _, err := s.WorkQueue().Get(t.Context(), item.Kind, item.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("queue did not complete occurrence: %v", err)
	}
}

type queueReadFault struct{ work.WorkQueue }

func (q queueReadFault) Get(context.Context, string, string) (work.WorkItem, error) {
	return work.WorkItem{}, errFailure
}

func TestUnitScheduleWorkerAdmissionFailures(t *testing.T) {
	s := inmem.New(memtx.New())
	now := instant(t, "2026-11-19T00:00:00Z")
	svc := &schedule.Service{Store: s, Now: func() time.Time { return now }}
	if _, err := svc.Configure(t.Context(), schedule.Reference{Kind: "cleanup", Key: "policy"}, "0 * * * * ?", "", true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := svc.Tick(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	items, err := s.WorkQueue().Lease(t.Context(), work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "worker", Limit: 1, Now: now, Until: now.Add(time.Minute)})
	if err != nil || len(items) != 1 {
		t.Fatalf("lease=%v,%v", items, err)
	}
	item := items[0]
	for _, change := range []func(*work.WorkItem){
		func(i *work.WorkItem) { i.Payload = []byte("bad") }, func(i *work.WorkItem) { i.Kind = "wrong" }, func(i *work.WorkItem) { i.Key = "wrong" },
		func(i *work.WorkItem) {
			var tr schedule.Trigger
			_ = json.Unmarshal(i.Payload, &tr)
			tr.Fire = time.Time{}
			i.Payload, _ = json.Marshal(tr)
		},
		func(i *work.WorkItem) {
			var tr schedule.Trigger
			_ = json.Unmarshal(i.Payload, &tr)
			tr.Reference.Key = ""
			i.Payload, _ = json.Marshal(tr)
		},
	} {
		bad := item
		change(&bad)
		if err := svc.Execute(t.Context(), bad, nil); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("malformed worker=%v", err)
		}
	}
	if err := svc.Execute(t.Context(), item, nil); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("unknown owner=%v", err)
	}
	handlers := map[string]schedule.Handler{"cleanup": func(context.Context, schedule.Trigger) error { return errFailure }}
	if err := svc.Execute(t.Context(), item, handlers); !errors.Is(err, errFailure) {
		t.Fatalf("handler failure=%v", err)
	}
	for _, backend := range []backendFaults{{Store: s, schedules: stateFaults{Store: s.Schedules(), get: errFailure}}, {Store: s, queue: queueReadFault{s.WorkQueue()}}} {
		bad := &schedule.Service{Store: backend, Now: svc.Now}
		if err := bad.Execute(t.Context(), item, handlers); !errors.Is(err, errFailure) {
			t.Fatalf("admission failure=%v", err)
		}
	}
	if err := s.WorkQueue().Complete(t.Context(), item.Kind, item.Key, item.Generation, now); err != nil {
		t.Fatal(err)
	}
	if err := svc.Execute(t.Context(), item, handlers); err != nil {
		t.Fatalf("completed work was admitted: %v", err)
	}
}
