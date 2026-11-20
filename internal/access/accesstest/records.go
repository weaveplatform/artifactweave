package accesstest

import (
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
)

// The access records that other modules' tests store, and the access store
// ports their fakes wrap. Access declares them in core/model and core/port,
// which a test outside access may not import (decision 0044, dependency
// rules 1 and 6), so the kit names them. Each is an alias: a value built
// through the kit is the value the access stores keep.
type (
	// User is a user account.
	User = model.User
	// Group is a set of users that permissions and roles name.
	Group = model.Group
	// RoleFlags are the platform role switches on a user or group.
	RoleFlags = model.RoleFlags
	// UserLockPolicy locks an account after repeated failed logins.
	UserLockPolicy = model.UserLockPolicy
	// AccessLockSettings are the user lock policy's fields beyond the
	// lock limit and the password expiry.
	AccessLockSettings = model.AccessLockSettings
	// Permission is a stored permission in every wire form's one shape.
	Permission = model.Permission
	// Resource is one section of a Permission.
	Resource = model.Resource
	// Grants map principal names to the actions they hold.
	Grants = model.Grants
	// Target scopes a Resource to paths in one repository or special
	// target.
	Target = model.Target
	// Token is the stored record of an access token.
	Token = model.Token
	// Project groups repositories, members and roles under a key.
	Project = model.Project
	// RepositoryAssignment is the persisted project owner, environments and shares.
	RepositoryAssignment = model.RepositoryAssignment
	// ProjectAuthority identifies a project's lifetime and current policy.
	ProjectAuthority = model.ProjectAuthority
	// SharingAuthority identifies the current repository sharing revision.
	SharingAuthority = model.SharingAuthority
	// Role is a named set of actions over environments.
	Role = model.Role
	// Environment is a deployment stage that repositories are assigned to.
	Environment = model.Environment
	// ProjectMember is one direct user or group role assignment in a project.
	ProjectMember = model.ProjectMember

	// UserStore stores users.
	UserStore = port.UserStore
	// ProjectStore is the project store that lifecycle and failure probes wrap.
	ProjectStore = port.ProjectStore
	// PermissionStore stores permissions.
	PermissionStore = port.PermissionStore
	// PermissionQuery asks PermissionStore.Page for one page of
	// permissions.
	PermissionQuery = port.PermissionQuery
	// TokenStore stores token records.
	TokenStore = port.TokenStore
	// TokenFilter narrows TokenStore's listings.
	TokenFilter = port.TokenFilter
	// APIRestriction is an administrator's temporary restriction of one
	// user's API calls.
	APIRestriction = port.APIRestriction
)

// The account statuses and the role type those tests set.
const (
	UserDisabled     = model.UserDisabled
	UserLocked       = model.UserLocked
	RoleCustomGlobal = model.RoleCustomGlobal
	RoleCustom       = model.RoleCustom
	PrincipalUser    = model.PrincipalUser
	PrincipalGroup   = model.PrincipalGroup
)
