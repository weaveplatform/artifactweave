package sql

import (
	"context"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Store is the SQL implementation of the artifacts module's port.Store: the
// repository, remote cache, item, trash, package, Git LFS lock, Bower,
// build and archived-item stores, the blob and replica indexes and the
// cold tier's indexes. It is built over a shared dialect.Store, so every
// call joins the transaction that the caller's context carries, whichever
// module's store began it.
type Store struct{ *dialect.Store }

var _ port.Store = (*Store)(nil)

// New returns the artifacts module's stores over db, which must hold the
// module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }

// LayoutWatch returns the config watch that fences remote publication when
// a layout document is created, changed or deleted: every remote repository
// that uses the layout as its own or its upstream layout gets a new source
// generation (storekit.FenceLayoutSources). The composition registers it
// with the platform's config store, which calls it inside the config
// write's transaction, so the edit and the fence commit or roll back
// together (decision 0007).
func (s *Store) LayoutWatch() configdoc.Watch {
	return configdoc.Watch{
		Kind: configdoc.ConfigLayout,
		Changed: func(ctx context.Context, key string) error {
			return storekit.FenceLayoutSources(ctx, s, remoteRecords{s}, key)
		},
	}
}

// Module registers the artifacts module with the SQL layer: its migration
// stream, under the name that keys the stream's rows in schema_migrations;
// the repository header backfill, before every reseal and after every
// migration; and the sealed columns its repository store writes. Each
// column keeps the purpose and the row id the store seals with, because
// both are authenticated with every value (decision 0017): a change to
// either makes the stored secrets unreadable.
var Module = dialect.Module{
	Name:       "artifacts",
	Migrations: migrations.FS,
	// The backfill moves the sensitive headers still held in legacy
	// repository documents into sealed rows, and counts them, before
	// the module's columns are re-sealed.
	BeforeReseal: func(ctx context.Context, s *dialect.Store) (int, error) {
		return New(s).backfillRepositoryHeaders(ctx)
	},
	// The same backfill runs at every open, with the configured
	// keyring, because DDL never sees key material (decision 0017).
	AfterMigrate: func(ctx context.Context, s *dialect.Store) error {
		_, err := New(s).backfillRepositoryHeaders(ctx)
		return err
	},
	Sealed: []dialect.SealedColumn{
		{
			Table: "repository_secrets", Column: "password", Purpose: purposeRepoPassword,
			Keys: []string{"repo_key"}, RowID: dialect.FirstKey,
		},
		{
			Table: "repository_header_secrets", Column: "value", Purpose: purposeRepoHeader,
			Keys: []string{"repo_key", "position", "name"},
			RowID: func(k []string) string {
				index, _ := strconv.Atoi(k[1])
				return repositoryHeaderID(k[0], index, k[2])
			},
		},
	},
}
