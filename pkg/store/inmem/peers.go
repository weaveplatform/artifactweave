package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// Table names of the peers group; the same names as the SQL tables.
const (
	tInstance      = "instance"
	tPeers         = "peers"
	tPairingNonces = "pairing_nonces"
	tOutbox        = "outbox"
	tOutboxSeqs    = "outbox_sequences"
	tPeerCursors   = "peer_cursors"
	tApplied       = "applied_markers"
	tFailedFetches = "failed_blob_fetches"
	tFedMembers    = "federation_members"
	tFullSyncs     = "full_sync_runs"
	tAccessTargets = "access_federation_targets"
	tPlatformFeds  = "platform_federations"
	tTopologies    = "topologies"
	instanceRowKey = 1
)

// peersPair identifies one stream of one origin, or one peer's position in
// a stream.
type peersPair struct{ a, b string }

// outboxKey identifies one outbox entry.
type outboxKey struct {
	origin, stream string
	seq            int64
}

// Instance implements store.Store.
func (s *Store) Instance() store.InstanceStore { return instanceStore{s} }

type instanceStore struct{ s *Store }

func (i instanceStore) t() *table[int, types.InstanceIdentity] {
	return tableOf[int, types.InstanceIdentity](i.s, tInstance)
}

func (i instanceStore) Init(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.do(ctx, func() error {
		if i.t().has(instanceRowKey) {
			return conflict("instance identity", id.InstanceID)
		}
		i.t().put(instanceRowKey, id)
		return nil
	})
}

func (i instanceStore) Get(ctx context.Context) (types.InstanceIdentity, error) {
	var out types.InstanceIdentity
	err := i.s.do(ctx, func() error {
		var ok bool
		if out, ok = i.t().get(instanceRowKey); !ok {
			return notFound("instance identity", "")
		}
		return nil
	})
	return out, err
}

func (i instanceStore) Update(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.do(ctx, func() error {
		if !i.t().has(instanceRowKey) {
			return notFound("instance identity", "")
		}
		i.t().put(instanceRowKey, id)
		return nil
	})
}

// Peers implements store.Store.
func (s *Store) Peers() store.PeerStore { return peerStore{s} }

type peerStore struct{ s *Store }

func (p peerStore) t() *table[string, types.Peer] { return tableOf[string, types.Peer](p.s, tPeers) }

func (p peerStore) nonces() *table[string, types.PairingNonce] {
	return tableOf[string, types.PairingNonce](p.s, tPairingNonces)
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
	return p.s.do(ctx, func() error {
		peer.UseCases = useCaseSet(peer.UseCases)
		p.t().put(peer.InstanceID, peer)
		return nil
	})
}

func (p peerStore) Get(ctx context.Context, instanceID string) (types.Peer, error) {
	var out types.Peer
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.t().get(instanceID); !ok {
			return notFound("peer", instanceID)
		}
		return nil
	})
	return out, err
}

func (p peerStore) List(ctx context.Context, f store.PeerFilter) ([]types.Peer, error) {
	var out []types.Peer
	err := p.s.do(ctx, func() error {
		out = p.t().all(func(_ string, v types.Peer) bool {
			return f.UseCase == "" || v.Has(f.UseCase)
		}, func(a, b types.Peer) int { return strings.Compare(a.InstanceID, b.InstanceID) })
		return nil
	})
	return out, err
}

func (p peerStore) Delete(ctx context.Context, instanceID string) error {
	return p.s.do(ctx, func() error {
		if !p.t().del(instanceID) {
			return notFound("peer", instanceID)
		}
		return nil
	})
}

func (p peerStore) AddNonce(ctx context.Context, n types.PairingNonce) error {
	return p.s.do(ctx, func() error {
		k := string(n.NonceHash)
		if p.nonces().has(k) {
			return conflict("pairing nonce", fmt.Sprintf("%x", n.NonceHash))
		}
		p.nonces().put(k, n)
		return nil
	})
}

func (p peerStore) RedeemNonce(
	ctx context.Context,
	hash []byte,
	now time.Time,
) (types.PairingNonce, error) {
	var out types.PairingNonce
	err := p.s.do(ctx, func() error {
		n, ok := p.nonces().get(string(hash))
		switch {
		case !ok:
			return notFound("pairing nonce", fmt.Sprintf("%x", hash))
		case !n.RedeemedAt.IsZero():
			return conflict("pairing nonce already redeemed", fmt.Sprintf("%x", hash))
		case !store.Normalise(now).Before(n.ExpiresAt):
			return notFound("pairing nonce expired", fmt.Sprintf("%x", hash))
		}
		n.RedeemedAt = now
		p.nonces().put(string(hash), n)
		out, _ = p.nonces().get(string(hash))
		return nil
	})
	return out, err
}

