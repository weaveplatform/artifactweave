package federation_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReplicationExecutionLifecycleRequiresBothIdleDrivers(t *testing.T) {
	store := inmem.New()
	m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.Coordinator(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unopened module exposed dispatch", err)
	}
	if err := m.ScheduleHandlers()[replication.ScheduleKind](t.Context(), schedule.Trigger{}); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("schedule registration initialized direction services", err)
	}
	if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Worker(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("half-initialized module exposed a worker", err)
	}
	if err := m.OpenPull(t.Context(), struct{ artifactsapi.PullSource }{}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := m.Coordinator()
	if err != nil || len(coordinator.Drivers) != 2 {
		t.Fatal("both real direction admission adapters were not bound", err)
	}
	worker, err := m.Worker()
	if err != nil || len(worker.Drivers) != 2 {
		t.Fatal("both checkpoint projections were not bound", err)
	}
	if err := m.RegisterWork(nil); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("nil queue accepted", err)
	}
	queue, err := work.New(store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterWork(queue); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterWork(queue); !errors.Is(err, work.ErrDuplicateHandler) {
		t.Fatal("duplicate execution registration accepted", err)
	}
	m.Close()
	if _, err := m.Coordinator(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("closed module exposed a coordinator", err)
	}
	if _, err := m.Worker(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("closed module exposed a worker", err)
	}
}
