package api

import "context"

// RepositoryLifecycle resets a consumer's records from the previous repository
// lifetime. Artifacts calls it on fresh creation and before deletion, inside
// the guarded metadata transaction and before content events take stream locks.
// It must join ctx's transaction and perform no remote I/O. Failure rolls back
// the repository change and every consumer effect together.
type RepositoryLifecycle interface {
	ResetRepository(ctx context.Context, key string) error
}
