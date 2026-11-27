package port

import "context"

// PeerRevision separates current trust from liveness. Grant changes on every
// relationship replacement, including a revoke/re-pair with the same key and
// use cases. Failure changes when a failed observation is recorded; a later
// success cannot revive evidence collected before that failure.
type PeerRevision struct {
	Grant, Failure string
}

// PeerRevisions supplies local publication fences for observations collected
// outside metadata transactions. These opaque values contain no endpoint or
// credential and do not themselves grant access to a repository.
type PeerRevisions interface {
	Revision(context.Context, string) (PeerRevision, error)
	// GuardRevision requires an active transaction and holds the relationship
	// against replacement/revocation until it commits or rolls back.
	GuardRevision(context.Context, string, PeerRevision) error
	// ObserveFailure invalidates only the grant the failed operation used.
	ObserveFailure(context.Context, string, string) error
}
