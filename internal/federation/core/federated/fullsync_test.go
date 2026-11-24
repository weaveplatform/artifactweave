package federated_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// fullSyncOps is the stream fixture's operations, with its engine and a
// local repository beside "fed".
func fullSyncOps(t *testing.T) (*streamFixture, federated.Operations) {
	t.Helper()
	f := newStreamFixture(t)
	f.repos.values["local"] = repo.Config{Key: "local", Class: repo.Local}
	ms := membership(f.s)
	ms.Dropped = func(ctx context.Context, key, peer string) error {
		return f.e.Forget(ctx, peer, model.RepositoryStream(key))
	}
	return f, federated.Operations{
		Membership: ms, Repositories: f.repos,
		Authorize: func(context.Context) error { return nil },
		Streams:   func() *streamsync.Engine { return f.e },
	}
}

// queuedKeys reports which of keys have a kind item queued.
func queuedKeys(t *testing.T, s store, kind string, keys ...string) []string {
	t.Helper()
	var out []string
	for _, k := range keys {
		_, err := s.WorkQueue().Get(t.Context(), kind, k)
		switch {
		case err == nil:
			out = append(out, k)
		case !errors.Is(err, kernel.ErrNotFound):
			t.Fatal(err)
		}
	}
	return out
}

var requestKeys = []string{"b/fed/normal", "b/fed/forced", "c/fed/normal", "c/fed/forced"}

func TestUnitOperationsFullSync(t *testing.T) {
	f, ops := fullSyncOps(t)
	if err := ops.FullSync(t.Context(), "fed", "", false); err != nil {
		t.Fatal(err)
	}
	if got := queuedKeys(t, f.s, federated.FullSyncRequestKind, requestKeys...); !slices.Equal(got, []string{"b/fed/normal", "c/fed/normal"}) {
		t.Fatalf("asked %v, want every member", got)
	}
	// The members resynchronise and record the runs; this instance runs
	// none.
	if runs, _, err := f.s.Federation().FullSyncs(t.Context(), "fed", kernel.Page{Limit: 10}); err != nil || len(runs) != 0 {
		t.Fatalf("runs here %+v, %v", runs, err)
	}

	// ?mirror names one member by its base URL or member URL.
	for _, mirror := range []string{"https://c.example", cBase + "/fed"} {
		if err := ops.FullSync(t.Context(), "fed", mirror, true); err != nil {
			t.Fatalf("%s: %v", mirror, err)
		}
	}
	if got := queuedKeys(t, f.s, federated.FullSyncRequestKind, requestKeys...); !slices.Contains(got, "c/fed/forced") ||
		slices.Contains(got, "b/fed/forced") {
		t.Fatalf("asked %v, want c forced only", got)
	}
	refusal(t, ops.FullSync(t.Context(), "fed", "https://nobody.example", false),
		"Federated member 'https://nobody.example' is not a member of repository 'fed'")
	if err := ops.FullSync(t.Context(), "missing", "", false); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatalf("missing: %v", err)
	}
	refusal(t, ops.FullSync(t.Context(), "local", "", false), "Repository 'local' is not a federated repository")

	denied := ops
	denied.Authorize = func(context.Context) error { return kernel.ErrForbidden }
	if err := denied.FullSync(t.Context(), "fed", "", false); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("non-admin: %v", err)
	}
}

