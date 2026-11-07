package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Table names of the peers group; the same names as the SQL tables.
const (
	tPeers         = "peers"
	tPairingNonces = "pairing_nonces"
	tFedMembers    = "federation_members"
	tFullSyncs     = "full_sync_runs"
	tAccessTargets = "access_federation_targets"
	tPlatformFeds  = "platform_federations"
	tTopologies    = "topologies"
)

// peersPair identifies one stream of one origin, or one peer's position in
// a stream.
type peersPair struct{ a, b string }

// Peers implements store.Store.
func (s *Store) Peers() store.PeerStore { return peerStore{s} }

type peerStore struct{ s *Store }

func (p peerStore) t() *memtx.Table[string, types.Peer] {
	return memtx.TableOf[string, types.Peer](p.s.Store, tPeers)
}

func (p peerStore) nonces() *memtx.Table[string, types.PairingNonce] {
	return memtx.TableOf[string, types.PairingNonce](p.s.Store, tPairingNonces)
}

// useCaseSet sorts and deduplicates use cases, the form they are stored in.
func useCaseSet(in []types.UseCase) []types.UseCase {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (p peerStore) Put(ctx context.Context, peer types.Peer) error {
	return p.s.Do(ctx, func() error {
		peer.UseCases = useCaseSet(peer.UseCases)
		p.t().Put(peer.InstanceID, peer)
		return nil
	})
}

func (p peerStore) Get(ctx context.Context, instanceID string) (types.Peer, error) {
	var out types.Peer
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.t().Get(instanceID); !ok {
			return memtx.NotFound("peer", instanceID)
		}
		return nil
	})
	return out, err
}

func (p peerStore) List(ctx context.Context, f store.PeerFilter) ([]types.Peer, error) {
	var out []types.Peer
	err := p.s.Do(ctx, func() error {
		out = p.t().All(func(_ string, v types.Peer) bool {
			return f.UseCase == "" || v.Has(f.UseCase)
		}, func(a, b types.Peer) int { return strings.Compare(a.InstanceID, b.InstanceID) })
		return nil
	})
	return out, err
}

func (p peerStore) Delete(ctx context.Context, instanceID string) error {
	return p.s.Do(ctx, func() error {
		if !p.t().Del(instanceID) {
			return memtx.NotFound("peer", instanceID)
		}
		return nil
	})
}

func (p peerStore) AddNonce(ctx context.Context, n types.PairingNonce) error {
	return p.s.Do(ctx, func() error {
		k := string(n.NonceHash)
		if p.nonces().Has(k) {
			return memtx.Conflict("pairing nonce", fmt.Sprintf("%x", n.NonceHash))
		}
		p.nonces().Put(k, n)
		return nil
	})
}

func (p peerStore) RedeemNonce(
	ctx context.Context,
	hash []byte,
	now time.Time,
) (types.PairingNonce, error) {
	var out types.PairingNonce
	err := p.s.Do(ctx, func() error {
		n, ok := p.nonces().Get(string(hash))
		switch {
		case !ok:
			return memtx.NotFound("pairing nonce", fmt.Sprintf("%x", hash))
		case !n.RedeemedAt.IsZero():
			return memtx.Conflict("pairing nonce already redeemed", fmt.Sprintf("%x", hash))
		case !store.Normalise(now).Before(n.ExpiresAt):
			return memtx.NotFound("pairing nonce expired", fmt.Sprintf("%x", hash))
		}
		n.RedeemedAt = now
		p.nonces().Put(string(hash), n)
		out, _ = p.nonces().Get(string(hash))
		return nil
	})
	return out, err
}

func (p peerStore) PruneNonces(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := p.s.Do(ctx, func() error {
		before = store.Normalise(before)
		n = p.nonces().DeleteWhere(func(_ string, v types.PairingNonce) bool {
			return v.ExpiresAt.Before(before)
		})
		return nil
	})
	return n, err
}

// Federation implements store.Store.
func (s *Store) Federation() store.FederationStore { return federationStore{s} }

