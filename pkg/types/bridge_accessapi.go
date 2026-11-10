// Bridge (P9d): declared in migration/access/api; PRO-02 deletes it.

package types

import accessapi "github.com/weaveplatform/artifactweave/migration/access/api"

var ErrUnknownAction = accessapi.ErrUnknownAction

type (
	ResourceType = accessapi.ResourceType
	Actions      = accessapi.Actions
	KeypairType  = accessapi.KeypairType
	Keypair      = accessapi.Keypair
	Certificate  = accessapi.Certificate
)

var (
	ParseAccessActions = accessapi.ParseAccessActions
	ParseLegacyActions = accessapi.ParseLegacyActions
	ParseLetters       = accessapi.ParseLetters
	IsReferenceToken   = accessapi.IsReferenceToken
)

const (
	ResourceArtifact       = accessapi.ResourceArtifact
	ResourceBuild          = accessapi.ResourceBuild
	ResourceReleaseBundle  = accessapi.ResourceReleaseBundle
	ResourceDestination    = accessapi.ResourceDestination
	ResourcePipelineSource = accessapi.ResourcePipelineSource
)

const (
	TargetAny                = accessapi.TargetAny
	TargetAnyLocal           = accessapi.TargetAnyLocal
	TargetAnyRemote          = accessapi.TargetAnyRemote
	TargetAnyDistribution    = accessapi.TargetAnyDistribution
	BuildInfoRepository      = accessapi.BuildInfoRepository
	ReleaseBundlesRepository = accessapi.ReleaseBundlesRepository
)

const (
	ActionRead     = accessapi.ActionRead
	ActionAnnotate = accessapi.ActionAnnotate
	ActionWrite    = accessapi.ActionWrite
	ActionDelete   = accessapi.ActionDelete
	ActionManage   = accessapi.ActionManage
	ActionScan     = accessapi.ActionScan
	ActionExecute  = accessapi.ActionExecute
)

const (
	KeypairRSA     = accessapi.KeypairRSA
	KeypairGPG     = accessapi.KeypairGPG
	KeypairEd25519 = accessapi.KeypairEd25519
)

var BuiltinEnvironments = accessapi.BuiltinEnvironments

const (
	EnvironmentDev      = accessapi.EnvironmentDev
	EnvironmentProd     = accessapi.EnvironmentProd
	DefaultRefreshGrace = accessapi.DefaultRefreshGrace
	RegistryTokenExpiry = accessapi.RegistryTokenExpiry
)
