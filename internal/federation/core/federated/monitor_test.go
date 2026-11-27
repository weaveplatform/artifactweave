package federated_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

func monitorFixture(t *testing.T) (*streamFixture, federated.Monitoring, string) {
	t.Helper()
	f, ops := fullSyncOps(t)
	for _, id := range []string{"b", "c"} {
		p := must(f.s.Peers().Get(t.Context(), id))
		p.ProtocolVersion, p.Status = instance.ProtocolVersion, model.PeerOnline
		if err := f.s.Peers().Put(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	return f, federated.Monitoring{Operations: ops, Items: f.items}, must(f.s.Instance().Get(t.Context())).InstanceID
}

func appendMonitorEvent(t *testing.T, f *streamFixture, self, typ, operation, path string, at time.Time) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path, "operation": operation})
	if _, err := f.s.Outbox().Append(t.Context(), outbox.OutboxEntry{Origin: self, Stream: "repo:fed", EventType: typ, Payload: body, Created: at}); err != nil {
		t.Fatal(err)
	}
}

func TestUnitMonitoringUsesCursorHealthAndFullSyncPrecedence(t *testing.T) {
	f, m, self := monitorFixture(t)
	ctx := t.Context()
	appendMonitorEvent(t, f, self, federated.EventPut, "create", "a", t0.Add(-2*time.Minute))
	check := func(want model.SyncStatus, connection string, lag int64) {
		t.Helper()
		got, err := m.Read(ctx, "fed", false)
		if err != nil || got.Status != want || len(got.Members) != 2 || got.Members[0].Status != connection || got.Members[0].LagInMS != lag {
			t.Fatalf("status %+v, %v; want %s/%s lag %d", got, err, want, connection, lag)
		}
	}
	check(model.SyncOutOfSync, "DELAYED", 120000)
	if err := f.s.Outbox().PutCursor(ctx, outbox.PeerCursor{PeerInstanceID: "b", Stream: "repo:fed", AckedSeq: 1}); err != nil {
		t.Fatal(err)
	}
	check(model.SyncSyncing, "HEALTHY", 0)
	p := must(f.s.Peers().Get(ctx, "b"))
	p.Status = model.PeerOffline
	if err := f.s.Peers().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	check(model.SyncOutOfSync, "N/A", 0)
	p.Status = model.PeerOnline
	if err := f.s.Peers().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	run := must(f.s.Federation().CreateFullSync(ctx, model.FullSyncRun{RepoKey: "fed", PeerInstanceID: "b", Status: retentionapi.RunInProgress, Started: t0}))
	// The durable incoming run is sufficient, even after its resync flag clears.
	check(model.SyncFullSyncRunning, "HEALTHY", 0)
	cfg := f.repos.values["fed"]
	cfg.Members[0].Enabled = repo.Flex(false)
	f.repos.values["fed"] = cfg
	check(model.SyncDisabled, "DISABLED", 0)
	cfg.Members[0].Enabled = repo.Flex(true)
	f.repos.values["fed"] = cfg
	run.Status = retentionapi.RunCompleted
	if err := f.s.Federation().UpdateFullSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	check(model.SyncSyncing, "HEALTHY", 0)
	if _, err := f.s.Outbox().RecordFailedFetch(ctx, outbox.FailedBlobFetch{Stream: "repo:fed", RepoKey: "fed", SHA256: sha1, FirstFailed: t0}); err != nil {
		t.Fatal(err)
	}
	check(model.SyncOutOfSync, "HEALTHY", 0)
}

func TestUnitMonitoringCountsPendingEventsAndArtifactsAcrossPages(t *testing.T) {
	f, m, self := monitorFixture(t)
	ctx := t.Context()
	for i := range 105 {
		appendMonitorEvent(t, f, self, federated.EventPut, "create", fmt.Sprintf("new-%03d", i), t0.Add(-time.Minute))
		f.items.held = append(f.items.held, artifactsapi.ItemChange{Path: fmt.Sprintf("file-%03d", i)})
	}
	appendMonitorEvent(t, f, self, federated.EventPut, "update", "overwrite", t0)
	appendMonitorEvent(t, f, self, federated.EventPut, "properties", "properties", t0)
	appendMonitorEvent(t, f, self, federated.EventDelete, "delete", "removed", t0)
	for _, path := range []string{"file-001", "pending-only"} {
		if err := f.s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: path, SHA256: sha1, Pending: []byte(`{"path":"` + path + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Outbox().PutCursor(ctx, outbox.PeerCursor{PeerInstanceID: "b", Stream: "repo:fed", LastError: "network private detail"}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Read(ctx, "fed", true)
	if err != nil {
		t.Fatal(err)
	}
	row := got.Members[0]
	if row.CreateEvents != 105 || row.UpdateEvents != 1 || row.PropsEvents != 1 || row.DeleteEvents != 1 || row.ErrorEvents != 108 || row.QueuedEvents != 108 ||
		row.RegisteredAt != t0.Add(-time.Minute).UnixMilli() {
		t.Fatalf("event counts %+v", row)
	}
	if got.FullyReplicated != 104 || got.MetadataOnly != 2 || got.InProgressTasks != 1 || got.FailedTasks != 0 {
		t.Fatalf("artifact counts %+v", got)
	}
	if err := f.s.Outbox().PutCursor(ctx, outbox.PeerCursor{PeerInstanceID: "b", Stream: "repo:fed", AckedSeq: 108}); err != nil {
		t.Fatal(err)
	}
	got, err = m.Read(ctx, "fed", true)
	if err != nil || got.Members[0].QueuedEvents != 0 || got.Members[0].RegisteredAt != 0 || got.Members[0].CreateEvents != 0 {
		t.Fatalf("acknowledged events %+v %v", got, err)
	}
}

func TestUnitMonitoringAuthorizesBeforeRepositoryDisclosure(t *testing.T) {
	_, m, _ := monitorFixture(t)
	for _, failure := range []error{kernel.ErrForbidden, kernel.ErrUnauthenticated} {
		denied := m
		denied.Authorize = func(context.Context) error { return failure }
		if _, err := denied.Read(t.Context(), "missing", true); !errors.Is(err, failure) {
			t.Fatalf("disclosed repository: %v", err)
		}
		if _, err := denied.List(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("disclosed repository list: %v", err)
		}
		if _, err := denied.ListWithBase(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("disclosed base configuration: %v", err)
		}
	}
	if _, err := m.Read(t.Context(), "missing", false); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatalf("missing repository: %v", err)
	}
	local, err := m.Read(t.Context(), "local", false)
	if err != nil || len(local.Members) != 0 {
		t.Fatalf("legacy local status %+v %v", local, err)
	}
	rows, err := m.List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Key != "fed" {
		t.Fatalf("list %+v %v", rows, err)
	}
}

func TestUnitMonitoringBasePrerequisiteKeepsLegacyList(t *testing.T) {
	_, m, _ := monitorFixture(t)
	m.Membership.BaseURL = ""
	if _, err := m.ListWithBase(t.Context()); !errors.Is(err, federated.ErrNoConnectionBaseURL) {
		t.Fatalf("missing configured base: %v", err)
	}
	if rows, err := m.List(t.Context()); err != nil || len(rows) != 1 {
		t.Fatalf("legacy listing changed: %+v %v", rows, err)
	}
}
