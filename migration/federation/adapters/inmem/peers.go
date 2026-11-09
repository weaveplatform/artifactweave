package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/federation/core/model"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// The tables have the SQL tables' names.
const (
	tPeers         = "peers"
	tPairingNonces = "pairing_nonces"
	tFedMembers    = "federation_members"
	tFullSyncs     = "full_sync_runs"
	tAccessTargets = "access_federation_targets"
	tPlatformFeds  = "platform_federations"
	tTopologies    = "topologies"
)

// peersPair is a federation member's key: a repository and a peer.
type peersPair struct{ a, b string }

// Peers returns the peer store: paired instances and the pairing nonces
// that admit new ones (decision 0021).
func (s *Store) Peers() port.PeerStore { return peerStore{s} }

type peerStore struct{ s *Store }

func (p peerStore) t() *memtx.Table[string, model.Peer] {
	return memtx.TableOf[string, model.Peer](p.s.Store, tPeers)
}

func (p peerStore) nonces() *memtx.Table[string, model.PairingNonce] {
	return memtx.TableOf[string, model.PairingNonce](p.s.Store, tPairingNonces)
}

// useCaseSet sorts and deduplicates use cases, the form they are stored in.
func useCaseSet(in []model.UseCase) []model.UseCase {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (p peerStore) Put(ctx context.Context, peer model.Peer) error {
	return p.s.Do(ctx, func() error {
		peer.UseCases = useCaseSet(peer.UseCases)
		p.t().Put(peer.InstanceID, peer)
		return nil
	})
}

func (p peerStore) Get(ctx context.Context, instanceID string) (model.Peer, error) {
	var out model.Peer
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.t().Get(instanceID); !ok {
			return memtx.NotFound("peer", instanceID)
		}
		return nil
	})
	return out, err
}