func (p peerStore) PruneNonces(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := p.s.do(ctx, func() error {
		before = store.Normalise(before)
		n = p.nonces().deleteWhere(func(_ string, v types.PairingNonce) bool {
			return v.ExpiresAt.Before(before)
		})
		return nil
	})
	return n, err
}

// Outbox implements store.Store.
func (s *Store) Outbox() store.OutboxStore { return outboxStore{s} }

type outboxStore struct{ s *Store }

func (o outboxStore) entries() *table[outboxKey, types.OutboxEntry] {
	return tableOf[outboxKey, types.OutboxEntry](o.s, tOutbox)
}

func (o outboxStore) seqs() *table[peersPair, int64] {
	return tableOf[peersPair, int64](o.s, tOutboxSeqs)
}

func (o outboxStore) cursors() *table[peersPair, types.PeerCursor] {
	return tableOf[peersPair, types.PeerCursor](o.s, tPeerCursors)
}

func (o outboxStore) applied() *table[peersPair, types.AppliedMarker] {
	return tableOf[peersPair, types.AppliedMarker](o.s, tApplied)
}

func (o outboxStore) failed() *table[peersPair, types.FailedBlobFetch] {
	return tableOf[peersPair, types.FailedBlobFetch](o.s, tFailedFetches)
}

func (o outboxStore) Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error) {
	var out types.OutboxEntry
	err := o.s.do(ctx, func() error {
		k := peersPair{e.Origin, e.Stream}
		seq, _ := o.seqs().get(k)
		seq++
		o.seqs().put(k, seq)
		e.Seq = seq
		o.entries().put(outboxKey{e.Origin, e.Stream, seq}, e)
		out, _ = o.entries().get(outboxKey{e.Origin, e.Stream, seq})
		return nil
	})
	return out, err
}

func (o outboxStore) Read(
	ctx context.Context, origin, stream string, after int64, limit int,
) ([]types.OutboxEntry, error) {
	var out []types.OutboxEntry
	err := o.s.do(ctx, func() error {
		out = o.entries().all(func(k outboxKey, _ types.OutboxEntry) bool {
			return k.origin == origin && k.stream == stream && k.seq > after
		}, func(a, b types.OutboxEntry) int { return cmp.Compare(a.Seq, b.Seq) })
		out = out[:min(len(out), peersLimit(limit))]
		return nil
	})
	return out, err
}

// peersLimit applies the default limit for "at most limit" reads.
func peersLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

func (o outboxStore) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	var seq int64
	err := o.s.do(ctx, func() error {
		seq, _ = o.seqs().get(peersPair{origin, stream})
		return nil
	})
	return seq, err
}

