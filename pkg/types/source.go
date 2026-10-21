package types

// RepositoryIncarnation identifies one lifetime of a repository. Deleting and
// recreating the same key must allocate a different identity.
type RepositoryIncarnation string

// SourceGeneration is a durable opaque revision, advanced when the effective
// source, credentials or connection configuration changes. A configuration hash
// alone is insufficient: changing a source away and back cannot revive old work.
type SourceGeneration string

// SourceKind selects how a remote resolves content; an internal project source
// must never be inferred from an HTTP URL pointing at this server.
type SourceKind string

const (
	ExternalHTTP      SourceKind = "external-http"
	ProjectRepository SourceKind = "project-repository"
)

// SourceIdentity namespaces cache entries and in-flight retrievals independently
// of mutable repository names. ID is opaque and contains no URL or credentials.
// Project sources will bind it to the producer and owner-project incarnations.
type SourceIdentity struct {
	Kind       SourceKind            `json:"kind"`
	Repository RepositoryIncarnation `json:"repository"`
	ID         string                `json:"id"`
	Generation SourceGeneration      `json:"generation"`
}
