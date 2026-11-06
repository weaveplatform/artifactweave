package storetest

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	seed[0] = byte(i)
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv //nolint:forcetypeassert // ed25519 public keys are this type
}

// peer builds fixture peer i.
func peer(i int, uses ...types.UseCase) types.Peer {
	pub, _ := peerKey(i)
	return types.Peer{
		InstanceID:      peerID(i),
		BaseURL:         fmt.Sprintf("https://edge-%d.example.com", i),
		PublicKey:       pub,
		ProtocolVersion: types.ProtocolVersion,
		UseCases:        uses,
		Status:          types.PeerOnline,
		LastSeen:        at(i),
		DisplayName:     fmt.Sprintf("Edge %d", i),
		CityName:        "Cardiff",
		CountryCode:     "GB",
		EdgeRole:        i%2 == 0,
	}
}

func testPeers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Peers()
	_, err := ps.Get(ctx, peerID(1))
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ps.Delete(ctx, peerID(1)), store.ErrNotFound)

	p1 := peer(1, types.UseFederation, types.UseDistribution, types.UseFederation)
	p2 := peer(2, types.UseDistribution)
	p3 := peer(3)
	for _, p := range []types.Peer{p3, p1, p2} {
		must(t, ps.Put(ctx, p))
	}
	// Use cases are a set, returned sorted.
	wantP1 := p1
	wantP1.UseCases = []types.UseCase{types.UseDistribution, types.UseFederation}
	got := get[types.Peer](t)(ps.Get(ctx, p1.InstanceID))
	same(t, "Get", wantP1, got)

	// Neither the caller's input nor a returned value aliases the store.
	p1.UseCases[0] = types.UseAccessFederation
	got.UseCases[0] = types.UseAccessFederation
	got.PublicKey[0]++
	same(t, "Get after mutating copies", wantP1, get[types.Peer](t)(ps.Get(ctx, p1.InstanceID)))

	all := get[[]types.Peer](t)(ps.List(ctx, store.PeerFilter{}))
	same(t, "List", []types.Peer{wantP1, p2, p3}, all)
	dist := get[[]types.Peer](t)(ps.List(ctx, store.PeerFilter{UseCase: types.UseDistribution}))
	same(t, "List distribution", []types.Peer{wantP1, p2}, dist)
	none := get[[]types.Peer](
		t,
	)(
		ps.List(ctx, store.PeerFilter{UseCase: types.UsePlatformFederation}),
	)
	same(t, "List platform-federation", []types.Peer(nil), none)

	// Put replaces, use cases included.
	moved := peer(1, types.UseAccessFederation)
	moved.BaseURL = "https://moved.example.com"
	moved.Status = types.PeerOffline
	moved.LastSeen = at(30)
	must(t, ps.Put(ctx, moved))
	same(t, "Get after replace", moved, get[types.Peer](t)(ps.Get(ctx, moved.InstanceID)))
	dist = get[[]types.Peer](t)(ps.List(ctx, store.PeerFilter{UseCase: types.UseDistribution}))
	same(t, "List distribution after replace", []types.Peer{p2}, dist)

	must(t, ps.Delete(ctx, p2.InstanceID))
	_, err = ps.Get(ctx, p2.InstanceID)
	wantErr(t, err, store.ErrNotFound)
	dist = get[[]types.Peer](t)(ps.List(ctx, store.PeerFilter{UseCase: types.UseDistribution}))
	same(t, "List distribution after delete", []types.Peer(nil), dist)
	// A peer put again after deletion starts with only its new use cases.
	again := peer(2, types.UseFederation)
	must(t, ps.Put(ctx, again))
	same(t, "Get after re-adding", again, get[types.Peer](t)(ps.Get(ctx, again.InstanceID)))
	failsCancelled(t, map[string]func(context.Context) error{
		"Put":    func(ctx context.Context) error { return ps.Put(ctx, again) },
		"Get":    func(ctx context.Context) error { _, err := ps.Get(ctx, again.InstanceID); return err },
		"List":   func(ctx context.Context) error { _, err := ps.List(ctx, store.PeerFilter{}); return err },
		"Delete": func(ctx context.Context) error { return ps.Delete(ctx, again.InstanceID) },
	})
}

// nonce builds a pairing nonce with hash byte i expiring at minute exp.
func nonce(i, exp int) types.PairingNonce {
	return types.PairingNonce{
		NonceHash: []byte{0xaa, byte(i), 0x00, 0xff},
		UseCase:   types.UseFederation,
		ExpiresAt: at(exp),
	}
}

