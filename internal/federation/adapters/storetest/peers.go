package storetest

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// runPeers covers the peer protocol's records (decision 0021), federated
// repository membership (0022) and platform federation (0025).
func runPeers(t *testing.T, newStore Factory) {
	t.Run("Peers", func(t *testing.T) { testPeers(t, newStore(t)) })
	t.Run("PairingNonces", func(t *testing.T) { testPairingNonces(t, newStore(t)) })
	t.Run("FederationMembers", func(t *testing.T) { testFederationMembers(t, newStore(t)) })
	t.Run("FullSyncs", func(t *testing.T) { testFullSyncs(t, newStore(t)) })
	t.Run("AccessTargets", func(t *testing.T) { testAccessTargets(t, newStore(t)) })
	t.Run("PlatformFederations", func(t *testing.T) { testPlatformFederations(t, newStore(t)) })
	t.Run("Topologies", func(t *testing.T) { testTopologies(t, newStore(t)) })
}

// cancelled returns a context that is already cancelled: every store call
// made with it must fail rather than read or write.
func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// failsCancelled asserts that each call fails on a cancelled context.
func failsCancelled(t *testing.T, calls map[string]func(context.Context) error) {
	t.Helper()
	ctx := cancelled()
	for name, call := range calls {
		if call(ctx) == nil {
			t.Errorf("%s succeeded on a cancelled context", name)
		}
	}
}

// peerID is the instance id of fixture instance i.
func peerID(i int) string {
	return fmt.Sprintf("jfrt@%08x-0000-4000-8000-%012x", i, i)
}

// peerKey is fixture instance i's Ed25519 key pair.
func peerKey(i int) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	//nolint:gosec // G115: fixture numbers are small, so the byte is the number
	seed[0] = byte(i)
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv //nolint:forcetypeassert // ed25519 public keys are this type
}

// peer builds fixture peer i.
func peer(i int, uses ...model.UseCase) model.Peer {
	pub, _ := peerKey(i)
	return model.Peer{
		InstanceID:      peerID(i),
		BaseURL:         fmt.Sprintf("https://edge-%d.example.com", i),
		PublicKey:       pub,
		ProtocolVersion: instance.ProtocolVersion,
		UseCases:        uses,
		Status:          model.PeerOnline,
		LastSeen:        storekit.At(i),
		DisplayName:     fmt.Sprintf("Edge %d", i),
		CityName:        "Cardiff",
		CountryCode:     "GB",
		EdgeRole:        i%2 == 0,
	}
}

func testPeers(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Peers()
	_, err := ps.Get(ctx, peerID(1))
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ps.Delete(ctx, peerID(1)), kernel.ErrNotFound)

	p1 := peer(1, model.UseFederation, model.UseDistribution, model.UseFederation)
	p2 := peer(2, model.UseDistribution)
	p3 := peer(3)
	for _, p := range []model.Peer{p3, p1, p2} {
		storekit.Must(t, ps.Put(ctx, p))
	}
	// Use cases are a set, returned sorted.
	wantP1 := p1
	wantP1.UseCases = []model.UseCase{model.UseDistribution, model.UseFederation}
	got := storekit.Get[model.Peer](t)(ps.Get(ctx, p1.InstanceID))
	storekit.Same(t, "Get", wantP1, got)

	// Neither the caller's input nor a returned value aliases the store.
	p1.UseCases[0] = model.UseAccessFederation
	got.UseCases[0] = model.UseAccessFederation
	got.PublicKey[0]++
	storekit.Same(
		t,
		"Get after mutating copies",
		wantP1,
		storekit.Get[model.Peer](t)(ps.Get(ctx, p1.InstanceID)),
	)

	all := storekit.Get[[]model.Peer](t)(ps.List(ctx, port.PeerFilter{}))
	storekit.Same(t, "List", []model.Peer{wantP1, p2, p3}, all)
	dist := storekit.Get[[]model.Peer](
		t,
	)(
		ps.List(ctx, port.PeerFilter{UseCase: model.UseDistribution}),
	)
	storekit.Same(t, "List distribution", []model.Peer{wantP1, p2}, dist)
	none := storekit.Get[[]model.Peer](
		t,
	)(
		ps.List(ctx, port.PeerFilter{UseCase: model.UsePlatformFederation}),
	)
	storekit.Same(t, "List platform-federation", []model.Peer(nil), none)

	// Put replaces, use cases included.
	moved := peer(1, model.UseAccessFederation)
	moved.BaseURL = "https://moved.example.com"
	moved.Status = model.PeerOffline
	moved.LastSeen = storekit.At(30)
	storekit.Must(t, ps.Put(ctx, moved))
	storekit.Same(
		t,
		"Get after replace",
		moved,
		storekit.Get[model.Peer](t)(ps.Get(ctx, moved.InstanceID)),
	)
	dist = storekit.Get[[]model.Peer](
		t,
	)(
		ps.List(ctx, port.PeerFilter{UseCase: model.UseDistribution}),
	)
	storekit.Same(t, "List distribution after replace", []model.Peer{p2}, dist)

	storekit.Must(t, ps.Delete(ctx, p2.InstanceID))
	_, err = ps.Get(ctx, p2.InstanceID)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	dist = storekit.Get[[]model.Peer](
		t,
	)(
		ps.List(ctx, port.PeerFilter{UseCase: model.UseDistribution}),
	)
	storekit.Same(t, "List distribution after delete", []model.Peer(nil), dist)
	// A peer put again after deletion starts with only its new use cases.
	again := peer(2, model.UseFederation)
	storekit.Must(t, ps.Put(ctx, again))
	storekit.Same(
		t,
		"Get after re-adding",
		again,
		storekit.Get[model.Peer](t)(ps.Get(ctx, again.InstanceID)),
	)
	//nolint:wrapcheck // each case checks only that the call fails
	failsCancelled(t, map[string]func(context.Context) error{
		"Put":    func(ctx context.Context) error { return ps.Put(ctx, again) },
		"Get":    func(ctx context.Context) error { _, err := ps.Get(ctx, again.InstanceID); return err },
		"List":   func(ctx context.Context) error { _, err := ps.List(ctx, port.PeerFilter{}); return err },
		"Delete": func(ctx context.Context) error { return ps.Delete(ctx, again.InstanceID) },
	})
}

