package pairing_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// statusRemote answers a status read the way the network's node at the
// peer's base URL would (Describe, with its record of the caller), or
// fails with err.
type statusRemote struct {
	net   *network
	from  *node
	err   error
	calls atomic.Int32
}

func (r *statusRemote) Status(ctx context.Context, p model.Peer) (pairing.PeerStatus, error) {
	r.calls.Add(1)
	if r.err != nil {
		return pairing.PeerStatus{}, r.err
	}
	to := r.net.nodes[p.BaseURL]
	caller, err := to.store.Peers().Get(ctx, r.from.store.identity().InstanceID)
	if err != nil {
		return pairing.PeerStatus{}, &pairing.StatusError{Status: 401}
	}
	return to.svc.Describe(ctx, caller, "9.9.9")
}

// record is n's record of other.
func record(t *testing.T, n, other *node) model.Peer {
	t.Helper()
	return must(n.store.Peers().Get(t.Context(), other.store.identity().InstanceID))
}

// TestUnitProbe reads a paired peer's status and records what it answered:
// seen with its clock skew when it verifies, UNAUTHORIZED when it does not
// until it verifies again, and OFFLINE once unseen beyond the stale window.
func TestUnitProbe(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	r := &statusRemote{net: net, from: b}
	b.svc.Prober = r

	// A's clock is two seconds ahead of B's.
	*b.clock = t0.Add(3 * time.Minute)
	*a.clock = b.clock.Add(2 * time.Second)
	if err := b.svc.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := record(t, b, a)
	if p.Status != model.PeerOnline || !p.LastSeen.Equal(*b.clock) || p.ClockSkew != 2*time.Second {
		t.Fatalf("after a verified read: %+v", p)
	}

	// No answer leaves the last-seen time; past the window it is OFFLINE.
	seen := *b.clock
	for _, err := range []error{pairing.ErrUnreachable, &pairing.StatusError{Status: 502}} {
		r.err = err
		*b.clock = seen.Add(5 * time.Minute)
		must(0, b.svc.Probe(t.Context()))
		if p := record(t, b, a); p.Status != model.PeerOnline || !p.LastSeen.Equal(seen) {
			t.Fatalf("%v within the window: %+v", err, p)
		}
		*b.clock = seen.Add(11 * time.Minute)
		must(0, b.svc.Probe(t.Context()))
		if p := record(t, b, a); p.Status != model.PeerOffline || !p.LastSeen.Equal(seen) || p.ClockSkew != 2*time.Second {
			t.Fatalf("%v past the window: %+v", err, p)
		}
	}

	// An answer that does not verify is UNAUTHORIZED, and stays so while
	// the peer cannot be read at all.
	r.err = pairing.ErrUnverified
	must(0, b.svc.Probe(t.Context()))
	if p := record(t, b, a); p.Status != model.PeerUnauthorized || !p.LastSeen.Equal(seen) {
		t.Fatalf("unverified: %+v", p)
	}
	r.err = pairing.ErrUnreachable
	must(0, b.svc.Probe(t.Context()))
	if p := record(t, b, a); p.Status != model.PeerUnauthorized {
		t.Fatalf("unreachable after unverified: %+v", p)
	}
	r.err = nil
	*a.clock = *b.clock
	must(0, b.svc.Probe(t.Context()))
	if p := record(t, b, a); p.Status != model.PeerOnline || !p.LastSeen.Equal(*b.clock) || p.ClockSkew != 0 {
		t.Fatalf("verified again: %+v", p)
	}

	// Without a transport nothing is read.
	svc := *b.svc
	svc.Prober = nil
	calls := r.calls.Load()
	must(0, svc.Probe(t.Context()))
	if r.calls.Load() != calls {
		t.Error("a service without a prober read a peer")
	}
}

// TestUnitProbePulledPeer leaves a peer this instance cannot reach unread:
// its status follows its own reads of this instance.
func TestUnitProbePulledPeer(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	p := record(t, a, b)
	p.Delivery = model.DeliveryPulled
	must(0, a.store.Peers().Put(t.Context(), p))
	r := &statusRemote{net: net, from: a}
	a.svc.Prober = r

	*a.clock = t0.Add(11 * time.Minute)
	must(0, a.svc.Probe(t.Context()))
	if got := record(t, a, b); got.Status != model.PeerOffline || r.calls.Load() != 0 {
		t.Fatalf("an unread pulled peer: %+v, %d reads", got, r.calls.Load())
	}
	// B reads A's status: A records B as seen.
	st, err := a.svc.Describe(t.Context(), record(t, a, b), "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if got := record(t, a, b); got.Status != model.PeerOnline || !got.LastSeen.Equal(*a.clock) {
		t.Fatalf("a pulled peer that read this instance: %+v", got)
	}
	id := a.store.identity()
	if st.InstanceID != id.InstanceID || st.ProtocolVersion != pairing.Version(id.ProtocolVersion) ||
		st.Version != "9.9.9" || !st.Time.Equal(*a.clock) || !slices.Equal(st.UseCases, []model.UseCase{model.UseFederatedRepo}) {
		t.Fatalf("status %+v", st)
	}
}

// TestUnitDescribe answers a caller with no use case an empty list, and
// leaves a pushed-to caller's record alone: Probe keeps that.
func TestUnitDescribe(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	before := record(t, a, b)
	*a.clock = t0.Add(time.Hour)
	caller := before
	caller.UseCases = nil
	st, err := a.svc.Describe(t.Context(), caller, "")
	if err != nil || st.UseCases == nil || len(st.UseCases) != 0 {
		t.Fatalf("status %+v, %v", st, err)
	}
	if after := record(t, a, b); !after.LastSeen.Equal(before.LastSeen) || after.Status != before.Status {
		t.Fatalf("describing a pushed-to caller changed its record: %+v", after)
	}
}

func TestUnitProbeIntervals(t *testing.T) {
	if pairing.DefaultProbeInterval != 5*time.Minute || pairing.StaleAfterProbes(pairing.DefaultProbeInterval) != 10*time.Minute {
		t.Errorf("default interval %v, stale after %v", pairing.DefaultProbeInterval,
			pairing.StaleAfterProbes(pairing.DefaultProbeInterval))
	}
}

func TestUnitProbeStoreFaults(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	faulty := func(n *node, fail string) *pairing.Service {
		svc := *n.svc
		svc.Store = faultStore{n.store, map[string]bool{fail: true}}
		svc.Prober = &statusRemote{net: net, from: n}
		return &svc
	}
	*b.clock = t0.Add(time.Minute)
	if err := faulty(b, "List").Probe(t.Context()); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("list: %v", err)
	}
	if err := faulty(b, "SetLiveness").Probe(t.Context()); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("record: %v", err)
	}
	// An unchanged record is not written, so a failing write goes unseen.
	*b.clock = t0
	*a.clock = t0
	if err := faulty(b, "SetLiveness").Probe(t.Context()); err != nil {
		t.Errorf("an unchanged record was written: %v", err)
	}
	p := record(t, a, b)
	p.Delivery = model.DeliveryPulled
	if _, err := faulty(a, "SetLiveness").Describe(t.Context(), p, ""); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("describe: %v", err)
	}
}