func testPairingNonces(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Peers()
	n1, n2, n3 := nonce(1, 10), nonce(2, 10), nonce(3, 30)
	for _, n := range []types.PairingNonce{n1, n2, n3} {
		must(t, ps.AddNonce(ctx, n))
	}
	wantErr(t, ps.AddNonce(ctx, n1), store.ErrConflict)

	_, err := ps.RedeemNonce(ctx, []byte{0x01}, at(0))
	wantErr(t, err, store.ErrNotFound)
	// Expired: after the expiry, and at the very instant of it.
	_, err = ps.RedeemNonce(ctx, n1.NonceHash, at(11))
	wantErr(t, err, store.ErrNotFound)
	_, err = ps.RedeemNonce(ctx, n1.NonceHash, at(10))
	wantErr(t, err, store.ErrNotFound)

	got := get[types.PairingNonce](t)(ps.RedeemNonce(ctx, n2.NonceHash, at(5)))
	redeemed := n2
	redeemed.RedeemedAt = at(5)
	same(t, "RedeemNonce", redeemed, got)
	// Single use.
	_, err = ps.RedeemNonce(ctx, n2.NonceHash, at(6))
	wantErr(t, err, store.ErrConflict)

	pruned, err := ps.PruneNonces(ctx, at(20))
	must(t, err)
	if pruned != 2 {
		t.Fatalf("PruneNonces = %d, want 2", pruned)
	}
	_, err = ps.RedeemNonce(ctx, n2.NonceHash, at(6))
	wantErr(t, err, store.ErrNotFound)
	got = get[types.PairingNonce](t)(ps.RedeemNonce(ctx, n3.NonceHash, at(25)))
	if !got.RedeemedAt.Equal(want(at(25))) {
		t.Fatalf("RedeemedAt = %v", got.RedeemedAt)
	}
}