// nonce builds a pairing nonce with hash byte i expiring at minute exp.
func nonce(i, exp int) model.PairingNonce {
	return model.PairingNonce{
		//nolint:gosec // G115: fixture numbers are small, so the byte is the number
		NonceHash: []byte{0xaa, byte(i), 0x00, 0xff},
		UseCase:   model.UseFederation,
		ExpiresAt: storekit.At(exp),
	}
}

func testPairingNonces(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Peers()
	n1, n2, n3 := nonce(1, 10), nonce(2, 10), nonce(3, 30)
	for _, n := range []model.PairingNonce{n1, n2, n3} {
		storekit.Must(t, ps.AddNonce(ctx, n))
	}
	storekit.WantErr(t, ps.AddNonce(ctx, n1), kernel.ErrConflict)

	_, err := ps.RedeemNonce(ctx, []byte{0x01}, storekit.At(0))
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// Expired: after the expiry, and at the very instant of it.
	_, err = ps.RedeemNonce(ctx, n1.NonceHash, storekit.At(11))
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = ps.RedeemNonce(ctx, n1.NonceHash, storekit.At(10))
	storekit.WantErr(t, err, kernel.ErrNotFound)

	got := storekit.Get[model.PairingNonce](t)(ps.RedeemNonce(ctx, n2.NonceHash, storekit.At(5)))
	redeemed := n2
	redeemed.RedeemedAt = storekit.At(5)
	storekit.Same(t, "RedeemNonce", redeemed, got)
	// Single use.
	_, err = ps.RedeemNonce(ctx, n2.NonceHash, storekit.At(6))
	storekit.WantErr(t, err, kernel.ErrConflict)

	pruned, err := ps.PruneNonces(ctx, storekit.At(20))
	storekit.Must(t, err)
	if pruned != 2 {
		t.Fatalf("PruneNonces = %d, want 2", pruned)
	}
	_, err = ps.RedeemNonce(ctx, n2.NonceHash, storekit.At(6))
	storekit.WantErr(t, err, kernel.ErrNotFound)
	got = storekit.Get[model.PairingNonce](t)(ps.RedeemNonce(ctx, n3.NonceHash, storekit.At(25)))
	if !got.RedeemedAt.Equal(storekit.Want(storekit.At(25))) {
		t.Fatalf("RedeemedAt = %v", got.RedeemedAt)
	}
}

