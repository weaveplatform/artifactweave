package store

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// InstanceStore persists this instance's peer identity (decision 0021), a
// singleton created at first start. The private key is sealed.
type InstanceStore interface {
	// Init stores the identity; ErrConflict when one exists.
	Init(ctx context.Context, id types.InstanceIdentity) error
	// Get returns the identity.
	Get(ctx context.Context) (types.InstanceIdentity, error)
	// Update replaces the identity, for key rotation.
	Update(ctx context.Context, id types.InstanceIdentity) error
}

// PeerFilter narrows peer listings.
type PeerFilter struct {
	UseCase types.UseCase
}

// PeerStore persists paired peers and outstanding pairing nonces.
type PeerStore interface {
	// Put creates or replaces the peer with p.InstanceID. UseCases are a
	// set: duplicates are dropped and Get and List return them sorted.
	Put(ctx context.Context, p types.Peer) error
	// Get returns the peer.
	Get(ctx context.Context, instanceID string) (types.Peer, error)
	// List returns matching peers ordered by instance ID.
	List(ctx context.Context, f PeerFilter) ([]types.Peer, error)
	// Delete removes the peer.
	Delete(ctx context.Context, instanceID string) error

	// AddNonce stores an issued pairing nonce; ErrConflict when its hash
	// exists.
	AddNonce(ctx context.Context, n types.PairingNonce) error
	// RedeemNonce marks the nonce redeemed at now and returns it. A nonce is
	// single-use: ErrNotFound when unknown or expired at now, ErrConflict
	// when already redeemed.
	RedeemNonce(ctx context.Context, hash []byte, now time.Time) (types.PairingNonce, error)
	// PruneNonces removes nonces that expired before t and returns how many.
	PruneNonces(ctx context.Context, before time.Time) (int64, error)
}

// OutboxStore persists the ordered event streams of decision 0021: this
// instance's outbox, each peer's acknowledged position in it, the receiver's
// applied positions, and blob fetches that failed while applying.
type OutboxStore interface {
	// Append stores e as the next entry of (e.Origin, e.Stream) and returns
	// it with Seq assigned, one more than the stream's last. Call it inside
	// the transaction that makes the change it records; concurrent appends
	// to one stream are serialised, never given the same Seq.
	Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error)
	// Read returns up to limit entries of a stream with Seq above after, in
	// Seq order.
	Read(
		ctx context.Context,
		origin, stream string,
		after int64,
		limit int,
	) ([]types.OutboxEntry, error)
	// LastSeq returns the highest Seq of a stream, 0 when it has none.
	LastSeq(ctx context.Context, origin, stream string) (int64, error)
	// Streams returns the streams origin has entries or a sequence in,
	// ordered.
	Streams(ctx context.Context, origin string) ([]string, error)
	// Prune removes entries of a stream with Seq at most upTo and returns how
	// many. The stream's sequence is not reset.
	Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error)

	// PutCursor creates or replaces a peer's position in a stream.
	PutCursor(ctx context.Context, c types.PeerCursor) error
	// Cursor returns a peer's position in a stream.
	Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error)
	// Cursors returns a peer's positions ("" for every peer) ordered by
	// (peer, stream).
	Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error)
	// DeleteCursors removes every position of a peer; a peer with none is
	// not an error.
	DeleteCursors(ctx context.Context, peer string) error

	// Applied returns the receiver's position in a peer's stream; a zero
	// LastSeq and no error when nothing was applied yet.
	Applied(ctx context.Context, origin, stream string) (types.AppliedMarker, error)
	// SetApplied records the position, in the transaction that applies the
	// entry, which is what makes applying idempotent.
	SetApplied(ctx context.Context, m types.AppliedMarker) error

	// RecordFailedFetch stores a failed blob fetch with Failures 1, or
	// increments Failures and replaces LastError of the existing one, keeping
	// its other fields, and returns the record as stored. Failures counts
	// the calls; the value in f is ignored.
	RecordFailedFetch(ctx context.Context, f types.FailedBlobFetch) (types.FailedBlobFetch, error)
	// FailedFetches returns a stream's failed fetches ("" for every stream)
	// ordered by (stream, sha256).
	FailedFetches(ctx context.Context, stream string) ([]types.FailedBlobFetch, error)
	// ClearFailedFetch removes one after a successful retry; ErrNotFound
	// when there is none.
	ClearFailedFetch(ctx context.Context, stream, sha256 string) error
}

// FederationStore persists federated repository membership (decision 0022)
// and full-sync runs.
type FederationStore interface {
	// SetMembers replaces a repository's members; each member's RepoKey is
	// set to repoKey. ErrInvalid when a member has no PeerInstanceID
	// (members are resolved to peers first), ErrConflict when two name the
	// same peer.
	SetMembers(ctx context.Context, repoKey string, members []types.FederationMember) error
	// Members returns a repository's members ordered by peer.
	Members(ctx context.Context, repoKey string) ([]types.FederationMember, error)
	// MembersWithPeer returns every membership naming peer, ordered by
	// repository.
	MembersWithPeer(ctx context.Context, peer string) ([]types.FederationMember, error)

	// CreateFullSync stores r and returns it with its assigned ID.
	CreateFullSync(ctx context.Context, r types.FullSyncRun) (types.FullSyncRun, error)
	// FullSync returns the run with id.
	FullSync(ctx context.Context, id int64) (types.FullSyncRun, error)
	// UpdateFullSync replaces a run.
	UpdateFullSync(ctx context.Context, r types.FullSyncRun) error
	// FullSyncs returns a repository's runs ("" for all) newest first.
	FullSyncs(ctx context.Context, repoKey string, p Page) ([]types.FullSyncRun, string, error)
}

// PlatformStore persists Access Federation targets, platform federations
// and topologies (decision 0025).
type PlatformStore interface {
	// PutAccessTarget creates or replaces the target with t.Name.
	PutAccessTarget(ctx context.Context, t types.AccessFederationTarget) error
	// AccessTarget returns one target.
	AccessTarget(ctx context.Context, name string) (types.AccessFederationTarget, error)
	// AccessTargets returns every target ordered by name.
	AccessTargets(ctx context.Context) ([]types.AccessFederationTarget, error)
	// DeleteAccessTarget removes a target.
	DeleteAccessTarget(ctx context.Context, name string) error

	// PutFederation creates or replaces the federation with f.ID.
	PutFederation(ctx context.Context, f types.PlatformFederation) error
	// Federation returns one federation.
	Federation(ctx context.Context, id string) (types.PlatformFederation, error)
	// Federations returns every federation ordered by ID.
	Federations(ctx context.Context) ([]types.PlatformFederation, error)
	// DeleteFederation removes a federation.
	DeleteFederation(ctx context.Context, id string) error

	// PutTopology creates or replaces the topology with t.Key and its sites;
	// ErrConflict when two sites share a name.
	PutTopology(ctx context.Context, t types.Topology) error
	// Topology returns one topology with its sites ordered by site name.
	Topology(ctx context.Context, key string) (types.Topology, error)
	// Topologies returns every topology ordered by key.
	Topologies(ctx context.Context) ([]types.Topology, error)
	// DeleteTopology removes a topology and its sites.
	DeleteTopology(ctx context.Context, key string) error
}