type federationStore struct{ s *Store }

func (f federationStore) members() *memtx.Table[peersPair, types.FederationMember] {
	return memtx.TableOf[peersPair, types.FederationMember](f.s.Store, tFedMembers)
}

func (f federationStore) runs() *memtx.Table[int64, types.FullSyncRun] {
	return memtx.TableOf[int64, types.FullSyncRun](f.s.Store, tFullSyncs)
}

func (f federationStore) SetMembers(
	ctx context.Context,
	repoKey string,
	members []types.FederationMember,
) error {
	return f.s.Do(ctx, func() error {
		seen := make(map[string]bool, len(members))
		for _, m := range members {
			if m.PeerInstanceID == "" {
				return fmt.Errorf(
					"federation member %q of %s has no peer: %w",
					m.BaseURL,
					repoKey,
					store.ErrInvalid,
				)
			}
			if seen[m.PeerInstanceID] {
				return memtx.Conflict("federation member of "+repoKey, m.PeerInstanceID)
			}
			seen[m.PeerInstanceID] = true
		}
		f.members().
			DeleteWhere(func(k peersPair, _ types.FederationMember) bool { return k.a == repoKey })
		for _, m := range members {
			m.RepoKey = repoKey
			f.members().Put(peersPair{repoKey, m.PeerInstanceID}, m)
		}
		return nil
	})
}

func (f federationStore) Members(
	ctx context.Context,
	repoKey string,
) ([]types.FederationMember, error) {
	var out []types.FederationMember
	err := f.s.Do(ctx, func() error {
		out = f.members().
			All(func(k peersPair, _ types.FederationMember) bool { return k.a == repoKey },
				func(a, b types.FederationMember) int { return strings.Compare(a.PeerInstanceID, b.PeerInstanceID) })
		return nil
	})
	return out, err
}

func (f federationStore) MembersWithPeer(
	ctx context.Context,
	peer string,
) ([]types.FederationMember, error) {
	var out []types.FederationMember
	err := f.s.Do(ctx, func() error {
		out = f.members().
			All(func(k peersPair, _ types.FederationMember) bool { return k.b == peer },
				func(a, b types.FederationMember) int { return strings.Compare(a.RepoKey, b.RepoKey) })
		return nil
	})
	return out, err
}

func (f federationStore) CreateFullSync(
	ctx context.Context,
	r types.FullSyncRun,
) (types.FullSyncRun, error) {
	var out types.FullSyncRun
	err := f.s.Do(ctx, func() error {
		r.ID = f.s.NextID(tFullSyncs)
		f.runs().Put(r.ID, r)
		out, _ = f.runs().Get(r.ID)
		return nil
	})
	return out, err
}

func (f federationStore) FullSync(ctx context.Context, id int64) (types.FullSyncRun, error) {
	var out types.FullSyncRun
	err := f.s.Do(ctx, func() error {
		var ok bool
		if out, ok = f.runs().Get(id); !ok {
			return memtx.NotFound("full sync run", id)
		}
		return nil
	})
	return out, err
}

func (f federationStore) UpdateFullSync(ctx context.Context, r types.FullSyncRun) error {
	return f.s.Do(ctx, func() error {
		if !f.runs().Has(r.ID) {
			return memtx.NotFound("full sync run", r.ID)
		}
		f.runs().Put(r.ID, r)
		return nil
	})
}

