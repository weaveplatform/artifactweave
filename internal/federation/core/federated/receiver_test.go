package federated_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReceiverTrustDirections(t *testing.T) {
	s := federated.Stream{}
	for _, tc := range []struct {
		uses                    []model.UseCase
		receives, send, receive bool
	}{
		{nil, false, false, false},
		{[]model.UseCase{model.UseFederatedRepoRead}, false, true, false},
		{[]model.UseCase{model.UseFederatedRepoRead}, true, false, true},
		{[]model.UseCase{model.UseFederatedRepo}, false, true, true},
		{[]model.UseCase{model.UseFederatedRepo, model.UseFederatedRepoRead}, true, true, true},
	} {
		p := model.Peer{UseCases: tc.uses, ReceivesFederationOnly: tc.receives}
		if s.AllowsPeer(p, streamsync.Send) != tc.send || s.AllowsPeer(p, streamsync.Receive) != tc.receive {
			t.Fatalf("wrong directions for %+v", p)
		}
	}
}

func TestUnitReceiverLocalWritesAndAuthority(t *testing.T) {
	f, ops := fullSyncOps(t)
	c := f.repos.values["fed"]
	c.Members[0].Mode = repo.ModeReceiver
	f.repos.values["fed"] = c
	ctx := t.Context()
	for _, change := range []artifactsapi.ItemChange{
		{Repository: "fed", Path: "local.txt", Actual: kernel.Checksums{SHA256: sha1}, Size: 3},
		{Repository: "fed", Path: "local.txt", Deleted: true},
	} {
		if err := f.k.ItemChanged(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.entries(t)) != 0 {
		t.Fatal("receiver appended local writes")
	}
	if _, err := f.s.Stamps().Stamp(ctx, "fed", "local.txt"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("receiver stamped a local write: %v", err)
	}
	for _, forced := range []bool{false, true} {
		if err := ops.FullSync(ctx, "fed", "", forced); err == nil || !strings.Contains(err.Error(), "receiver mode") {
			t.Fatalf("receiver initiated full sync: %v", err)
		}
	}
	if ok, err := f.k.AuthoritativeSnapshot(ctx, "repo:fed"); err != nil || !ok {
		t.Fatalf("receiver snapshot authority: %v %v", ok, err)
	}
	if ok, err := f.k.AllowsStream(ctx, "repo:fed", caller(model.UseFederatedRepo), streamsync.Send); err != nil || ok {
		t.Fatalf("receiver supplied stream: %v %v", ok, err)
	}
	p := caller(model.UseFederatedRepo)
	if _, err := f.e.Events(ctx, p, "repo:fed", 0, 1); !errors.Is(err, streamsync.ErrRefused) {
		t.Fatalf("receiver served events: %v", err)
	}
	if _, err := f.e.Snapshot(ctx, p, "repo:fed", "", 1); !errors.Is(err, streamsync.ErrRefused) {
		t.Fatalf("receiver served snapshot: %v", err)
	}
	for _, change := range []func(*repo.Config){
		func(c *repo.Config) { c.Members = c.Members[1:] },
		func(c *repo.Config) { c.Members[0].Mode = repo.ModeBidirectional },
		func(c *repo.Config) { c.Class = repo.Local },
	} {
		next := c
		next.Members = slices.Clone(c.Members)
		change(&next)
		if err := ops.Membership.CheckLocalUpdate(ctx, next, f.repos.Get); err == nil || !strings.Contains(err.Error(), "receiver mode") {
			t.Fatalf("receiver membership change allowed: %v", err)
		}
	}
	// Reordering members and rendering the self URL do not constitute a change.
	same := c
	same.Members = slices.Clone(c.Members)
	slices.Reverse(same.Members)
	if err := ops.Membership.CheckLocalUpdate(ctx, same, f.repos.Get); err != nil {
		t.Fatal(err)
	}
	next := c
	next.Key = "new"
	if err := ops.Membership.CheckLocalUpdate(ctx, next, f.repos.Get); err != nil {
		t.Fatal(err)
	}
}

func TestUnitReceiverPeerAuthorizationIgnoresMaliciousMode(t *testing.T) {
	f := newStreamFixture(t)
	// The issuer's peer record says this caller redeemed /r. Its member is
	// deliberately still bidirectional; the trust direction remains binding.
	p := caller(model.UseFederatedRepoRead)
	if _, err := f.e.Receive(t.Context(), p, streamsync.Batch{Origin: p.InstanceID, Stream: "repo:fed", FromSeq: 1}); !errors.Is(err, streamsync.ErrRefused) {
		t.Fatalf("/r redeemer pushed: %v", err)
	}
	p.ReceivesFederationOnly = true
	if _, err := f.e.Events(t.Context(), p, "repo:fed", 0, 1); !errors.Is(err, streamsync.ErrRefused) {
		t.Fatalf("/r redeemer supplied events: %v", err)
	}
	ms := f.s.members(t, "fed")
	ms[0].Mode = repo.ModeReceiver
	if err := f.s.Federation().SetMembers(t.Context(), "fed", ms); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.k.AllowsStream(t.Context(), "repo:fed", caller(model.UseFederatedRepo), streamsync.Receive); err != nil || ok {
		t.Fatalf("receiver pushed with two-way trust: %v %v", ok, err)
	}
}

func TestUnitReceiverAdditionQueuesFullSync(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	c := fed("fed", bBase+"/fed")
	c.Members[0].Mode = repo.ModeReceiver
	if err := ms.Reconcile(t.Context(), c, true); err != nil {
		t.Fatal(err)
	}
	if got := queuedKeys(t, s, federated.FullSyncRequestKind, "b/fed/normal"); len(got) != 1 {
		t.Fatalf("receiver addition not synced: %v", got)
	}
}

func TestUnitReceiverDirectionPreservesMissingMembership(t *testing.T) {
	f := newStreamFixture(t)
	if err := f.s.Federation().SetMembers(t.Context(), "fed", nil); err != nil {
		t.Fatal(err)
	}
	p := caller(model.UseFederatedRepo)
	if _, err := f.e.Receive(t.Context(), p, streamsync.Batch{Origin: p.InstanceID, Stream: "repo:fed", FromSeq: 1}); !errors.Is(err, streamsync.ErrNoStream) {
		t.Fatalf("non-member direction check concealed missing stream: %v", err)
	}
}

func TestUnitReceiverReadsSourceConfiguration(t *testing.T) {
	s := newStore(t)
	p := caller(model.UseFederatedRepoRead)
	p.ReceivesFederationOnly = true
	if err := s.Peers().Put(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	ms := membership(s)
	rs := newRepositories()
	r := federated.Receiver{Membership: ms, Repositories: rs}
	cfg := sent(t, func(body map[string]any) {
		members := body["members"].([]any)
		for _, raw := range members {
			m := raw.(map[string]any)
			if strings.Contains(m["url"].(string), "a.example") {
				m["mode"] = "receiver"
			}
		}
	})
	if err := r.Apply(t.Context(), p, "fed", cfg); err != nil {
		t.Fatal(err)
	}
	if len(s.queued(t)) != 0 {
		t.Fatal("receiving configuration propagated")
	}
	c := rs.values["fed"]
	if err := ms.Reconcile(t.Context(), c, true); err != nil {
		t.Fatal(err)
	}
	if len(s.queued(t)) != 0 {
		t.Fatal("receiver queued configSync")
	}
	// The receiver's sender also refuses stale work queued before its role changed.
	rem := &remote{}
	sender := federated.Sender{Membership: ms, Repositories: rs, Remote: rem}
	if err := sender.Send(t.Context(), work.WorkItem{Key: "b/fed"}); err != nil {
		t.Fatal(err)
	}
	if len(rem.sent) != 0 {
		t.Fatal("receiver sent stale configuration work")
	}
}

func TestUnitReceiverConnectionModeAndHealth(t *testing.T) {
	f, ops := fullSyncOps(t)
	ctx := t.Context()
	ms := f.s.members(t, "fed")
	ms[0].Mode = repo.ModeReceiver
	if err := f.s.Federation().SetMembers(ctx, "fed", ms); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		p, err := f.s.Peers().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		p.ProtocolVersion = instance.ProtocolVersion
		p.Status = model.PeerOnline
		if err := f.s.Peers().Put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ops.Connections(ctx, "fed")
	if err != nil || len(rows) != 2 {
		t.Fatalf("connection states: %+v %v", rows, err)
	}
	if rows[0].Mode != "Receiving_Only" || rows[0].Status != "HEALTHY" || rows[1].Status != "DISABLED" {
		t.Fatalf("receiver mode versus health: %+v", rows)
	}
	if err := f.k.ItemChanged(ctx, artifactsapi.ItemChange{Repository: "fed", Path: "queued", Folder: true}); err != nil {
		t.Fatal(err)
	}
	rows, err = ops.Connections(ctx, "fed")
	if err != nil || rows[0].Status != "HEALTHY" || rows[0].QueuedEvents != 1 {
		t.Fatalf("pending event: %+v %v", rows, err)
	}
	if err := f.s.Outbox().PutCursor(ctx, outbox.PeerCursor{PeerInstanceID: "b", Stream: "repo:fed", LastError: "unreachable"}); err != nil {
		t.Fatal(err)
	}
	rows, err = ops.Connections(ctx, "fed")
	if err != nil || rows[0].Status != "ERROR" {
		t.Fatalf("failed send: %+v %v", rows, err)
	}
	if _, err := ops.Connections(ctx, "missing"); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatal(err)
	}
	ops.Authorize = func(context.Context) error { return kernel.ErrForbidden }
	if _, err := ops.Connections(ctx, "missing"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestUnitReceiverConnectionProgressStates(t *testing.T) {
	f, ops := fullSyncOps(t)
	ctx := t.Context()
	p, err := f.s.Peers().Get(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	p.ProtocolVersion = instance.ProtocolVersion
	p.Status = model.PeerOnline
	if err := f.s.Peers().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	status := func(want string) {
		t.Helper()
		rows, err := ops.Connections(ctx, "fed")
		if err != nil || len(rows) == 0 || rows[0].Status != want {
			t.Fatalf("connections=%+v %v, want %s", rows, err, want)
		}
	}
	if err := f.k.ItemChanged(ctx, artifactsapi.ItemChange{Repository: "fed", Path: "queued", Folder: true}); err != nil {
		t.Fatal(err)
	}
	status("HEALTHY")
	ops.Membership.Now = func() time.Time { return t0.Add(2 * time.Minute) }
	status("DELAYED")
	if err := ops.Membership.QueueFullSync(ctx, "fed", "b", false); err != nil {
		t.Fatal(err)
	}
	status("PENDING_FS")
	leased, ok, err := f.s.WorkQueue().LeaseKey(ctx, federated.FullSyncRequestKind, "b/fed/normal", work.WorkLease{Holder: "test", Now: t0, Until: t0.Add(time.Minute)})
	if err != nil || !ok {
		t.Fatalf("lease request: %+v %v %v", leased, ok, err)
	}
	if err := f.s.WorkQueue().Fail(ctx, leased.Kind, leased.Key, leased.Generation, work.WorkFailure{Park: true, Error: "request refused", Now: t0}); err != nil {
		t.Fatal(err)
	}
	status("ERROR")
	if err := f.s.WorkQueue().Enqueue(ctx, leased.Kind, leased.Key, nil, t0); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Resyncs().Flag(ctx, model.StreamResync{Origin: must(f.e.Self(ctx)), Receiver: "b", Stream: "repo:fed", Flagged: t0}); err != nil {
		t.Fatal(err)
	}
	status("FULL_SYNC_RUNNING")
	c := f.repos.values["fed"]
	c.Members[0].Enabled = repo.Flex(false)
	f.repos.values["fed"] = c
	status("DISABLED")
	c.Members[0].Enabled = repo.Flex(true)
	f.repos.values["fed"] = c
	p.ProtocolVersion = 0
	if err := f.s.Peers().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	status("UNSUPPORTED")
	p.ProtocolVersion = instance.ProtocolVersion
	p.Status = model.PeerOffline
	if err := f.s.Peers().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	status("N/A")
}

// Authority failures must not allow a local change to enter the stream or
// let a receiver supply a snapshot. Unknown streams and missing repositories
// remain harmless to the generic stream kind's optional policy hooks.
func TestUnitReceiverAuthorityFailures(t *testing.T) {
	f, ops := fullSyncOps(t)
	ctx := t.Context()
	if ok, err := f.k.AllowsStream(ctx, "access", caller(model.UseFederatedRepo), streamsync.Send); err != nil || ok {
		t.Fatalf("unknown stream: %v %v", ok, err)
	}
	if ok, err := f.k.AuthoritativeSnapshot(ctx, "access"); err != nil || ok {
		t.Fatalf("unknown snapshot: %v %v", ok, err)
	}
	if ok, err := f.k.AuthoritativeSnapshot(ctx, "repo:missing"); err != nil || ok {
		t.Fatalf("missing repository: %v %v", ok, err)
	}
	f.repos.getErr = kernel.ErrStore
	if err := f.k.ItemChanged(ctx, artifactsapi.ItemChange{Repository: "fed", Path: "x"}); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	if _, err := f.k.AuthoritativeSnapshot(ctx, "repo:fed"); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	if err := ops.Membership.CheckLocalUpdate(ctx, repo.Config{Key: "fed"}, f.repos.Get); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	f.repos.getErr = nil
	f.k.SelfBase = func(context.Context) (string, error) { return "", kernel.ErrStore }
	if _, err := f.k.AuthoritativeSnapshot(ctx, "repo:fed"); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	f.k.Store = faulty{store: f.s, members: kernel.ErrStore}
	if _, err := f.k.AllowsStream(ctx, "repo:fed", caller(model.UseFederatedRepo), streamsync.Receive); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	f.k.Store = f.s
	if ok, err := f.k.AllowsStream(ctx, "repo:fed", model.Peer{InstanceID: "outsider"}, streamsync.Receive); err != nil || ok {
		t.Fatalf("outsider: %v %v", ok, err)
	}
	if len(f.entries(t)) != 0 {
		t.Fatal("failed authority appended an event")
	}
}

func TestUnitReceiverConversionAndTrustRevocation(t *testing.T) {
	f, ops := fullSyncOps(t)
	ctx := t.Context()
	c := f.repos.values["fed"]
	c.Members[0].Mode = repo.ModeReceiver
	f.repos.values["fed"] = c
	converted, failed, err := ops.ConvertToLocal(ctx, []string{"fed"})
	if err != nil || len(converted) != 0 || !strings.Contains(failed["fed"], "receiver mode") {
		t.Fatalf("receiver converted: %v %v %v", converted, failed, err)
	}
	// The HTTP middleware may have read a valid caller immediately before
	// trust was cancelled. Configuration apply rechecks in its transaction.
	p := caller(model.UseFederatedRepo)
	if err := f.s.Peers().Delete(ctx, p.InstanceID); err != nil {
		t.Fatal(err)
	}
	receiver := federated.Receiver{Membership: ops.Membership, Repositories: f.repos}
	if err := receiver.Apply(ctx, p, "fed", sent(t, nil)); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("revoked source updated configuration: %v", err)
	}
}
