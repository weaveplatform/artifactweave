// Bridge (P9d): declared in migration/federation/core/model; PRO-02 deletes it.

package types

import fedmodel "github.com/weaveplatform/artifactweave/migration/federation/core/model"

var (
	ErrInvalidMemberURL = fedmodel.ErrInvalidMemberURL
	ErrInvalidStream    = fedmodel.ErrInvalidStream
)

var (
	ParseMemberURL     = fedmodel.ParseMemberURL
	MembersOf          = fedmodel.MembersOf
	DeriveSyncStatus   = fedmodel.DeriveSyncStatus
	KnownUseCase       = fedmodel.KnownUseCase
	RepositoryStream   = fedmodel.RepositoryStream
	DistributionStream = fedmodel.DistributionStream
	ParseStream        = fedmodel.ParseStream
	DirectionFor       = fedmodel.DirectionFor
)

type (
	FederationMember       = fedmodel.FederationMember
	FederationPriority     = fedmodel.FederationPriority
	FederationSettings     = fedmodel.FederationSettings
	SyncStatus             = fedmodel.SyncStatus
	FullSyncRun            = fedmodel.FullSyncRun
	UseCase                = fedmodel.UseCase
	PeerStatus             = fedmodel.PeerStatus
	Peer                   = fedmodel.Peer
	PairingNonce           = fedmodel.PairingNonce
	Stamp                  = fedmodel.Stamp
	StreamKind             = fedmodel.StreamKind
	FederatedEntity        = fedmodel.FederatedEntity
	PatternFilter          = fedmodel.PatternFilter
	AccessFederationTarget = fedmodel.AccessFederationTarget
	PlatformFederation     = fedmodel.PlatformFederation
	TopologySite           = fedmodel.TopologySite
	Topology               = fedmodel.Topology
	Replication            = fedmodel.Replication
	ReplicationDirection   = fedmodel.ReplicationDirection
	ReplicationStatus      = fedmodel.ReplicationStatus
	ReplicationRun         = fedmodel.ReplicationRun
)

const (
	PriorityHigh              = fedmodel.PriorityHigh
	PriorityStandard          = fedmodel.PriorityStandard
	SyncSyncing               = fedmodel.SyncSyncing
	SyncFullSyncRunning       = fedmodel.SyncFullSyncRunning
	SyncDisabled              = fedmodel.SyncDisabled
	SyncOutOfSync             = fedmodel.SyncOutOfSync
	UseFederation             = fedmodel.UseFederation
	UseDistribution           = fedmodel.UseDistribution
	UseAccessFederation       = fedmodel.UseAccessFederation
	UsePlatformFederation     = fedmodel.UsePlatformFederation
	PeerOnline                = fedmodel.PeerOnline
	PeerOffline               = fedmodel.PeerOffline
	PeerPartial               = fedmodel.PeerPartial
	PeerUnauthorized          = fedmodel.PeerUnauthorized
	PairingTokenTTL           = fedmodel.PairingTokenTTL
	StreamRepository          = fedmodel.StreamRepository
	StreamAccess              = fedmodel.StreamAccess
	StreamDistribution        = fedmodel.StreamDistribution
	AccessStream              = fedmodel.AccessStream
	EntityUsers               = fedmodel.EntityUsers
	EntityGroups              = fedmodel.EntityGroups
	EntityPermissions         = fedmodel.EntityPermissions
	EntityTokens              = fedmodel.EntityTokens
	ReplicationPush           = fedmodel.ReplicationPush
	ReplicationPull           = fedmodel.ReplicationPull
	ReplicationUnknown        = fedmodel.ReplicationUnknown
	ReplicationNeverRun       = fedmodel.ReplicationNeverRun
	ReplicationIncomplete     = fedmodel.ReplicationIncomplete
	ReplicationError          = fedmodel.ReplicationError
	ReplicationWarn           = fedmodel.ReplicationWarn
	ReplicationOK             = fedmodel.ReplicationOK
	ReplicationInconsistent   = fedmodel.ReplicationInconsistent
	ReplicationPartialFailure = fedmodel.ReplicationPartialFailure
)
