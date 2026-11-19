package model

// ProjectIncarnation identifies one lifetime of a project. Recreating a project
// under the same key must not restore grants bound to its previous lifetime.
type ProjectIncarnation string

// AuthorizationRevision identifies one version of effective project or sharing
// policy. Revisions are opaque, durable and never reused after a policy changes.
type AuthorizationRevision string

// ProjectAuthority identifies the current project lifetime and policy. It is an
// internal fence, not a permission grant or a public project representation.
// Current caller, token and platform permissions still require revalidation.
type ProjectAuthority struct {
	Incarnation ProjectIncarnation
	Revision    AuthorizationRevision
}

// SharingAuthority identifies one version of a repository's project assignment
// and shares. Deleting and recreating an assignment allocates another revision,
// even when the repository and its new assignment have the same names.
type SharingAuthority struct {
	Revision AuthorizationRevision
}