func (o outboxStore) Streams(ctx context.Context, origin string) ([]string, error) {
	var out []string
	err := o.s.do(ctx, func() error {
		for k := range o.seqs().rows {
			if k.a == origin {
				out = append(out, k.b)
			}
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

func (o outboxStore) Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error) {
	var n int64
	err := o.s.do(ctx, func() error {
		n = o.entries().deleteWhere(func(k outboxKey, _ types.OutboxEntry) bool {
			return k.origin == origin && k.stream == stream && k.seq <= upTo
		})
		return nil
	})
	return n, err
}

func (o outboxStore) PutCursor(ctx context.Context, c types.PeerCursor) error {
	return o.s.do(ctx, func() error {
		o.cursors().put(peersPair{c.PeerInstanceID, c.Stream}, c)
		return nil
	})
}

func (o outboxStore) Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error) {
	var out types.PeerCursor
	err := o.s.do(ctx, func() error {
		var ok bool
		if out, ok = o.cursors().get(peersPair{peer, stream}); !ok {
			return notFound("peer cursor", peer+" "+stream)
		}
		return nil
	})
	return out, err
}

func (o outboxStore) Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error) {
	var out []types.PeerCursor
	err := o.s.do(ctx, func() error {
		out = o.cursors().all(func(k peersPair, _ types.PeerCursor) bool {
			return peer == "" || k.a == peer
		}, func(a, b types.PeerCursor) int {
			return cmp.Or(
				strings.Compare(a.PeerInstanceID, b.PeerInstanceID),
				strings.Compare(a.Stream, b.Stream),
			)
		})
		return nil
	})
	return out, err
}

func (o outboxStore) DeleteCursors(ctx context.Context, peer string) error {
	return o.s.do(ctx, func() error {
		o.cursors().deleteWhere(func(k peersPair, _ types.PeerCursor) bool { return k.a == peer })
		return nil
	})
}

func (o outboxStore) Applied(
	ctx context.Context,
	origin, stream string,
) (types.AppliedMarker, error) {
	out := types.AppliedMarker{Origin: origin, Stream: stream}
	err := o.s.do(ctx, func() error {
		if m, ok := o.applied().get(peersPair{origin, stream}); ok {
			out = m
		}
		return nil
	})
	return out, err
}

func (o outboxStore) SetApplied(ctx context.Context, m types.AppliedMarker) error {
	return o.s.do(ctx, func() error {
		o.applied().put(peersPair{m.Origin, m.Stream}, m)
		return nil
	})
}

func (o outboxStore) RecordFailedFetch(
	ctx context.Context,
	f types.FailedBlobFetch,
) (types.FailedBlobFetch, error) {
	var out types.FailedBlobFetch
	err := o.s.do(ctx, func() error {
		k := peersPair{f.Stream, f.SHA256}
		cur, ok := o.failed().get(k)
		if ok {
			cur.Failures++
			cur.LastError = f.LastError
		} else {
			cur = f
			cur.Failures = 1
		}
		o.failed().put(k, cur)
		out, _ = o.failed().get(k)
		return nil
	})
	return out, err
}

func (o outboxStore) FailedFetches(
	ctx context.Context,
	stream string,
) ([]types.FailedBlobFetch, error) {
	var out []types.FailedBlobFetch
	err := o.s.do(ctx, func() error {
		out = o.failed().all(func(k peersPair, _ types.FailedBlobFetch) bool {
			return stream == "" || k.a == stream
		}, func(a, b types.FailedBlobFetch) int {
			return cmp.Or(strings.Compare(a.Stream, b.Stream), strings.Compare(a.SHA256, b.SHA256))
		})
		return nil
	})
	return out, err
}

func (o outboxStore) ClearFailedFetch(ctx context.Context, stream, sha256 string) error {
	return o.s.do(ctx, func() error {
		if !o.failed().del(peersPair{stream, sha256}) {
			return notFound("failed blob fetch", stream+" "+sha256)
		}
		return nil
	})
}

// Federation implements store.Store.
func (s *Store) Federation() store.FederationStore { return federationStore{s} }

type federationStore struct{ s *Store }

func (f federationStore) members() *table[peersPair, types.FederationMember] {
	return tableOf[peersPair, types.FederationMember](f.s, tFedMembers)
}

func (f federationStore) runs() *table[int64, types.FullSyncRun] {
	return tableOf[int64, types.FullSyncRun](f.s, tFullSyncs)
}

func (f federationStore) SetMembers(
	ctx context.Context,
	repoKey string,
	members []types.FederationMember,
) error {
	return f.s.do(ctx, func() error {
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
				return conflict("federation member of "+repoKey, m.PeerInstanceID)
			}
			seen[m.PeerInstanceID] = true
		}
		f.members().
			deleteWhere(func(k peersPair, _ types.FederationMember) bool { return k.a == repoKey })
		for _, m := range members {
			m.RepoKey = repoKey
			f.members().put(peersPair{repoKey, m.PeerInstanceID}, m)
		}
		return nil
	})
}

