package schedule_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitScheduleExpiredAdmissionRemainsRetriable(t *testing.T) {
	s := inmem.New(memtx.New())
	now := instant(t, "2026-11-19T00:00:00Z")
	svc := &schedule.Service{Store: s, Now: func() time.Time { return now }}
	if _, err := svc.Configure(t.Context(), schedule.Reference{Kind: "cleanup", Key: "policy"}, "0 * * * * ?", "", true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := svc.Tick(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	items, err := s.WorkQueue().Lease(t.Context(), work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "peek", Limit: 1, Now: now, Until: now.Add(time.Minute)})
	if err != nil || len(items) != 1 {
		t.Fatalf("queued work=%v,%v", items, err)
	}
	item := items[0]
	if err := s.WorkQueue().Release(t.Context(), item.Kind, item.Key, item.Generation, now); err != nil {
		t.Fatal(err)
	}
	queue, err := work.New(s, work.Options{Lease: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	if err := svc.Register(queue, map[string]schedule.Handler{"cleanup": func(context.Context, schedule.Trigger) error { runs++; return nil }}); err != nil {
		t.Fatal(err)
	}
	// Admission observes the delay that expired this generation before a new
	// worker took over; queue settlement must preserve the work for retry.
	svc.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := queue.EnsureFresh(t.Context(), item.Kind, item.Key); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("expired admission=%v", err)
	}
	pending, err := s.WorkQueue().Get(t.Context(), item.Kind, item.Key)
	if err != nil || pending.State != work.WorkPending || runs != 0 {
		t.Fatalf("expired occurrence lost: state=%+v,error=%v,runs=%d", pending, err, runs)
	}
	svc.Now = func() time.Time { return now }
	if err := queue.EnsureFresh(t.Context(), item.Kind, item.Key); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("valid retry ran%d times", runs)
	}
	if _, err := s.WorkQueue().Get(t.Context(), item.Kind, item.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("successful retry did not complete: %v", err)
	}
}

func TestUnitScheduleInvalidIncrementsNeverActivate(t *testing.T) {
	s := inmem.New(memtx.New())
	svc := &schedule.Service{Store: s}
	for _, expression := range []string{"*/60 * * * * ?", "0 */60 * * * ?", "0 0 */24 * * ?"} {
		ref := schedule.Reference{Kind: "cleanup", Key: expression}
		if _, err := svc.Configure(t.Context(), ref, expression, "", true); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("increment validation=%v", err)
		}
		if _, err := s.Schedules().Get(t.Context(), ref); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("invalid increment became active: %v", err)
		}
	}
}
