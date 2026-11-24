package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

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

// RepositoryAdmission is a RepositoryLifecycle that also takes part in a
// repository's write, inside the same guarded transaction and with the same
// no-remote-I/O rule. The federation module implements it for federated
// repository members (decision 0022), which the artifacts module cannot
// check: they name paired peers and this instance's federation base URL.
type RepositoryAdmission interface {
	// AdmitRepository checks c, about to be created or stored as an
	// update, and may rewrite it to its stored form. A refusal returns the
	// error the caller answers with and rolls the write back.
	AdmitRepository(ctx context.Context, c *repo.Config) error
	// CreatedRepository reconciles records once a new repository and its
	// bindings are stored, after ResetRepository.
	CreatedRepository(ctx context.Context, key string) error
}
