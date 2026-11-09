// Bridge (P9d): declared in migration/artifacts/core/model; PRO-02 deletes it.

package types

import artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"

var (
	ErrInvalidPropertyKey = artmodel.ErrInvalidPropertyKey
	ErrPropertyKeyTooLong = artmodel.ErrPropertyKeyTooLong
)

var (
	BuildInfoRepo          = artmodel.BuildInfoRepo
	EncodeBuildInfoSegment = artmodel.EncodeBuildInfoSegment
	RootItem               = artmodel.RootItem
	ResolveRepoKey         = artmodel.ResolveRepoKey
	ValidatePropertyKey    = artmodel.ValidatePropertyKey
	CheckPropertyKeyLength = artmodel.CheckPropertyKeyLength
	TruncatePropertyValue  = artmodel.TruncatePropertyValue
	ComparatorFor          = artmodel.ComparatorFor
	SortVersions           = artmodel.SortVersions
	CompareSemver          = artmodel.CompareSemver
	CompareNuGet           = artmodel.CompareNuGet
	ComparePEP440          = artmodel.ComparePEP440
	CompareMaven           = artmodel.CompareMaven
	CompareMercury         = artmodel.CompareMercury
	AssignOrderKeys        = artmodel.AssignOrderKeys
)

type (
	ArchivedItem           = artmodel.ArchivedItem
	BowerRegistration      = artmodel.BowerRegistration
	Build                  = artmodel.Build
	BuildRun               = artmodel.BuildRun
	BuildName              = artmodel.BuildName
	BuildModule            = artmodel.BuildModule
	BuildFile              = artmodel.BuildFile
	BuildFileRef           = artmodel.BuildFileRef
	BuildPromotion         = artmodel.BuildPromotion
	BuildRetention         = artmodel.BuildRetention
	GitLFSLock             = artmodel.GitLFSLock
	Item                   = artmodel.Item
	Properties             = artmodel.Properties
	ItemStats              = artmodel.ItemStats
	TrashRecord            = artmodel.TrashRecord
	TrashEntry             = artmodel.TrashEntry
	KeypairRole            = artmodel.KeypairRole
	Matrix                 = artmodel.Matrix
	RemoteKind             = artmodel.RemoteKind
	RemoteCacheKey         = artmodel.RemoteCacheKey
	RemoteCacheToken       = artmodel.RemoteCacheToken
	RemoteCacheEntry       = artmodel.RemoteCacheEntry
	RemoteSmartState       = artmodel.RemoteSmartState
	RemoteSourceState      = artmodel.RemoteSourceState
	RemoteSmartPeer        = artmodel.RemoteSmartPeer
	TrashcanSettings       = artmodel.TrashcanSettings
	FolderDownloadSettings = artmodel.FolderDownloadSettings
	RepositoryIncarnation  = artmodel.RepositoryIncarnation
	SourceGeneration       = artmodel.SourceGeneration
	SourceKind             = artmodel.SourceKind
	SourceIdentity         = artmodel.SourceIdentity
	VersionComparator      = artmodel.VersionComparator
	StringComparator       = artmodel.StringComparator
	CreatedOrder           = artmodel.CreatedOrder
	Package                = artmodel.Package
	PackageVersion         = artmodel.PackageVersion
	VersionFile            = artmodel.VersionFile
)

const (
	CacheSuffix           = artmodel.CacheSuffix
	PropertyKeyMax        = artmodel.PropertyKeyMax
	PropertyValueMax      = artmodel.PropertyValueMax
	BuildPropertyValueMax = artmodel.BuildPropertyValueMax
	KeypairPrimary        = artmodel.KeypairPrimary
	KeypairSecondary      = artmodel.KeypairSecondary
	RemoteUnique          = artmodel.RemoteUnique
	RemoteMutable         = artmodel.RemoteMutable
	RemoteMetadata        = artmodel.RemoteMetadata
	RemoteDerived         = artmodel.RemoteDerived
	ExternalHTTP          = artmodel.ExternalHTTP
	ProjectRepository     = artmodel.ProjectRepository
)
