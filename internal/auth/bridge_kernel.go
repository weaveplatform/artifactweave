// Bridge (P9d): declared in migration/kernel; PRO-02 deletes it.

package auth

import "github.com/weaveplatform/artifactweave/migration/kernel"

type (
	Principal           = kernel.Principal
	CredentialReference = kernel.CredentialReference
	RegistryReference   = kernel.RegistryReference
	DownloadCredential  = kernel.DownloadCredential
)

const AnonymousUser = kernel.AnonymousUser

var Anonymous = kernel.Anonymous

var (
	ErrBadCredentials  = kernel.ErrBadCredentials
	ErrUnauthenticated = kernel.ErrUnauthenticated
	ErrForbidden       = kernel.ErrForbidden
	ErrAPIRestricted   = kernel.ErrAPIRestricted
	ErrPasswordExpired = kernel.ErrPasswordExpired
	ErrAccountLocked   = kernel.ErrAccountLocked
)

var (
	WithPrincipal        = kernel.WithPrincipal
	FromContext          = kernel.FromContext
	RequireAuthenticated = kernel.RequireAuthenticated
	RequireAdmin         = kernel.RequireAdmin
)
