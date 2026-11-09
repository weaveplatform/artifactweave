// Bridge (P9d): declared in migration/access/core/port; PRO-02 deletes it.

package store

import accport "github.com/weaveplatform/artifactweave/migration/access/core/port"

type (
	APIRestriction      = accport.APIRestriction
	APIRestrictionStore = accport.APIRestrictionStore
	UserStore           = accport.UserStore
	GroupStore          = accport.GroupStore
	PermissionStore     = accport.PermissionStore
	DigestKind          = accport.DigestKind
	TokenFilter         = accport.TokenFilter
	TokenStore          = accport.TokenStore
	KeyStore            = accport.KeyStore
	IdentityFilter      = accport.IdentityFilter
	IdentityResource    = accport.IdentityResource
	UserFilter          = accport.UserFilter
	UserSummary         = accport.UserSummary
	ProjectStore        = accport.ProjectStore
)

const (
	DigestAccess    = accport.DigestAccess
	DigestRefresh   = accport.DigestRefresh
	DigestReference = accport.DigestReference
)
