// Bridge (P9d): declared in migration/retention/core/model; PRO-02 deletes it.

package types

import retmodel "github.com/weaveplatform/artifactweave/migration/retention/core/model"

var ErrInvalidRestore = retmodel.ErrInvalidRestore

type (
	ArchiveFamily    = retmodel.ArchiveFamily
	ArchivePolicy    = retmodel.ArchivePolicy
	ExecutionKind    = retmodel.ExecutionKind
	ExecutionState   = retmodel.ExecutionState
	ArchiveExecution = retmodel.ArchiveExecution
	RestoreType      = retmodel.RestoreType
	RestoreRequest   = retmodel.RestoreRequest
	CleanupItemType  = retmodel.CleanupItemType
	CleanupCriteria  = retmodel.CleanupCriteria
	CleanupPolicy    = retmodel.CleanupPolicy
	CleanupRun       = retmodel.CleanupRun
)

const (
	MaxRestoreItems               = retmodel.MaxRestoreItems
	ArchiveRetentionV1            = retmodel.ArchiveRetentionV1
	ArchiveSmartV2                = retmodel.ArchiveSmartV2
	ExecutionArchive              = retmodel.ExecutionArchive
	ExecutionRestore              = retmodel.ExecutionRestore
	ExecutionPurge                = retmodel.ExecutionPurge
	ExecutionPending              = retmodel.ExecutionPending
	ExecutionInProgress           = retmodel.ExecutionInProgress
	ExecutionInProgressWithErrors = retmodel.ExecutionInProgressWithErrors
	ExecutionComplete             = retmodel.ExecutionComplete
	ExecutionCompleteWithErrors   = retmodel.ExecutionCompleteWithErrors
	ExecutionFailed               = retmodel.ExecutionFailed
	RestorePackage                = retmodel.RestorePackage
	RestoreArtifact               = retmodel.RestoreArtifact
	CleanupPackage                = retmodel.CleanupPackage
	CleanupBuild                  = retmodel.CleanupBuild
	CleanupReleaseBundle          = retmodel.CleanupReleaseBundle
)
