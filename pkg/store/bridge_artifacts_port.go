// Bridge (P9d): declared in migration/artifacts/core/port; PRO-02 deletes it.

package store

import artport "github.com/weaveplatform/artifactweave/migration/artifacts/core/port"

var ErrDeleting = artport.ErrDeleting

var (
	NormaliseBuild         = artport.NormaliseBuild
	CleanProperties        = artport.CleanProperties
	CheckPropertyKeys      = artport.CheckPropertyKeys
	CheckBuildPropertyKeys = artport.CheckBuildPropertyKeys
	NewRemoteSource        = artport.NewRemoteSource
	RemoteConfigChanged    = artport.RemoteConfigChanged
	RemoteSourceConfig     = artport.RemoteSourceConfig
	RemotePolicyChanged    = artport.RemotePolicyChanged
	RemoteCacheID          = artport.RemoteCacheID
	ValidateRemoteKey      = artport.ValidateRemoteKey
	RemotePathMatches      = artport.RemotePathMatches
	ValidateRemoteEntry    = artport.ValidateRemoteEntry
)

type (
	ArchivedFilter         = artport.ArchivedFilter
	ArchivedItemStore      = artport.ArchivedItemStore
	BowerRegistrationStore = artport.BowerRegistrationStore
	BuildStore             = artport.BuildStore
	RepoFilter             = artport.RepoFilter
	RepoStore              = artport.RepoStore
	Download               = artport.Download
	DeleteResult           = artport.DeleteResult
	RepoUsage              = artport.RepoUsage
	ItemStore              = artport.ItemStore
	BlobIndex              = artport.BlobIndex
	ReplicaIndex           = artport.ReplicaIndex
	TrashStore             = artport.TrashStore
	PackageStore           = artport.PackageStore
	GitLFSLockFilter       = artport.GitLFSLockFilter
	GitLFSLockStore        = artport.GitLFSLockStore
	RemoteCacheStore       = artport.RemoteCacheStore
)
