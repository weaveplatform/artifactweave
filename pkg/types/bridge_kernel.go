// Bridge (P9d): declared in migration/kernel; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/kernel"

var ErrInvalidItemPath = kernel.ErrInvalidItemPath

const RootPath = kernel.RootPath

type Checksums = kernel.Checksums

var SplitItemPath = kernel.SplitItemPath

type (
	Scope          = kernel.Scope
	ScopeEntry     = kernel.ScopeEntry
	ScopeKind      = kernel.ScopeKind
	ScopeActions   = kernel.ScopeActions
	TokenIdentity  = kernel.TokenIdentity
	RegistryAccess = kernel.RegistryAccess
	RegistryGrant  = kernel.RegistryGrant
)

var ErrInvalidScope = kernel.ErrInvalidScope

var (
	ParseScope = kernel.ParseScope
	UserScope  = kernel.UserScope
)

const MaxScopeLength = kernel.MaxScopeLength

const (
	ScopeUser     = kernel.ScopeUser
	ScopeAdmin    = kernel.ScopeAdmin
	ScopeGroups   = kernel.ScopeGroups
	ScopeRoles    = kernel.ScopeRoles
	ScopeArtifact = kernel.ScopeArtifact
	ScopeRepo     = kernel.ScopeRepo
	ScopeProject  = kernel.ScopeProject
	ScopeSystem   = kernel.ScopeSystem
	ScopeBuild    = kernel.ScopeBuild
)

const (
	ScopeRead     = kernel.ScopeRead
	ScopeWrite    = kernel.ScopeWrite
	ScopeDelete   = kernel.ScopeDelete
	ScopeAnnotate = kernel.ScopeAnnotate
	ScopeExecute  = kernel.ScopeExecute
	ScopeScan     = kernel.ScopeScan
	ScopeManage   = kernel.ScopeManage
	ScopeAll      = kernel.ScopeAll
)

const (
	IdentityStored    = kernel.IdentityStored
	IdentityTransient = kernel.IdentityTransient
	IdentitySynthetic = kernel.IdentitySynthetic
)