func testFederationMembers(t *testing.T, s store.Store) {
	ctx := context.Background()
	fs := s.Federation()
	member := func(repoKey string, peer int, enabled bool) types.FederationMember {
		return types.FederationMember{
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
	same(t, "Members of a repository with none", []types.FederationMember(nil),
		get[[]types.FederationMember](t)(fs.Members(ctx, "r1")))

	// RepoKey on the input is overwritten by the repository being set.
	b, a := member("wrong", 2, false), member("wrong", 1, true)
	must(t, fs.SetMembers(ctx, "r1", []types.FederationMember{b, a}))
	a.RepoKey, b.RepoKey = "r1", "r1"
	same(
		t,
		"Members",
		[]types.FederationMember{a, b},
		get[[]types.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	r2a := member("r2", 1, true)
	must(t, fs.SetMembers(ctx, "r2", []types.FederationMember{r2a}))
	same(t, "MembersWithPeer", []types.FederationMember{a, r2a},
		get[[]types.FederationMember](t)(fs.MembersWithPeer(ctx, peerID(1))))

	// Invalid sets change nothing.
	wantErr(
		t,
		fs.SetMembers(ctx, "r1", []types.FederationMember{{BaseURL: "https://x"}}),
		store.ErrInvalid,
	)
	wantErr(
		t,
		fs.SetMembers(
			ctx,
			"r1",
			[]types.FederationMember{member("r1", 3, true), member("r1", 3, false)},
		),
		store.ErrConflict,
	)
	same(t, "Members after refused sets", []types.FederationMember{a, b},
		get[[]types.FederationMember](t)(fs.Members(ctx, "r1")))

	c := member("r1", 3, true)
	must(t, fs.SetMembers(ctx, "r1", []types.FederationMember{c}))
	same(
		t,
		"Members after replace",
		[]types.FederationMember{c},
		get[[]types.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	must(t, fs.SetMembers(ctx, "r1", nil))
	same(
		t,
		"Members after clearing",
		[]types.FederationMember(nil),
		get[[]types.FederationMember](t)(fs.Members(ctx, "r1")),
	)
	same(t, "MembersWithPeer after clearing", []types.FederationMember{r2a},
		get[[]types.FederationMember](t)(fs.MembersWithPeer(ctx, peerID(1))))
}

func testFullSyncs(t *testing.T, s store.Store) {
	ctx := context.Background()
	fs := s.Federation()
	_, err := fs.FullSync(ctx, 1)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, fs.UpdateFullSync(ctx, types.FullSyncRun{ID: 99}), store.ErrNotFound)

	var created []types.FullSyncRun
	for i := range 5 {
		repoKey := "r1"
		if i%2 == 1 {
			repoKey = "r2"
		}
		r := types.FullSyncRun{
			RepoKey: repoKey, PeerInstanceID: peerID(i), Forced: i == 0, SnapshotSeq: int64(10 * i),
			Started: at(i), Status: types.RunInProgress,
		}
		got := get[types.FullSyncRun](t)(fs.CreateFullSync(ctx, r))
		if got.ID <= 0 || (len(created) > 0 && got.ID <= created[len(created)-1].ID) {
			t.Fatalf("CreateFullSync id = %d after %v", got.ID, created)
		}
		r.ID = got.ID
		same(t, "CreateFullSync", r, got)
		created = append(created, r)
	}
	done := created[2]
	done.Finished, done.Status, done.ResultPaths = at(
		40,
	), types.RunCompleted, []string{
		"a/1.jar",
		"b/2.jar",
	}
	must(t, fs.UpdateFullSync(ctx, done))
	got := get[types.FullSyncRun](t)(fs.FullSync(ctx, done.ID))
	same(t, "FullSync after update", done, got)
	got.ResultPaths[0] = "changed"
	same(
		t,
		"FullSync after mutating a copy",
		done,
		get[types.FullSyncRun](t)(fs.FullSync(ctx, done.ID)),
	)
	created[2] = done

	newest := slices.Clone(created)
	slices.Reverse(newest)
	all := pages(
		t,
		2,
		func(p store.Page) ([]types.FullSyncRun, string, error) { return fs.FullSyncs(ctx, "", p) },
	)
	same(t, "FullSyncs", newest, all)
	r1 := pages(
		t,
		2,
		func(p store.Page) ([]types.FullSyncRun, string, error) { return fs.FullSyncs(ctx, "r1", p) },
	)
	same(t, "FullSyncs of r1", []types.FullSyncRun{created[4], created[2], created[0]}, r1)

	_, _, err = fs.FullSyncs(ctx, "", store.Page{After: store.Cursor("not-a-number")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = fs.FullSyncs(ctx, "", store.Page{After: store.Cursor("1", "2")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = fs.FullSyncs(ctx, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
	failsCancelled(t, map[string]func(context.Context) error{
		"CreateFullSync": func(ctx context.Context) error { _, err := fs.CreateFullSync(ctx, done); return err },
		"FullSync":       func(ctx context.Context) error { _, err := fs.FullSync(ctx, done.ID); return err },
		"FullSyncs": func(ctx context.Context) error {
			_, _, err := fs.FullSyncs(ctx, "", store.Page{})
			return err
		},
		"SetMembers": func(ctx context.Context) error { return fs.SetMembers(ctx, "r1", nil) },
		"Members":    func(ctx context.Context) error { _, err := fs.Members(ctx, "r1"); return err },
	})
}

func testAccessTargets(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.AccessTarget(ctx, "edge")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ps.DeleteAccessTarget(ctx, "edge"), store.ErrNotFound)

	edge := types.AccessFederationTarget{
		Name:           "edge",
		PeerInstanceID: peerID(2),
		Active:         true,
		Entities:       []types.FederatedEntity{types.EntityUsers, types.EntityGroups},
		PermissionFilters: types.PatternFilter{
			Include: []string{"team-*"},
			Exclude: []string{"team-x"},
		},
		GroupFilters: types.PatternFilter{Include: []string{"*"}},
		Created:      at(1),
	}
	bare := types.AccessFederationTarget{Name: "bare", PeerInstanceID: peerID(3)}
	for _, v := range []types.AccessFederationTarget{edge, bare} {
		must(t, ps.PutAccessTarget(ctx, v))
	}
	got := get[types.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge"))
	same(t, "AccessTarget", edge, got)
	got.Entities[0] = types.EntityTokens
	same(
		t,
		"AccessTarget after mutating a copy",
		edge,
		get[types.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge")),
	)
	same(t, "AccessTargets", []types.AccessFederationTarget{bare, edge},
		get[[]types.AccessFederationTarget](t)(ps.AccessTargets(ctx)))

	edge.Active, edge.Entities = false, []types.FederatedEntity{types.EntityPermissions}
	must(t, ps.PutAccessTarget(ctx, edge))
	same(
		t,
		"AccessTarget after replace",
		edge,
		get[types.AccessFederationTarget](t)(ps.AccessTarget(ctx, "edge")),
	)
	must(t, ps.DeleteAccessTarget(ctx, "bare"))
	same(t, "AccessTargets after delete", []types.AccessFederationTarget{edge},
		get[[]types.AccessFederationTarget](t)(ps.AccessTargets(ctx)))
}

func testPlatformFederations(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.Federation(ctx, "f1")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ps.DeleteFederation(ctx, "f1"), store.ErrNotFound)

	f2 := types.PlatformFederation{
		ID: "f2", Name: "Europe", Description: "EU edges",
		ConnectionGroups: map[string][]string{"eu": {peerID(1), peerID(2)}, "uk": {peerID(3)}},
		Entities:         []string{"CURATION_POLICIES"},
	}
	f1 := types.PlatformFederation{ID: "f1", Name: "Empty"}
	for _, v := range []types.PlatformFederation{f2, f1} {
		must(t, ps.PutFederation(ctx, v))
	}
	got := get[types.PlatformFederation](t)(ps.Federation(ctx, "f2"))
	same(t, "Federation", f2, got)
	got.ConnectionGroups["eu"][0] = "changed"
	same(
		t,
		"Federation after mutating a copy",
		f2,
		get[types.PlatformFederation](t)(ps.Federation(ctx, "f2")),
	)
	same(
		t,
		"Federations",
		[]types.PlatformFederation{f1, f2},
		get[[]types.PlatformFederation](t)(ps.Federations(ctx)),
	)

	f1.Description, f1.ConnectionGroups = "now with a group", map[string][]string{
		"all": {peerID(4)},
	}
	must(t, ps.PutFederation(ctx, f1))
	same(
		t,
		"Federation after replace",
		f1,
		get[types.PlatformFederation](t)(ps.Federation(ctx, "f1")),
	)
	must(t, ps.DeleteFederation(ctx, "f2"))
	same(t, "Federations after delete", []types.PlatformFederation{f1},
		get[[]types.PlatformFederation](t)(ps.Federations(ctx)))
}

func testTopologies(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Platform()
	_, err := ps.Topology(ctx, "eu")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ps.DeleteTopology(ctx, "eu"), store.ErrNotFound)

	site := func(name string, peer int) types.TopologySite {
		return types.TopologySite{SiteName: name, PeerInstanceID: peerID(peer)}
	}
	eu := types.Topology{
		Key: "eu", Name: "Europe", Description: "EU sites",
		Sites:   []types.TopologySite{site("paris", 2), site("cardiff", 1)},
		Created: at(1), Modified: at(2),
	}
	bare := types.Topology{Key: "bare", Name: "No sites"}
	for _, v := range []types.Topology{eu, bare} {
		must(t, ps.PutTopology(ctx, v))
	}
	// Sites come back ordered by name.
	wantEU := eu
	wantEU.Sites = []types.TopologySite{site("cardiff", 1), site("paris", 2)}
	got := get[types.Topology](t)(ps.Topology(ctx, "eu"))
	same(t, "Topology", wantEU, got)
	got.Sites[0].SiteName = "changed"
	same(
		t,
		"Topology after mutating a copy",
		wantEU,
		get[types.Topology](t)(ps.Topology(ctx, "eu")),
	)
	same(
		t,
		"Topologies",
		[]types.Topology{bare, wantEU},
		get[[]types.Topology](t)(ps.Topologies(ctx)),
	)

	// Duplicate site names are refused without touching the stored topology.
	dup := eu
	dup.Sites = []types.TopologySite{site("x", 1), site("x", 2)}
	wantErr(t, ps.PutTopology(ctx, dup), store.ErrConflict)
	same(t, "Topology after a refused put", wantEU, get[types.Topology](t)(ps.Topology(ctx, "eu")))

	wantEU.Sites, wantEU.Modified = []types.TopologySite{site("berlin", 3)}, at(9)
	must(t, ps.PutTopology(ctx, wantEU))
	same(t, "Topology after replace", wantEU, get[types.Topology](t)(ps.Topology(ctx, "eu")))
	must(t, ps.DeleteTopology(ctx, "eu"))
	_, err = ps.Topology(ctx, "eu")
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"Topologies after delete",
		[]types.Topology{bare},
		get[[]types.Topology](t)(ps.Topologies(ctx)),
	)
	// Re-creating a deleted topology starts with only its new sites.
	must(t, ps.PutTopology(ctx, types.Topology{Key: "eu"}))
	same(
		t,
		"Topology re-created",
		types.Topology{Key: "eu"},
		get[types.Topology](t)(ps.Topology(ctx, "eu")),
	)
	failsCancelled(t, map[string]func(context.Context) error{
		"PutTopology":    func(ctx context.Context) error { return ps.PutTopology(ctx, bare) },
		"Topology":       func(ctx context.Context) error { _, err := ps.Topology(ctx, "eu"); return err },
		"Topologies":     func(ctx context.Context) error { _, err := ps.Topologies(ctx); return err },
		"DeleteTopology": func(ctx context.Context) error { return ps.DeleteTopology(ctx, "eu") },
		"AccessTargets":  func(ctx context.Context) error { _, err := ps.AccessTargets(ctx); return err },
		"Federations":    func(ctx context.Context) error { _, err := ps.Federations(ctx); return err },
	})
}
