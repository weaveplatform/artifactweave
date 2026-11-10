// Bridge (P9d): declared in migration/access/core/model; PRO-02 deletes it.

package types

import accmodel "github.com/weaveplatform/artifactweave/migration/access/core/model"

var (
	ErrLossyProjection   = accmodel.ErrLossyProjection
	ErrInvalidProjectKey = accmodel.ErrInvalidProjectKey
	ErrInvalidSubject    = accmodel.ErrInvalidSubject
	ErrInvalidTimestamp  = accmodel.ErrInvalidTimestamp
	ErrInvalidStatus     = accmodel.ErrInvalidStatus
)

var (
	GlobalRoleActions         = accmodel.GlobalRoleActions
	PermissionFromV1          = accmodel.PermissionFromV1
	PermissionToV1            = accmodel.PermissionToV1
	PermissionFromV2          = accmodel.PermissionFromV2
	PermissionToV2            = accmodel.PermissionToV2
	PermissionFromAccess      = accmodel.PermissionFromAccess
	PermissionToAccess        = accmodel.PermissionToAccess
	MatchesRepository         = accmodel.MatchesRepository
	DefaultPasswordPolicy     = accmodel.DefaultPasswordPolicy
	DefaultAccessLockSettings = accmodel.DefaultAccessLockSettings
	EffectiveAdmin            = accmodel.EffectiveAdmin
	ValidateProjectKey        = accmodel.ValidateProjectKey
	ProjectRepoKey            = accmodel.ProjectRepoKey
	IsGlobalRoleAction        = accmodel.IsGlobalRoleAction
	IsBuiltinEnvironment      = accmodel.IsBuiltinEnvironment
	ValidEnvironmentName      = accmodel.ValidEnvironmentName
	BuiltinGlobalRoles        = accmodel.BuiltinGlobalRoles
	BuiltinGlobalRole         = accmodel.BuiltinGlobalRole
	RoleActions               = accmodel.RoleActions
	Revocable                 = accmodel.Revocable
	ParseSubject              = accmodel.ParseSubject
	UserFromLegacy            = accmodel.UserFromLegacy
	UserToLegacy              = accmodel.UserToLegacy
	UserFromAccess            = accmodel.UserFromAccess
	UserToAccess              = accmodel.UserToAccess
	GroupFromLegacy           = accmodel.GroupFromLegacy
	GroupToLegacy             = accmodel.GroupToLegacy
	GroupFromAccess           = accmodel.GroupFromAccess
	GroupToAccess             = accmodel.GroupToAccess
)

type (
	TrustedKey               = accmodel.TrustedKey
	Grants                   = accmodel.Grants
	Target                   = accmodel.Target
	Resource                 = accmodel.Resource
	Permission               = accmodel.Permission
	UserLockPolicy           = accmodel.UserLockPolicy
	PasswordExpirationPolicy = accmodel.PasswordExpirationPolicy
	PasswordPolicy           = accmodel.PasswordPolicy
	AccessLockSettings       = accmodel.AccessLockSettings
	UserStatus               = accmodel.UserStatus
	MFAStatus                = accmodel.MFAStatus
	RoleFlags                = accmodel.RoleFlags
	User                     = accmodel.User
	Group                    = accmodel.Group
	AdminPrivileges          = accmodel.AdminPrivileges
	Project                  = accmodel.Project
	RoleType                 = accmodel.RoleType
	Role                     = accmodel.Role
	Environment              = accmodel.Environment
	PrincipalType            = accmodel.PrincipalType
	ProjectMember            = accmodel.ProjectMember
	RepositoryAssignment     = accmodel.RepositoryAssignment
	SigningKey               = accmodel.SigningKey
	Token                    = accmodel.Token
	Subject                  = accmodel.Subject
)

const (
	UserInvited                   = accmodel.UserInvited
	UserEnabled                   = accmodel.UserEnabled
	UserDisabled                  = accmodel.UserDisabled
	UserLocked                    = accmodel.UserLocked
	MFANone                       = accmodel.MFANone
	MFAEnabled                    = accmodel.MFAEnabled
	MFADisabled                   = accmodel.MFADisabled
	RealmInternal                 = accmodel.RealmInternal
	RoleAdmin                     = accmodel.RoleAdmin
	RolePredefined                = accmodel.RolePredefined
	RoleCustom                    = accmodel.RoleCustom
	RoleCustomGlobal              = accmodel.RoleCustomGlobal
	PrincipalUser                 = accmodel.PrincipalUser
	PrincipalGroup                = accmodel.PrincipalGroup
	MaxCustomGlobalRoles          = accmodel.MaxCustomGlobalRoles
	DefaultTokenExpiry            = accmodel.DefaultTokenExpiry
	DefaultRevocableThreshold     = accmodel.DefaultRevocableThreshold
	LegacyTokenExpiry             = accmodel.LegacyTokenExpiry
	TokenPurposeEncryptedPassword = accmodel.TokenPurposeEncryptedPassword
)
