package api

import "github.com/weaveplatform/artifactweave/migration/kernel"

// Subject is who a request acts as. Groups are the groups the subject
// belongs to; the token service (#74) may instead put a token's scoped
// groups here when it mints a token for a subject that is not a user.
type Subject struct {
	Name      string
	Groups    []string
	Admin     bool
	Anonymous bool
	// Scope is the scope of the token the request carries; nil for password
	// authentication, which acts with everything the subject holds.
	Scope *kernel.Scope
}

// Repo identifies a permission target: a repository's key and class (local,
// remote, virtual, federated, distribution), or the fixed build-info or
// release-bundle repository of those resources. "<remote>-cache" is
// accepted for a remote's cache and resolves to the remote.
type Repo struct {
	Key   string
	Class string
}
