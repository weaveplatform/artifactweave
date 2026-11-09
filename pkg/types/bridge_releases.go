// Bridge (P9d): declared in migration/releases/core/model; PRO-02 deletes it.

package types

import relmodel "github.com/weaveplatform/artifactweave/migration/releases/core/model"

var (
	SelectTargets = relmodel.SelectTargets
	TrackerStatus = relmodel.TrackerStatus
)

type (
	BundleState           = relmodel.BundleState
	BundleOrigin          = relmodel.BundleOrigin
	PropertyValues        = relmodel.PropertyValues
	PathMapping           = relmodel.PathMapping
	BundleQuery           = relmodel.BundleQuery
	BundleSpec            = relmodel.BundleSpec
	ReleaseNotes          = relmodel.ReleaseNotes
	ReleaseBundle         = relmodel.ReleaseBundle
	BundleArtifact        = relmodel.BundleArtifact
	DistributionTarget    = relmodel.DistributionTarget
	DistributionRule      = relmodel.DistributionRule
	TrackerKind           = relmodel.TrackerKind
	JobStatus             = relmodel.JobStatus
	DistributionTracker   = relmodel.DistributionTracker
	DistributionJob       = relmodel.DistributionJob
	BundleRetentionConfig = relmodel.BundleRetentionConfig
	BundleOperation       = relmodel.BundleOperation
	BundleRetentionAudit  = relmodel.BundleRetentionAudit
	BundleExport          = relmodel.BundleExport
)

const (
	BundleOpen        = relmodel.BundleOpen
	BundleSigned      = relmodel.BundleSigned
	BundleStored      = relmodel.BundleStored
	BundleSource      = relmodel.BundleSource
	BundleReceived    = relmodel.BundleReceived
	TrackerDistribute = relmodel.TrackerDistribute
	TrackerDelete     = relmodel.TrackerDelete
	JobNotDistributed = relmodel.JobNotDistributed
	JobInQueue        = relmodel.JobInQueue
	JobInProgress     = relmodel.JobInProgress
	JobCompleted      = relmodel.JobCompleted
	JobFailed         = relmodel.JobFailed
	JobAborted        = relmodel.JobAborted
	BundleDelete      = relmodel.BundleDelete
	BundleKeep        = relmodel.BundleKeep
	BundleUnkeep      = relmodel.BundleUnkeep
)
