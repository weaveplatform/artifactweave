package api

import "context"

// RepositoryLifecycle keeps a consumer's records coherent with canonical
// repository changes. Artifacts calls it inside the guarded metadata transaction
// and before content events take stream locks.
// It must join ctx's transaction and perform no remote I/O. Failure rolls back
// the repository change and every consumer effect together.
type RepositoryLifecycle interface {
	// ResetRepository discards records on fresh creation and before deletion.
	ResetRepository(ctx context.Context, key string) error
	// RefreshRepository reconciles records after an update. A failed refresh
	// rolls back the canonical configuration and bindings with the consumer.
	RefreshRepository(ctx context.Context, key string) error
}
