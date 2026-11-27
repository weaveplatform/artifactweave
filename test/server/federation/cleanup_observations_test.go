package federation_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	artmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	fedmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	platformmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type store struct {
	*fedmem.Store
	platform *platformmem.Store
}

func (s store) Configs() configdoc.ConfigStore   { return s.platform.Configs() }
func (s store) Instance() instance.InstanceStore { return s.platform.Instance() }
func (s store) Outbox() outbox.OutboxStore       { return s.platform.Outbox() }

type cleanupWorldStore interface {
	cleanup.Store
	CleanupIntents() port.CleanupIntentStore
	RequestNonces() port.RequestNonceStore
	Outbox() outbox.OutboxStore
}

type remoteFunc func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error)

func (f remoteFunc) Downloads(ctx context.Context, p model.Peer, key string, request cleanup.Request) (cleanup.Answer, error) {
	return f(ctx, p, key, request)
}

type world struct {
	service   *cleanup.Service
	store     cleanupWorldStore
	artifacts artport.Store
	versions  []api.CleanupVersion
	firstItem int64
}

func newWorld(t *testing.T, members, versions int) *world {
	t.Helper()
	return newWorldWithKey(t, "packages", members, versions)
}

func newWorldWithKey(t *testing.T, key string, members, versions int) *world {
	t.Helper()
	db := memtx.New()
	st := store{Store: fedmem.New(db), platform: platformmem.New(db)}
	artifacts := artmem.New(db)
	return newWorldOver(t, key, members, versions, st, artifacts)
}

func newWorldOver(t *testing.T, key string, members, versions int, st cleanupWorldStore, artifacts artport.Store) *world {
	t.Helper()
	storekit.Must(t, instance.EnsureInstance(t.Context(), st))
	cfg := *repo.Defaults(&repo.Config{Key: key, Class: repo.Federated, PackageType: repo.NPM, Members: []repo.Member{{URL: repo.SelfMemberURL(key), Enabled: repo.Flex(true)}}})
	var resolved []model.FederationMember
	for n := range members {
		id := fmt.Sprintf("peer-%02d", n)
		base := fmt.Sprintf("https://peer-%02d.example/artifactory", n)
		storekit.Must(t, st.Peers().Put(t.Context(), model.Peer{InstanceID: id, BaseURL: base, ProtocolVersion: 1, UseCases: []model.UseCase{model.UseFederatedRepo}}))
		cfg.Members = append(cfg.Members, repo.Member{URL: base + "/remote", Enabled: repo.Flex(true)})
		resolved = append(resolved, model.FederationMember{PeerInstanceID: id, BaseURL: base, RemoteRepoKey: "remote", Enabled: true})
	}
	storekit.Must(t, artifacts.Repos().Create(t.Context(), cfg))
	storekit.Must(t, st.Federation().SetMembers(t.Context(), cfg.Key, resolved))
	w := &world{store: st, artifacts: artifacts}
	w.service = &cleanup.Service{Store: st, Metadata: pkgindex.NewCleanupMetadata(artifacts), Exclusions: artifacts.(interface {
		CleanupExclusions() artifactsapi.CleanupExclusions
	}).CleanupExclusions(), BaseURL: "https://self.example"}
	p := storekit.Get[artmodel.Package](t)(artifacts.Packages().Ensure(t.Context(), artmodel.Package{RepoKey: cfg.Key, Type: repo.NPM, Name: "widget"}))
	for n := range versions {
		name := fmt.Sprintf("1.0.%d", n)
		v := storekit.Get[artmodel.PackageVersion](
			t,
		)(
			artifacts.Packages().PutVersion(t.Context(), artmodel.PackageVersion{PackageID: p.ID, Version: name, Created: storekit.At(1)}),
		)
		item := storekit.Get[artmodel.Item](t)(artifacts.Items().Put(t.Context(), artmodel.Item{RepoKey: cfg.Key, Name: name + ".tgz", Created: storekit.At(1)}))
		storekit.Must(t, artifacts.Packages().LinkFile(t.Context(), artmodel.VersionFile{VersionID: v.ID, ItemID: item.ID}))
		w.versions = append(w.versions, api.CleanupVersion{Package: p.Name, Version: name})
		if n == 0 {
			w.firstItem = item.ID
		}
	}
	w.service.Remote = remoteFunc(func(_ context.Context, p model.Peer, _ string, request cleanup.Request) (cleanup.Answer, error) {
		return answer(p, request), nil
	})
	return w
}