func (f federationStore) Members(
	ctx context.Context,
	repoKey string,
) ([]types.FederationMember, error) {
	var out []types.FederationMember
	err := f.s.do(ctx, func() error {
		out = f.members().
			all(func(k peersPair, _ types.FederationMember) bool { return k.a == repoKey },
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
	err := f.s.do(ctx, func() error {
		out = f.members().
			all(func(k peersPair, _ types.FederationMember) bool { return k.b == peer },
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
	err := f.s.do(ctx, func() error {
		r.ID = f.s.nextID(tFullSyncs)
		f.runs().put(r.ID, r)
		out, _ = f.runs().get(r.ID)
		return nil
	})
	return out, err
}

func (f federationStore) FullSync(ctx context.Context, id int64) (types.FullSyncRun, error) {
	var out types.FullSyncRun
	err := f.s.do(ctx, func() error {
		var ok bool
		if out, ok = f.runs().get(id); !ok {
			return notFound("full sync run", id)
		}
		return nil
	})
	return out, err
}

func (f federationStore) UpdateFullSync(ctx context.Context, r types.FullSyncRun) error {
	return f.s.do(ctx, func() error {
		if !f.runs().has(r.ID) {
			return notFound("full sync run", r.ID)
		}
		f.runs().put(r.ID, r)
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
	err := f.s.do(ctx, func() error {
		rows := f.runs().all(func(_ int64, v types.FullSyncRun) bool {
			return repoKey == "" || v.RepoKey == repoKey
		}, func(a, b types.FullSyncRun) int { return cmp.Compare(b.ID, a.ID) })
		var err error
		out, next, err = paginate(
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

func (p platformStore) targets() *table[string, types.AccessFederationTarget] {
	return tableOf[string, types.AccessFederationTarget](p.s, tAccessTargets)
}

func (p platformStore) feds() *table[string, types.PlatformFederation] {
	return tableOf[string, types.PlatformFederation](p.s, tPlatformFeds)
}

func (p platformStore) topologies() *table[string, types.Topology] {
	return tableOf[string, types.Topology](p.s, tTopologies)
}

func (p platformStore) PutAccessTarget(ctx context.Context, t types.AccessFederationTarget) error {
	return p.s.do(ctx, func() error {
		p.targets().put(t.Name, t)
		return nil
	})
}

func (p platformStore) AccessTarget(
	ctx context.Context,
	name string,
) (types.AccessFederationTarget, error) {
	var out types.AccessFederationTarget
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.targets().get(name); !ok {
			return notFound("access federation target", name)
		}
		return nil
	})
	return out, err
}

func (p platformStore) AccessTargets(ctx context.Context) ([]types.AccessFederationTarget, error) {
	var out []types.AccessFederationTarget
	err := p.s.do(ctx, func() error {
		out = p.targets().
			all(nil, func(a, b types.AccessFederationTarget) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteAccessTarget(ctx context.Context, name string) error {
	return p.s.do(ctx, func() error {
		if !p.targets().del(name) {
			return notFound("access federation target", name)
		}
		return nil
	})
}

func (p platformStore) PutFederation(ctx context.Context, f types.PlatformFederation) error {
	return p.s.do(ctx, func() error {
		p.feds().put(f.ID, f)
		return nil
	})
}

func (p platformStore) Federation(
	ctx context.Context,
	id string,
) (types.PlatformFederation, error) {
	var out types.PlatformFederation
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.feds().get(id); !ok {
			return notFound("platform federation", id)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Federations(ctx context.Context) ([]types.PlatformFederation, error) {
	var out []types.PlatformFederation
	err := p.s.do(ctx, func() error {
		out = p.feds().
			all(nil, func(a, b types.PlatformFederation) int { return strings.Compare(a.ID, b.ID) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteFederation(ctx context.Context, id string) error {
	return p.s.do(ctx, func() error {
		if !p.feds().del(id) {
			return notFound("platform federation", id)
		}
		return nil
	})
}

func (p platformStore) PutTopology(ctx context.Context, t types.Topology) error {
	return p.s.do(ctx, func() error {
		if t.Sites != nil {
			t.Sites = slices.Clone(t.Sites)
			slices.SortFunc(
				t.Sites,
				func(a, b types.TopologySite) int { return strings.Compare(a.SiteName, b.SiteName) },
			)
			for i := 1; i < len(t.Sites); i++ {
				if t.Sites[i].SiteName == t.Sites[i-1].SiteName {
					return conflict("topology "+t.Key+" site", t.Sites[i].SiteName)
				}
			}
		}
		p.topologies().put(t.Key, t)
		return nil
	})
}

func (p platformStore) Topology(ctx context.Context, key string) (types.Topology, error) {
	var out types.Topology
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.topologies().get(key); !ok {
			return notFound("topology", key)
		}
		return nil
	})
	return out, err
}

func (p platformStore) Topologies(ctx context.Context) ([]types.Topology, error) {
	var out []types.Topology
	err := p.s.do(ctx, func() error {
		out = p.topologies().
			all(nil, func(a, b types.Topology) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (p platformStore) DeleteTopology(ctx context.Context, key string) error {
	return p.s.do(ctx, func() error {
		if !p.topologies().del(key) {
			return notFound("topology", key)
		}
		return nil
	})
}