func testFederationMembers(t *testing.T, s port.Store) {
	ctx := context.Background()
	fs := s.Federation()
	member := func(repoKey string, peer int, enabled bool) model.FederationMember {
		return model.FederationMember{
			RepoKey:        repoKey,
			PeerInstanceID: peerID(peer),
			BaseURL: fmt.Sprintf(
				"https://edge-%d.example.com",
				peer,
			),
			RemoteRepoKey: repoKey + "-remote",
			Enabled:       enabled,
		}
	}
	storekit.Same(t, "Members of a repository with none", []model.FederationMember(nil),
		storekit.Get[[]model.FederationMember](t)(fs.Members(ctx, "r1")))

	// RepoKey on the input is overwritten by the repository being set.
	b, a := member("wrong", 2, false), member("wrong", 1, true)
	storekit.Must(t, fs.SetMembers(ctx, "r1", []model.FederationMember{b, a}))
	a.RepoKey, b.RepoKey = "r1", "r1"
	storekit.Same(
		t,
		"Members",
		[]model.FederationMember{a, b},
		storekit.Get[[]model.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	r2a := member("r2", 1, true)
	storekit.Must(t, fs.SetMembers(ctx, "r2", []model.FederationMember{r2a}))
	storekit.Same(t, "MembersWithPeer", []model.FederationMember{a, r2a},
		storekit.Get[[]model.FederationMember](t)(fs.MembersWithPeer(ctx, peerID(1))))

	// Invalid sets change nothing.
	storekit.WantErr(
		t,
		fs.SetMembers(ctx, "r1", []model.FederationMember{{BaseURL: "https://x"}}),
		kernel.ErrInvalid,
	)
	storekit.WantErr(
		t,
		fs.SetMembers(
			ctx,
			"r1",
			[]model.FederationMember{member("r1", 3, true), member("r1", 3, false)},
		),
		kernel.ErrConflict,
	)
	storekit.Same(t, "Members after refused sets", []model.FederationMember{a, b},
		storekit.Get[[]model.FederationMember](t)(fs.Members(ctx, "r1")))

	c := member("r1", 3, true)
	storekit.Must(t, fs.SetMembers(ctx, "r1", []model.FederationMember{c}))
	storekit.Same(
		t,
		"Members after replace",
		[]model.FederationMember{c},
		storekit.Get[[]model.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	storekit.Must(t, fs.SetMembers(ctx, "r1", nil))
	storekit.Same(
		t,
		"Members after clearing",
		[]model.FederationMember(nil),
		storekit.Get[[]model.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	storekit.Same(t, "MembersWithPeer after clearing", []model.FederationMember{r2a},
		storekit.Get[[]model.FederationMember](t)(fs.MembersWithPeer(ctx, peerID(1))))
}

func testFullSyncs(t *testing.T, s port.Store) {
	ctx := context.Background()
	fs := s.Federation()
	_, err := fs.FullSync(ctx, 1)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, fs.UpdateFullSync(ctx, model.FullSyncRun{ID: 99}), kernel.ErrNotFound)

	const runs = 5
	created := make([]model.FullSyncRun, 0, runs)
	for i := range runs {
		repoKey := "r1"
		if i%2 == 1 {
			repoKey = "r2"
		}
		r := model.FullSyncRun{
			RepoKey: repoKey, PeerInstanceID: peerID(i), Forced: i == 0, SnapshotSeq: int64(10 * i),
			Started: storekit.At(i), Status: retentionapi.RunInProgress,
		}
		got := storekit.Get[model.FullSyncRun](t)(fs.CreateFullSync(ctx, r))
		if got.ID <= 0 || (len(created) > 0 && got.ID <= created[len(created)-1].ID) {
			t.Fatalf("CreateFullSync id = %d after %v", got.ID, created)
		}
		r.ID = got.ID
		storekit.Same(t, "CreateFullSync", r, got)
		created = append(created, r)
	}
	done := created[2]
	done.Finished, done.Status, done.ResultPaths = storekit.At(
		40,
	), retentionapi.RunCompleted, []string{
		"a/1.jar",
		"b/2.jar",
	}
	storekit.Must(t, fs.UpdateFullSync(ctx, done))
	got := storekit.Get[model.FullSyncRun](t)(fs.FullSync(ctx, done.ID))
	storekit.Same(t, "FullSync after update", done, got)
	got.ResultPaths[0] = "changed"
	storekit.Same(
		t,
		"FullSync after mutating a copy",
		done,
		storekit.Get[model.FullSyncRun](t)(fs.FullSync(ctx, done.ID)),
	)
	created[2] = done

	newest := slices.Clone(created)
	slices.Reverse(newest)
	all := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.FullSyncRun, string, error) { return fs.FullSyncs(ctx, "", p) },
	)
	storekit.Same(t, "FullSyncs", newest, all)
	r1 := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]model.FullSyncRun, string, error) { return fs.FullSyncs(ctx, "r1", p) },
	)
	storekit.Same(t, "FullSyncs of r1", []model.FullSyncRun{created[4], created[2], created[0]}, r1)

	_, _, err = fs.FullSyncs(ctx, "", kernel.Page{After: kernel.Cursor("not-a-number")})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	_, _, err = fs.FullSyncs(ctx, "", kernel.Page{After: kernel.Cursor("1", "2")})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	_, _, err = fs.FullSyncs(ctx, "", kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	//nolint:wrapcheck // each case checks only that the call fails
	failsCancelled(t, map[string]func(context.Context) error{
		"CreateFullSync": func(ctx context.Context) error { _, err := fs.CreateFullSync(ctx, done); return err },
		"FullSync":       func(ctx context.Context) error { _, err := fs.FullSync(ctx, done.ID); return err },
		"FullSyncs": func(ctx context.Context) error {
			_, _, err := fs.FullSyncs(ctx, "", kernel.Page{})
			return err
		},
		"SetMembers": func(ctx context.Context) error { return fs.SetMembers(ctx, "r1", nil) },
		"Members":    func(ctx context.Context) error { _, err := fs.Members(ctx, "r1"); return err },
	})
}

func testAccessTargets(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.AccessTarget(ctx, "edge")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ps.DeleteAccessTarget(ctx, "edge"), kernel.ErrNotFound)

	edge := model.AccessFederationTarget{
		Name:           "edge",
		PeerInstanceID: peerID(2),
		Active:         true,
		Entities:       []model.FederatedEntity{model.EntityUsers, model.EntityGroups},
		PermissionFilters: model.PatternFilter{
			Include: []string{"team-*"},
			Exclude: []string{"team-x"},
		},
		GroupFilters: model.PatternFilter{Include: []string{"*"}},
		Created:      storekit.At(1),
	}
	bare := model.AccessFederationTarget{Name: "bare", PeerInstanceID: peerID(3)}
	for _, v := range []model.AccessFederationTarget{edge, bare} {
		storekit.Must(t, ps.PutAccessTarget(ctx, v))
	}
	got := storekit.Get[model.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge"))
	storekit.Same(t, "AccessTarget", edge, got)
	got.Entities[0] = model.EntityTokens
	storekit.Same(
		t,
		"AccessTarget after mutating a copy",
		edge,
		storekit.Get[model.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge")),
	)
	storekit.Same(t, "AccessTargets", []model.AccessFederationTarget{bare, edge},
		storekit.Get[[]model.AccessFederationTarget](t)(ps.AccessTargets(ctx)))

	edge.Active, edge.Entities = false, []model.FederatedEntity{model.EntityPermissions}
	storekit.Must(t, ps.PutAccessTarget(ctx, edge))
	storekit.Same(
		t,
		"AccessTarget after replace",
		edge,
		storekit.Get[model.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge")),
	)
	storekit.Must(t, ps.DeleteAccessTarget(ctx, "bare"))
	storekit.Same(t, "AccessTargets after delete", []model.AccessFederationTarget{edge},
		storekit.Get[[]model.AccessFederationTarget](t)(ps.AccessTargets(ctx)))
}

func testPlatformFederations(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.Federation(ctx, "f1")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ps.DeleteFederation(ctx, "f1"), kernel.ErrNotFound)

	f2 := model.PlatformFederation{
		ID: "f2", Name: "Europe", Description: "EU edges",
		ConnectionGroups: map[string][]string{"eu": {peerID(1), peerID(2)}, "uk": {peerID(3)}},
		Entities:         []string{"CURATION_POLICIES"},
	}
	f1 := model.PlatformFederation{ID: "f1", Name: "Empty"}
	for _, v := range []model.PlatformFederation{f2, f1} {
		storekit.Must(t, ps.PutFederation(ctx, v))
	}
	got := storekit.Get[model.PlatformFederation](t)(ps.Federation(ctx, "f2"))
	storekit.Same(t, "Federation", f2, got)
	got.ConnectionGroups["eu"][0] = "changed"
	storekit.Same(
		t,
		"Federation after mutating a copy",
		f2,
		storekit.Get[model.PlatformFederation](t)(ps.Federation(ctx, "f2")),
	)
	storekit.Same(
		t,
		"Federations",
		[]model.PlatformFederation{f1, f2},
		storekit.Get[[]model.PlatformFederation](t)(ps.Federations(ctx)),
	)

	f1.Description, f1.ConnectionGroups = "now with a group", map[string][]string{
		"all": {peerID(4)},
	}
	storekit.Must(t, ps.PutFederation(ctx, f1))
	storekit.Same(
		t,
		"Federation after replace",
		f1,
		storekit.Get[model.PlatformFederation](t)(ps.Federation(ctx, "f1")),
	)
	storekit.Must(t, ps.DeleteFederation(ctx, "f2"))
	storekit.Same(t, "Federations after delete", []model.PlatformFederation{f1},
		storekit.Get[[]model.PlatformFederation](t)(ps.Federations(ctx)))
}

func testTopologies(t *testing.T, s port.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.Topology(ctx, "eu")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ps.DeleteTopology(ctx, "eu"), kernel.ErrNotFound)

	site := func(name string, peer int) model.TopologySite {
		return model.TopologySite{SiteName: name, PeerInstanceID: peerID(peer)}
	}
	eu := model.Topology{
		Key: "eu", Name: "Europe", Description: "EU sites",
		Sites:   []model.TopologySite{site("paris", 2), site("cardiff", 1)},
		Created: storekit.At(1), Modified: storekit.At(2),
	}
	bare := model.Topology{Key: "bare", Name: "No sites"}
	for _, v := range []model.Topology{eu, bare} {
		storekit.Must(t, ps.PutTopology(ctx, v))
	}
	// Sites come back ordered by name.
	wantEU := eu
	wantEU.Sites = []model.TopologySite{site("cardiff", 1), site("paris", 2)}
	got := storekit.Get[model.Topology](t)(ps.Topology(ctx, "eu"))
	storekit.Same(t, "Topology", wantEU, got)
	got.Sites[0].SiteName = "changed"
	storekit.Same(
		t,
		"Topology after mutating a copy",
		wantEU,
		storekit.Get[model.Topology](t)(ps.Topology(ctx, "eu")),
	)
	storekit.Same(
		t,
		"Topologies",
		[]model.Topology{bare, wantEU},
		storekit.Get[[]model.Topology](t)(ps.Topologies(ctx)),
	)

	// Duplicate site names are refused without touching the stored topology.
	dup := eu
	dup.Sites = []model.TopologySite{site("x", 1), site("x", 2)}
	storekit.WantErr(t, ps.PutTopology(ctx, dup), kernel.ErrConflict)
	storekit.Same(
		t,
		"Topology after a refused put",
		wantEU,
		storekit.Get[model.Topology](t)(ps.Topology(ctx, "eu")),
	)

	wantEU.Sites, wantEU.Modified = []model.TopologySite{site("berlin", 3)}, storekit.At(9)
	storekit.Must(t, ps.PutTopology(ctx, wantEU))
	storekit.Same(
		t,
		"Topology after replace",
		wantEU,
		storekit.Get[model.Topology](t)(ps.Topology(ctx, "eu")),
	)
	storekit.Must(t, ps.DeleteTopology(ctx, "eu"))
	_, err = ps.Topology(ctx, "eu")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"Topologies after delete",
		[]model.Topology{bare},
		storekit.Get[[]model.Topology](t)(ps.Topologies(ctx)),
	)
	// Re-creating a deleted topology starts with only its new sites.
	storekit.Must(t, ps.PutTopology(ctx, model.Topology{Key: "eu"}))
	storekit.Same(
		t,
		"Topology re-created",
		model.Topology{Key: "eu"},
		storekit.Get[model.Topology](t)(ps.Topology(ctx, "eu")),
	)
	//nolint:wrapcheck // each case checks only that the call fails
	failsCancelled(t, map[string]func(context.Context) error{
		"PutTopology":    func(ctx context.Context) error { return ps.PutTopology(ctx, bare) },
		"Topology":       func(ctx context.Context) error { _, err := ps.Topology(ctx, "eu"); return err },
		"Topologies":     func(ctx context.Context) error { _, err := ps.Topologies(ctx); return err },
		"DeleteTopology": func(ctx context.Context) error { return ps.DeleteTopology(ctx, "eu") },
		"AccessTargets":  func(ctx context.Context) error { _, err := ps.AccessTargets(ctx); return err },
		"Federations":    func(ctx context.Context) error { _, err := ps.Federations(ctx); return err },
	})
}