func (f federationStore) FullSyncs(
	ctx context.Context, repoKey string, p store.Page,
) ([]types.FullSyncRun, string, error) {
	var (
		out  []types.FullSyncRun
		next string
	)
	err := f.s.Do(ctx, func() error {
		rows := f.runs().All(func(_ int64, v types.FullSyncRun) bool {
			return repoKey == "" || v.RepoKey == repoKey
		}, func(a, b types.FullSyncRun) int { return cmp.Compare(b.ID, a.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			true,
			func(v types.FullSyncRun) []any { return []any{v.ID} },
		)
		return err
	})
	return out, next, err
}

// Platform implements store.Store.
func (s *Store) Platform() store.PlatformStore { return platformStore{s} }

type platformStore struct{ s *Store }

func (p platformStore) targets() *memtx.Table[string, types.AccessFederationTarget] {
	return memtx.TableOf[string, types.AccessFederationTarget](p.s.Store, tAccessTargets)
}

func (p platformStore) feds() *memtx.Table[string, types.PlatformFederation] {
	return memtx.TableOf[string, types.PlatformFederation](p.s.Store, tPlatformFeds)
}

func (p platformStore) topologies() *memtx.Table[string, types.Topology] {
	return memtx.TableOf[string, types.Topology](p.s.Store, tTopologies)
}

func (p platformStore) PutAccessTarget(ctx context.Context, t types.AccessFederationTarget) error {
	return p.s.Do(ctx, func() error {
		p.targets().Put(t.Name, t)
		return nil
	})
}

func (p platformStore) AccessTarget(
	ctx context.Context,
	name string,
) (types.AccessFederationTarget, error) {
	var out types.AccessFederationTarget
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.targets().Get(name); !ok {
			return memtx.NotFound("access federation target", name)
		}
		return nil
	})
	return out, err
}

func (p platformStore) AccessTargets(ctx context.Context) ([]types.AccessFederationTarget, error) {
	var out []types.AccessFederationTarget
	err := p.s.Do(ctx, func() error {
		out = p.targets().
			All(nil, func(a, b types.AccessFederationTarget) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteAccessTarget(ctx context.Context, name string) error {
	return p.s.Do(ctx, func() error {
		if !p.targets().Del(name) {
			return memtx.NotFound("access federation target", name)
		}
		return nil
	})
}

func (p platformStore) PutFederation(ctx context.Context, f types.PlatformFederation) error {
	return p.s.Do(ctx, func() error {
		p.feds().Put(f.ID, f)
		return nil
	})
}

func (p platformStore) Federation(
	ctx context.Context,
	id string,
) (types.PlatformFederation, error) {
	var out types.PlatformFederation
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.feds().Get(id); !ok {
			return memtx.NotFound("platform federation", id)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Federations(ctx context.Context) ([]types.PlatformFederation, error) {
	var out []types.PlatformFederation
	err := p.s.Do(ctx, func() error {
		out = p.feds().
			All(nil, func(a, b types.PlatformFederation) int { return strings.Compare(a.ID, b.ID) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteFederation(ctx context.Context, id string) error {
	return p.s.Do(ctx, func() error {
		if !p.feds().Del(id) {
			return memtx.NotFound("platform federation", id)
		}
		return nil
	})
}

func (p platformStore) PutTopology(ctx context.Context, t types.Topology) error {
	return p.s.Do(ctx, func() error {
		if t.Sites != nil {
			t.Sites = slices.Clone(t.Sites)
			slices.SortFunc(
				t.Sites,
				func(a, b types.TopologySite) int { return strings.Compare(a.SiteName, b.SiteName) },
			)
			for i := 1; i < len(t.Sites); i++ {
				if t.Sites[i].SiteName == t.Sites[i-1].SiteName {
					return memtx.Conflict("topology "+t.Key+" site", t.Sites[i].SiteName)
				}
			}
		}
		p.topologies().Put(t.Key, t)
		return nil
	})
}

func (p platformStore) Topology(ctx context.Context, key string) (types.Topology, error) {
	var out types.Topology
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.topologies().Get(key); !ok {
			return memtx.NotFound("topology", key)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Topologies(ctx context.Context) ([]types.Topology, error) {
	var out []types.Topology
	err := p.s.Do(ctx, func() error {
		out = p.topologies().
			All(nil, func(a, b types.Topology) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteTopology(ctx context.Context, key string) error {
	return p.s.Do(ctx, func() error {
		if !p.topologies().Del(key) {
			return memtx.NotFound("topology", key)
		}
		return nil
	})
}