func TestUnitResyncerSend(t *testing.T) {
	f, _ := fullSyncOps(t)
	rem := &remote{}
	r := federated.Resyncer{Membership: membership(f.s), Remote: rem}
	item := func(k string) work.WorkItem { return work.WorkItem{Kind: federated.FullSyncRequestKind, Key: k} }
	for _, k := range []string{"b/fed/normal", "c/fed/forced"} {
		if err := r.Send(t.Context(), item(k)); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if !slices.Equal(rem.asked, []string{"b repo:fed false", "c repo:fed true"}) {
		t.Fatalf("asked %v", rem.asked)
	}
	// A repository or member gone, or a member without a peer record,
	// completes unasked.
	if err := f.s.Federation().SetMembers(t.Context(), "gone", []model.FederationMember{
		{PeerInstanceID: "nobody", BaseURL: "https://n.example/artifactory", RemoteRepoKey: "gone", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"b/missing/normal", "r/fed/normal", "nobody/gone/normal"} {
		if err := r.Send(t.Context(), item(k)); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if len(rem.asked) != 2 {
		t.Fatalf("asked %v", rem.asked)
	}
	for _, k := range []string{"b/fed", "b/fed/later", "/fed/normal", "b//normal", "b/fed/normal/x"} {
		if err := r.Send(t.Context(), item(k)); !errors.Is(err, work.ErrPermanent) {
			t.Fatalf("%q: %v", k, err)
		}
	}
	rem.err = &federated.RemoteRefusal{Status: http.StatusForbidden, Message: "no"}
	if err := r.Send(t.Context(), item("b/fed/normal")); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("refusal: %v", err)
	}
	rem.err = fmt.Errorf("%w: down", federated.ErrUnavailable)
	if err := r.Send(t.Context(), item("b/fed/normal")); !errors.Is(err, work.ErrUnsettled) {
		t.Fatalf("unavailable: %v", err)
	}
	// A member's own failure is not a refusal: the request is asked again.
	rem.err = &federated.RemoteRefusal{Status: http.StatusInternalServerError, Message: "store"}
	if err := r.Send(t.Context(), item("b/fed/normal")); !errors.Is(err, work.ErrUnsettled) {
		t.Fatalf("member failure: %v", err)
	}
}

func TestUnitOperationsRecovery(t *testing.T) {
	f, ops := fullSyncOps(t)
	if err := ops.Recovery(t.Context(), "fed", false); err != nil {
		t.Fatal(err)
	}
	if err := ops.Reconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := queuedKeys(
		t,
		f.s,
		streamsync.RecoverKind,
		"repo:fed",
		"force repo:fed",
		"repo:local",
		"force repo:local",
	); !slices.Equal(
		got,
		[]string{"repo:fed", "force repo:fed"},
	) {
		t.Fatalf("queued %v", got)
	}
	if err := ops.Recovery(t.Context(), "missing", false); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatalf("missing: %v", err)
	}
	refusal(t, ops.Recovery(t.Context(), "local", false), "Repository 'local' is not a federated repository")
	f.repos.listErr = kernel.ErrStore
	if err := ops.Recovery(t.Context(), "", false); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("list failure: %v", err)
	}
	f.repos.listErr = nil
	without := ops
	without.Streams = nil
	if err := without.Recovery(t.Context(), "", false); !errors.Is(err, federated.ErrNoStreams) {
		t.Fatalf("no engine: %v", err)
	}
}

func TestUnitOperationsRemoveMember(t *testing.T) {
	f, ops := fullSyncOps(t)
	if err := f.s.Outbox().PutCursor(t.Context(), outbox.PeerCursor{PeerInstanceID: "c", Stream: "repo:fed", AckedSeq: 3}); err != nil {
		t.Fatal(err)
	}
	n, err := ops.RemoveMember(t.Context(), "https://c.example")
	if err != nil || n != 1 {
		t.Fatalf("remove: %d, %v", n, err)
	}
	c := f.repos.values["fed"]
	for _, m := range c.Members {
		if m.URL == cBase+"/fed" {
			t.Fatalf("members still list C: %v", urls(c))
		}
	}
	for _, m := range f.s.members(t, "fed") {
		if m.PeerInstanceID == "c" {
			t.Fatalf("membership still lists C: %+v", m)
		}
	}
	if _, err := f.s.Outbox().Cursor(t.Context(), "c", "repo:fed"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("C's cursor: %v, want gone", err)
	}
	// The remaining member is sent the change.
	if got := queuedKeys(t, f.s, federated.ConfigSyncKind, "b/fed"); len(got) != 1 {
		t.Fatalf("configSync queued %v", got)
	}
	// By member URL too.
	if n, err := ops.RemoveMember(t.Context(), bBase+"/fed"); err != nil || n != 1 {
		t.Fatalf("remove by member URL: %d, %v", n, err)
	}
	refusal(t, nilCount(ops.RemoveMember(t.Context(), " ")), "The base URL of the member to remove is required")
	refusal(t, nilCount(ops.RemoveMember(t.Context(), "https://c.example")),
		"Federated member 'https://c.example' is not a member of any federated repository")
	f.repos.updErr = kernel.ErrStore
	if err := f.s.Federation().SetMembers(t.Context(), "fed", []model.FederationMember{
		{PeerInstanceID: "c", BaseURL: cBase, RemoteRepoKey: "fed", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.RemoveMember(t.Context(), "https://c.example"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("update failure: %v", err)
	}
}

func nilCount(_ int, err error) error { return err }

func TestUnitOperationsPrioritize(t *testing.T) {
	f, ops := fullSyncOps(t)
	priority := federated.Priority(f.s.Settings())
	if err := ops.Prioritize(t.Context(), "fed", "high"); err != nil {
		t.Fatal(err)
	}
	if got := priority(t.Context(), "repo:fed"); got != 1 {
		t.Fatalf("fed priority %d, want 1", got)
	}
	for _, s := range []string{"repo:local", "test:x"} {
		if got := priority(t.Context(), s); got != 0 {
			t.Fatalf("%s priority %d, want 0", s, got)
		}
	}
	if err := ops.Prioritize(t.Context(), "fed", "Standard"); err != nil {
		t.Fatal(err)
	}
	if got := priority(t.Context(), "repo:fed"); got != 0 {
		t.Fatalf("fed priority %d after standard, want 0", got)
	}
	refusal(t, ops.Prioritize(t.Context(), "fed", "urgent"), "priorityLevel must be HIGH or STANDARD, not 'urgent'")
	if err := ops.Prioritize(t.Context(), "missing", "high"); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatalf("missing: %v", err)
	}

	// At most ten at once; a repository deleted since holds no place.
	high := []string{"deleted"}
	for i := range federated.MaxHighPriority - 1 {
		k := fmt.Sprintf("fed%d", i)
		f.repos.values[k] = repo.Config{Key: k, Class: repo.Federated}
		high = append(high, k)
	}
	if err := f.s.Settings().Put(t.Context(), settings.SettingFederationPriority, high); err != nil {
		t.Fatal(err)
	}
	if err := ops.Prioritize(t.Context(), "fed", "HIGH"); err != nil {
		t.Fatalf("tenth: %v", err)
	}
	f.repos.values["fed9"] = repo.Config{Key: "fed9", Class: repo.Federated}
	refusal(t, ops.Prioritize(t.Context(), "fed9", "high"),
		"At most 10 federated repositories can have high priority at once")
	got, err := federated.HighPriority(t.Context(), f.s.Settings())
	if err != nil || len(got) != federated.MaxHighPriority || slices.Contains(got, "deleted") {
		t.Fatalf("high %v, %v", got, err)
	}
}

func TestUnitOperationsPropagateCreatedTime(t *testing.T) {
	f, ops := fullSyncOps(t)
	for _, on := range []bool{true, false} {
		if err := ops.SetPropagateCreatedTime(t.Context(), on); err != nil {
			t.Fatal(err)
		}
		if got, err := federated.PropagatesCreatedTime(t.Context(), f.s.Settings()); err != nil || got != on {
			t.Fatalf("set %v: %v, %v", on, got, err)
		}
	}
	denied := ops
	denied.Authorize = func(context.Context) error { return kernel.ErrForbidden }
	if err := denied.SetPropagateCreatedTime(t.Context(), true); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("non-admin: %v", err)
	}
	if err := denied.Administrator(t.Context()); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("administrator: %v", err)
	}
}

// Each peer that stops being a member, by an update or the repository's
// removal, is dropped once.
func TestUnitMembershipDropped(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	var dropped []string
	var fail error
	ms.Dropped = func(_ context.Context, key, peer string) error {
		dropped = append(dropped, key+" "+peer)
		return fail
	}
	reconcile := func(c repo.Config) error {
		t.Helper()
		if err := ms.Admit(t.Context(), &c); err != nil {
			t.Fatal(err)
		}
		return ms.Reconcile(t.Context(), c, false)
	}
	if err := reconcile(fed("fed", bBase+"/fed", cBase+"/fed")); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(fed("fed", bBase+"/fed")); err != nil {
		t.Fatal(err)
	}
	if err := ms.Clear(t.Context(), "fed"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dropped, []string{"fed c", "fed b"}) {
		t.Fatalf("dropped %v", dropped)
	}
	if err := reconcile(fed("fed", bBase+"/fed")); err != nil {
		t.Fatal(err)
	}
	fail = kernel.ErrStore
	if err := reconcile(fed("fed")); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("update: %v", err)
	}
	if err := reconcile(fed("fed", bBase+"/fed")); err != nil {
		t.Fatal(err)
	}
	if err := ms.Clear(t.Context(), "fed"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("clear: %v", err)
	}
}