func answer(p model.Peer, request cleanup.Request) cleanup.Answer {
	out := cleanup.Answer{
		Instance:    p.InstanceID,
		Incarnation: p.InstanceID + "-lifetime",
		Generation:  "config-1",
		Grant:       "remote-grant-1",
		Challenge:   request.Challenge,
		PackageType: request.PackageType,
	}
	for n, version := range request.Versions {
		out.Versions = append(
			out.Versions,
			api.CleanupDownload{CleanupVersion: version, ID: int64(n + 1), Revision: "download-1", AdmissionRevision: "/", Created: storekit.Want(storekit.At(1))},
		)
	}
	return out
}

func (w *world) collect(t *testing.T) api.CleanupEvidence {
	t.Helper()
	scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(t.Context(), "packages"))
	return storekit.Get[api.CleanupEvidence](t)(w.service.Collect(t.Context(), scope, w.versions))
}

func guard(w *world, ctx context.Context, evidence api.CleanupEvidence) error {
	return w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Guard(ctx, evidence) })
}

func TestUnitCleanupMaximumMembershipAndBatch(t *testing.T) {
	w := newWorld(t, repo.MaxMembers-1, api.CleanupBatchSize)
	var active, peak, calls atomic.Int32
	w.service.Remote = remoteFunc(func(ctx context.Context, p model.Peer, key string, request cleanup.Request) (cleanup.Answer, error) {
		if w.store.InTx(ctx) {
			return cleanup.Answer{}, errors.New("network call retained a transaction")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > cleanup.AttemptTimeout {
			return cleanup.Answer{}, errors.New("member call is not deadline bounded")
		}
		if len(request.Versions) != api.CleanupBatchSize || key != "remote" {
			return cleanup.Answer{}, errors.New("logical versions were not batched")
		}
		n := active.Add(1)
		defer active.Add(-1)
		for current := peak.Load(); n > current; current = peak.Load() {
			if peak.CompareAndSwap(current, n) {
				break
			}
		}
		calls.Add(1)
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return cleanup.Answer{}, ctx.Err()
		case <-timer.C:
		}
		return answer(p, request), nil
	})
	started := time.Now()
	evidence := w.collect(t)
	if time.Since(started) >= cleanup.Window || calls.Load() != int32(repo.MaxMembers-1) || peak.Load() > cleanup.Concurrency || peak.Load() < 2 {
		t.Fatalf("unbounded or sequential collection: calls=%d peak=%d elapsed=%s", calls.Load(), peak.Load(), time.Since(started))
	}
	if len(evidence.Pins) != repo.MaxMembers*api.CleanupBatchSize {
		t.Fatal("member counters were discarded", len(evidence.Pins))
	}
	if api.CleanupMaximumClockAllowance != corepeer.Skew {
		t.Fatal("cleanup clock allowance drifted from signature verification")
	}
	for _, pin := range evidence.Pins {
		if pin.Member != evidence.Scope.Self && pin.ClockAllowance != corepeer.Skew {
			t.Fatal("remote clock uncertainty was discarded")
		}
	}
	storekit.Must(t, guard(w, t.Context(), evidence))
}

func TestUnitCleanupEvidenceInvalidation(t *testing.T) {
	for _, name := range []string{"replacement", "failure_then_recovery", "item_only_download", "membership_ABA", "missing_remote_version", "changed_challenge", "receiver", "relationship_direction"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			evidence := w.collect(t)
			switch name {
			case "replacement":
				peer := storekit.Get[model.Peer](t)(w.store.Peers().Get(t.Context(), "peer-00"))
				storekit.Must(t, w.store.Peers().Put(t.Context(), peer))
			case "failure_then_recovery":
				w.service.Remote = remoteFunc(func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error) {
					return cleanup.Answer{}, errors.New("unreachable")
				})
				_, err := w.service.Collect(t.Context(), evidence.Scope, w.versions)
				storekit.WantErr(t, err, cleanup.ErrUnavailable)
				w.service.Remote = remoteFunc(func(_ context.Context, p model.Peer, _ string, r cleanup.Request) (cleanup.Answer, error) {
					return answer(p, r), nil
				})
				fresh := w.collect(t)
				storekit.Must(t, guard(w, t.Context(), fresh))
			case "item_only_download":
				storekit.Must(t, w.artifacts.Items().RecordDownload(t.Context(), w.firstItem, artport.Download{At: time.Now()}))
			case "membership_ABA":
				cfg := storekit.Get[repo.Config](t)(w.artifacts.Repos().Get(t.Context(), "packages"))
				cfg.Members[0].Enabled = repo.Flex(false)
				storekit.Must(t, w.artifacts.Repos().Update(t.Context(), cfg))
				cfg.Members[0].Enabled = repo.Flex(true)
				storekit.Must(t, w.artifacts.Repos().Update(t.Context(), cfg))
			case "missing_remote_version", "changed_challenge":
				w.service.Remote = remoteFunc(func(_ context.Context, p model.Peer, _ string, r cleanup.Request) (cleanup.Answer, error) {
					out := answer(p, r)
					if name == "missing_remote_version" {
						out.Versions = nil
					} else {
						out.Challenge = "old-request-challenge"
					}
					return out, nil
				})
				_, err := w.service.Collect(t.Context(), evidence.Scope, w.versions)
				storekit.WantErr(t, err, cleanup.ErrUnavailable)
			case "relationship_direction":
				peer := storekit.Get[model.Peer](t)(w.store.Peers().Get(t.Context(), "peer-00"))
				peer.UseCases = []model.UseCase{model.UseFederatedRepoRead}
				peer.ReceivesFederationOnly = true
				storekit.Must(t, w.store.Peers().Put(t.Context(), peer))
			case "receiver":
				cfg := storekit.Get[repo.Config](t)(w.artifacts.Repos().Get(t.Context(), "packages"))
				cfg.Members[0].Mode = repo.ModeReceiver
				storekit.Must(t, w.artifacts.Repos().Update(t.Context(), cfg))
				scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(t.Context(), "packages"))
				if !scope.Receiver {
					t.Fatal("receiver execution decision was omitted")
				}
			}
			storekit.WantErr(t, guard(w, t.Context(), evidence), kernel.ErrConflict)
		})
	}
}

