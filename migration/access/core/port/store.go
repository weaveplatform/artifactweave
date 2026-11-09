package port

import "github.com/weaveplatform/artifactweave/migration/platform/tx"

// Store is every store the access module owns. All of them take part in
// the caller's unit of work (tx.Runner).
type Store interface {
	tx.Runner
	Users() UserStore
	Groups() GroupStore
	Permissions() PermissionStore
	Tokens() TokenStore
	Keys() KeyStore
	Projects() ProjectStore
	APIRestrictions() APIRestrictionStore
}
