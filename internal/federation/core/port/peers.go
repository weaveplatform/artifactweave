package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PeerFilter narrows peer listings.
type PeerFilter struct {
	UseCase model.UseCase
}

// PeerStore persists paired peers and outstanding pairing nonces.
type PeerStore interface {
	// Put creates or replaces the peer with p.InstanceID. UseCases are a
	// set: duplicates are dropped and Get and List return them sorted.
	Put(ctx context.Context, p model.Peer) error
	// Get returns the peer.
	Get(ctx context.Context, instanceID string) (model.Peer, error)
	// List returns matching peers ordered by instance ID.
	List(ctx context.Context, f PeerFilter) ([]model.Peer, error)
	// Delete removes the peer.
	Delete(ctx context.Context, instanceID string) error

	// AddNonce stores an issued pairing nonce; kernel.ErrConflict when its
	// hash exists.
	AddNonce(ctx context.Context, n model.PairingNonce) error
	// RedeemNonce marks the nonce redeemed at now and returns it. A nonce is
	// single-use: kernel.ErrNotFound when unknown or expired at now,
	// kernel.ErrConflict when already redeemed.
	RedeemNonce(ctx context.Context, hash []byte, now time.Time) (model.PairingNonce, error)
	// PruneNonces removes nonces that expired before t and returns how many.
	PruneNonces(ctx context.Context, before time.Time) (int64, error)
}

// FederationStore persists federated repository membership (decision 0022)
// and full-sync runs.
type FederationStore interface {
	// SetMembers replaces a repository's members; each member's RepoKey is
	// set to repoKey. kernel.ErrInvalid when a member has no PeerInstanceID
	// (members are resolved to peers first), kernel.ErrConflict when two
	// name the same peer.
	SetMembers(ctx context.Context, repoKey string, members []model.FederationMember) error
	// Members returns a repository's members ordered by peer.
	Members(ctx context.Context, repoKey string) ([]model.FederationMember, error)
	// MembersWithPeer returns every membership naming peer, ordered by
	// repository.
	MembersWithPeer(ctx context.Context, peer string) ([]model.FederationMember, error)

	// CreateFullSync stores r and returns it with its assigned ID.
	CreateFullSync(ctx context.Context, r model.FullSyncRun) (model.FullSyncRun, error)
	// FullSync returns the run with id.
	FullSync(ctx context.Context, id int64) (model.FullSyncRun, error)
	// UpdateFullSync replaces a run.
	UpdateFullSync(ctx context.Context, r model.FullSyncRun) error
	// FullSyncs returns a repository's runs ("" for all) newest first.
	FullSyncs(
		ctx context.Context,
		repoKey string,
		p kernel.Page,
	) ([]model.FullSyncRun, string, error)
}

// PlatformStore persists Access Federation targets, platform federations
// and topologies (decision 0025).
type PlatformStore interface {
	// PutAccessTarget creates or replaces the target with t.Name.
	PutAccessTarget(ctx context.Context, t model.AccessFederationTarget) error
	// AccessTarget returns one target.
	AccessTarget(ctx context.Context, name string) (model.AccessFederationTarget, error)
	// AccessTargets returns every target ordered by name.
	AccessTargets(ctx context.Context) ([]model.AccessFederationTarget, error)
	// DeleteAccessTarget removes a target.
	DeleteAccessTarget(ctx context.Context, name string) error

	// PutFederation creates or replaces the federation with f.ID.
	PutFederation(ctx context.Context, f model.PlatformFederation) error
	// Federation returns one federation.
	Federation(ctx context.Context, id string) (model.PlatformFederation, error)
	// Federations returns every federation ordered by ID.
	Federations(ctx context.Context) ([]model.PlatformFederation, error)
	// DeleteFederation removes a federation.
	DeleteFederation(ctx context.Context, id string) error

	// PutTopology creates or replaces the topology with t.Key and its sites;
	// kernel.ErrConflict when two sites share a name.
	PutTopology(ctx context.Context, t model.Topology) error
	// Topology returns one topology with its sites ordered by site name.
	Topology(ctx context.Context, key string) (model.Topology, error)
	// Topologies returns every topology ordered by key.
	Topologies(ctx context.Context) ([]model.Topology, error)
	// DeleteTopology removes a topology and its sites.
	DeleteTopology(ctx context.Context, key string) error
}