type waitingPeers struct {
	port.PeerStore
	port.PeerRevisions
	wait func()
}

func (p waitingPeers) GuardRevision(ctx context.Context, id string, revision port.PeerRevision) error {
	p.wait()
	return p.PeerRevisions.GuardRevision(ctx, id, revision)
}

type waitingStore struct {
	cleanup.Store
	peers port.PeerStore
}

func (s waitingStore) Peers() port.PeerStore { return s.peers }

func TestUnitCleanupExpiryAfterPublicationWait(t *testing.T) {
	w := newWorld(t, 1, 1)
	var elapsed atomic.Int64
	base := time.Now()
	w.service.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	evidence := w.collect(t)
	peers := w.store.Peers()
	w.service.Store = waitingStore{
		Store: w.store,
		peers: waitingPeers{PeerStore: peers, PeerRevisions: peers.(port.PeerRevisions), wait: func() { elapsed.Store(int64(cleanup.Window)) }},
	}
	storekit.WantErr(t, guard(w, t.Context(), evidence), kernel.ErrConflict)
}

func TestUnitCleanupSelfOnlyAndReadAuthorization(t *testing.T) {
	w := newWorld(t, 0, 1)
	evidence := w.collect(t)
	if len(evidence.Pins) != 1 || len(evidence.Scope.Members) != 0 {
		t.Fatal("self-only observation invented remote members")
	}
	storekit.Must(t, guard(w, t.Context(), evidence))
	w = newWorld(t, 1, 1)
	caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(t.Context(), "peer-00"))
	request := cleanup.Request{SourceRepository: "remote", PackageType: "npm", Challenge: "fresh-unique-challenge", Versions: w.versions}
	response := storekit.Get[cleanup.Answer](t)(w.service.Downloads(t.Context(), caller, "packages", request))
	if len(response.Versions) != 1 || response.Challenge != request.Challenge {
		t.Fatal("current member did not receive bound read evidence")
	}
	request.SourceRepository = "foreign"
	_, err := w.service.Downloads(t.Context(), caller, "packages", request)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	request.SourceRepository = "remote"
	request.Versions = append(request.Versions, request.Versions[0])
	_, err = w.service.Downloads(t.Context(), caller, "packages", request)
	storekit.WantErr(t, err, kernel.ErrInvalid)
}

func TestUnitCleanupLogicalVersionBatchBoundary(t *testing.T) {
	w := newWorld(t, repo.MaxMembers-1, api.CleanupBatchSize+1)
	var calls atomic.Int32
	w.service.Remote = remoteFunc(func(_ context.Context, p model.Peer, _ string, r cleanup.Request) (cleanup.Answer, error) {
		calls.Add(1)
		return answer(p, r), nil
	})
	scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(t.Context(), "packages"))
	proof := storekit.Get[api.CleanupEvidence](t)(w.service.Collect(t.Context(), scope, w.versions[:api.CleanupBatchSize]))
	if len(proof.Pins) != repo.MaxMembers*api.CleanupBatchSize {
		t.Fatal("64 logical versions did not include every member", len(proof.Pins))
	}
	before := calls.Load()
	_, err := w.service.Collect(t.Context(), scope, w.versions)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	if calls.Load() != before {
		t.Fatal("65-version input performed remote I/O", calls.Load(), before)
	}
	storekit.Must(t, guard(w, t.Context(), proof))
}
