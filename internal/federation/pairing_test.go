package federation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// TestUnitPairingAdministrator admits a caller who is an administrator now,
// as 7.161.26's ServiceTrustResource admits ROLE_ADMIN alone.
func TestUnitPairingAdministrator(t *testing.T) {
	boom := errors.New("boom")
	admin := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	for _, c := range []struct {
		name   string
		ctx    context.Context
		access func(*accessapi.API)
		want   error
	}{
		{"admin", admin, nil, nil},
		{"anonymous", t.Context(), nil, kernel.ErrUnauthenticated},
		{"user", kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "u"}), nil, kernel.ErrForbidden},
		{"no contracts", admin, func(a *accessapi.API) { a.Authorizer = nil }, kernel.ErrStore},
		{"guard", admin, func(a *accessapi.API) {
			a.Projects = &accessapitest.Projects{GuardFunc: func(context.Context) error { return boom }}
		}, boom},
		{"removed", admin, func(a *accessapi.API) {
			a.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
				return kernel.Principal{}, kernel.ErrNotFound
			}}
		}, kernel.ErrBadCredentials},
		{"credential", admin, func(a *accessapi.API) {
			a.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
				return kernel.Principal{}, boom
			}}
		}, boom},
		{"permissions", admin, func(a *accessapi.API) {
			a.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return nil, boom
			}}
		}, boom},
		{"demoted", admin, func(a *accessapi.API) {
			a.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return &accessapitest.Decider{}, nil
			}}
		}, kernel.ErrForbidden},
		{"no decider", admin, func(a *accessapi.API) {
			a.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return nil, nil
			}}
		}, kernel.ErrForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			access := pushAccess()
			access.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return &accessapitest.Decider{Admin: true}, nil
			}}
			if c.access != nil {
				c.access(&access)
			}
			m, err := federation.New(federation.Deps{Store: inmem.New(), Access: access})
			if err != nil {
				t.Fatal(err)
			}
			svc, err := m.Pairing(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Authorize(c.ctx); !errors.Is(err, c.want) || c.want == nil && err != nil {
				t.Fatalf("Authorize = %v, want %v", err, c.want)
			}
		})
	}
}

// TestUnitPairingNoncePruning queues the pruning once, however often the
// module registers, and each run clears the expired nonces and queues the
// next.
func TestUnitPairingNoncePruning(t *testing.T) {
	store := inmem.New()
	if err := instance.EnsureInstance(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	expired := model.PairingNonce{NonceHash: []byte("expired"), UseCase: model.UseFederatedRepo, ExpiresAt: time.Now().Add(-time.Minute)}
	if err := store.Peers().AddNonce(t.Context(), expired); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := m.OpenPull(t.Context(), struct{ artifactsapi.PullSource }{}); err != nil {
			t.Fatal(err)
		}
		queue, err := work.New(store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.RegisterWork(t.Context(), queue); err != nil {
			t.Fatal(err)
		}
		if n, err := queue.RunDue(t.Context(), federation.PruneNoncesKind); err != nil || n != 1 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		if _, err := store.WorkQueue().Get(t.Context(), federation.PruneNoncesKind, "prune"); err != nil {
			t.Fatalf("the next pruning is not queued: %v", err)
		}
		m.Close()
	}
	if _, err := store.Peers().RedeemNonce(t.Context(), expired.NonceHash, "x", time.Now().Add(-2*time.Minute)); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("the expired nonce was not pruned: %v", err)
	}
}

// probeRemote answers every status read verified, at this instance's
// clock, and counts them.
type probeRemote struct{ reads int }

func (r *probeRemote) Status(context.Context, model.Peer) (pairing.PeerStatus, error) {
	r.reads++
	return pairing.PeerStatus{Time: time.Now()}, nil
}

// TestUnitPeerProbe registers the peer status reads once OpenLiveness has
// given the module a transport, queues them once however often the module
// registers, and each run reads every peer, records it seen and queues the
// next. The stale window is two intervals.
func TestUnitPeerProbe(t *testing.T) {
	store := inmem.New()
	if err := instance.EnsureInstance(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	peer := model.Peer{
		InstanceID: "jfrt@00000000-0000-4000-8000-000000000001", BaseURL: "https://b.example.com",
		ProtocolVersion: instance.ProtocolVersion, UseCases: []model.UseCase{model.UseFederatedRepo},
	}
	if err := store.Peers().Put(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	remote := &probeRemote{}
	for i := range 3 {
		m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := m.OpenPull(t.Context(), struct{ artifactsapi.PullSource }{}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			// The first module has no transport: its queue knows no reads.
			m.OpenLiveness(remote, []time.Duration{0, time.Second, 0}[i])
		}
		queue, err := work.New(store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.RegisterWork(t.Context(), queue); err != nil {
			t.Fatal(err)
		}
		svc, err := m.Pairing(nil)
		if err != nil {
			t.Fatal(err)
		}
		n, err := queue.RunDue(t.Context(), federation.PeerProbeKind)
		switch i {
		case 0:
			if !errors.Is(err, work.ErrNoHandler) || svc.StaleAfter != 0 {
				t.Fatalf("without a transport: RunDue = %d, %v; stale after %v", n, err, svc.StaleAfter)
			}
			m.Close()
			continue
		case 1:
			if svc.StaleAfter != 2*time.Second {
				t.Errorf("stale after %v, want two intervals", svc.StaleAfter)
			}
		case 2:
			if svc.StaleAfter != pairing.StaleAfterProbes(pairing.DefaultProbeInterval) {
				t.Errorf("default stale after %v", svc.StaleAfter)
			}
		}
		if err != nil || n != 1 || remote.reads != i {
			t.Fatalf("RunDue = %d, %v; %d reads", n, err, remote.reads)
		}
		if _, err := store.WorkQueue().Get(t.Context(), federation.PeerProbeKind, "probe"); err != nil {
			t.Fatalf("the next read is not queued: %v", err)
		}
		m.Close()
	}
	if p, err := store.Peers().Get(t.Context(), peer.InstanceID); err != nil || p.Status != model.PeerOnline || p.LastSeen.IsZero() {
		t.Fatalf("the read peer: %+v, %v", p, err)
	}
}