func (p peerStore) List(ctx context.Context, f port.PeerFilter) ([]model.Peer, error) {
	var out []model.Peer
	err := p.s.Do(ctx, func() error {
		out = p.t().All(func(_ string, v model.Peer) bool {
			return f.UseCase == "" || v.Has(f.UseCase)
		}, func(a, b model.Peer) int { return strings.Compare(a.InstanceID, b.InstanceID) })
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

func (p peerStore) AddNonce(ctx context.Context, n model.PairingNonce) error {
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
) (model.PairingNonce, error) {
	var out model.PairingNonce
	err := p.s.Do(ctx, func() error {
		n, ok := p.nonces().Get(string(hash))
		switch {
		case !ok:
			return memtx.NotFound("pairing nonce", fmt.Sprintf("%x", hash))
		case !n.RedeemedAt.IsZero():
			return memtx.Conflict("pairing nonce already redeemed", fmt.Sprintf("%x", hash))
		case !kernel.Normalise(now).Before(n.ExpiresAt):
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
		before = kernel.Normalise(before)
		n = p.nonces().DeleteWhere(func(_ string, v model.PairingNonce) bool {
			return v.ExpiresAt.Before(before)
		})
		return nil
	})
	return n, err
}

// Federation returns the federated repository store: members and full
// syncs (decision 0022).
func (s *Store) Federation() port.FederationStore { return federationStore{s} }

type federationStore struct{ s *Store }

func (f federationStore) members() *memtx.Table[peersPair, model.FederationMember] {
	return memtx.TableOf[peersPair, model.FederationMember](f.s.Store, tFedMembers)
}

func (f federationStore) runs() *memtx.Table[int64, model.FullSyncRun] {
	return memtx.TableOf[int64, model.FullSyncRun](f.s.Store, tFullSyncs)
}

func (f federationStore) SetMembers(
	ctx context.Context,
	repoKey string,
	members []model.FederationMember,
) error {
	return f.s.Do(ctx, func() error {
		seen := make(map[string]bool, len(members))
		for _, m := range members {
			if m.PeerInstanceID == "" {
				return fmt.Errorf(
					"federation member %q of %s has no peer: %w",
					m.BaseURL,
					repoKey,
					kernel.ErrInvalid,
				)
			}
			if seen[m.PeerInstanceID] {
				return memtx.Conflict("federation member of "+repoKey, m.PeerInstanceID)
			}
			seen[m.PeerInstanceID] = true
		}
		f.members().
			DeleteWhere(func(k peersPair, _ model.FederationMember) bool { return k.a == repoKey })
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
) ([]model.FederationMember, error) {
	var out []model.FederationMember
	err := f.s.Do(ctx, func() error {
		out = f.members().
			All(func(k peersPair, _ model.FederationMember) bool { return k.a == repoKey },
				func(a, b model.FederationMember) int { return strings.Compare(a.PeerInstanceID, b.PeerInstanceID) })
		return nil
	})
	return out, err
}

func (f federationStore) MembersWithPeer(
	ctx context.Context,
	peer string,
) ([]model.FederationMember, error) {
	var out []model.FederationMember
	err := f.s.Do(ctx, func() error {
		out = f.members().
			All(func(k peersPair, _ model.FederationMember) bool { return k.b == peer },
				func(a, b model.FederationMember) int { return strings.Compare(a.RepoKey, b.RepoKey) })
		return nil
	})
	return out, err
}

func (f federationStore) CreateFullSync(
	ctx context.Context,
	r model.FullSyncRun,
) (model.FullSyncRun, error) {
	var out model.FullSyncRun
	err := f.s.Do(ctx, func() error {
		r.ID = f.s.NextID(tFullSyncs)
		f.runs().Put(r.ID, r)
		out, _ = f.runs().Get(r.ID)
		return nil
	})
	return out, err
}

func (f federationStore) FullSync(ctx context.Context, id int64) (model.FullSyncRun, error) {
	var out model.FullSyncRun
	err := f.s.Do(ctx, func() error {
		var ok bool
		if out, ok = f.runs().Get(id); !ok {
			return memtx.NotFound("full sync run", id)
		}
		return nil
	})
	return out, err
}

func (f federationStore) UpdateFullSync(ctx context.Context, r model.FullSyncRun) error {
	return f.s.Do(ctx, func() error {
		if !f.runs().Has(r.ID) {
			return memtx.NotFound("full sync run", r.ID)
		}
		f.runs().Put(r.ID, r)
		return nil
	})
}

func (f federationStore) FullSyncs(
	ctx context.Context, repoKey string, p kernel.Page,
) ([]model.FullSyncRun, string, error) {
	var (
		out  []model.FullSyncRun
		next string
	)
	err := f.s.Do(ctx, func() error {
		rows := f.runs().All(func(_ int64, v model.FullSyncRun) bool {
			return repoKey == "" || v.RepoKey == repoKey
		}, func(a, b model.FullSyncRun) int { return cmp.Compare(b.ID, a.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			true,
			func(v model.FullSyncRun) []any { return []any{v.ID} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

// Platform returns the platform federation store: access federation
// targets, platform federations and topologies (decision 0025).
func (s *Store) Platform() port.PlatformStore { return platformStore{s} }

type platformStore struct{ s *Store }

func (p platformStore) targets() *memtx.Table[string, model.AccessFederationTarget] {
	return memtx.TableOf[string, model.AccessFederationTarget](p.s.Store, tAccessTargets)
}

func (p platformStore) feds() *memtx.Table[string, model.PlatformFederation] {
	return memtx.TableOf[string, model.PlatformFederation](p.s.Store, tPlatformFeds)
}

func (p platformStore) topologies() *memtx.Table[string, model.Topology] {
	return memtx.TableOf[string, model.Topology](p.s.Store, tTopologies)
}

func (p platformStore) PutAccessTarget(ctx context.Context, t model.AccessFederationTarget) error {
	return p.s.Do(ctx, func() error {
		p.targets().Put(t.Name, t)
		return nil
	})
}

func (p platformStore) AccessTarget(
	ctx context.Context,
	name string,
) (model.AccessFederationTarget, error) {
	var out model.AccessFederationTarget
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.targets().Get(name); !ok {
			return memtx.NotFound("access federation target", name)
		}
		return nil
	})
	return out, err
}

func (p platformStore) AccessTargets(ctx context.Context) ([]model.AccessFederationTarget, error) {
	var out []model.AccessFederationTarget
	err := p.s.Do(ctx, func() error {
		out = p.targets().
			All(nil, func(a, b model.AccessFederationTarget) int { return strings.Compare(a.Name, b.Name) })
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

func (p platformStore) PutFederation(ctx context.Context, f model.PlatformFederation) error {
	return p.s.Do(ctx, func() error {
		p.feds().Put(f.ID, f)
		return nil
	})
}

func (p platformStore) Federation(
	ctx context.Context,
	id string,
) (model.PlatformFederation, error) {
	var out model.PlatformFederation
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.feds().Get(id); !ok {
			return memtx.NotFound("platform federation", id)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Federations(ctx context.Context) ([]model.PlatformFederation, error) {
	var out []model.PlatformFederation
	err := p.s.Do(ctx, func() error {
		out = p.feds().
			All(nil, func(a, b model.PlatformFederation) int { return strings.Compare(a.ID, b.ID) })
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

func (p platformStore) PutTopology(ctx context.Context, t model.Topology) error {
	return p.s.Do(ctx, func() error {
		if t.Sites != nil {
			t.Sites = slices.Clone(t.Sites)
			slices.SortFunc(
				t.Sites,
				func(a, b model.TopologySite) int { return strings.Compare(a.SiteName, b.SiteName) },
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

func (p platformStore) Topology(ctx context.Context, key string) (model.Topology, error) {
	var out model.Topology
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.topologies().Get(key); !ok {
			return memtx.NotFound("topology", key)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Topologies(ctx context.Context) ([]model.Topology, error) {
	var out []model.Topology
	err := p.s.Do(ctx, func() error {
		out = p.topologies().
			All(nil, func(a, b model.Topology) int { return strings.Compare(a.Key, b.Key) })
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
