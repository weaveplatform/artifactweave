package sql

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/store/sql/migrations"
)

// Store is the SQL implementation of the platform's stores. It is built
// over a shared dialect.Store, so every call joins the transaction that the
// caller's context carries, whichever store began it.
type Store struct {
	*dialect.Store
	watches []configdoc.Watch
}

// New returns the platform's stores over db, which must hold the platform
// stream's tables. Its config store calls watches as configdoc.Watch
// describes.
func New(db *dialect.Store, watches ...configdoc.Watch) *Store {
	return &Store{Store: db, watches: slices.Clone(watches)}
}

// Module registers the platform with the SQL layer: its migration stream,
// under the name that keys the stream's rows in schema_migrations, and the
// sealed columns its stores write. Each column keeps the purpose and the
// row id its store seals with, because both are authenticated with every
// value (decision 0017): a change to either makes the stored values of the
// column unreadable. The platform comes first in the canonical module
// order (decision 0044, "Persistence").
var Module = dialect.Module{
	Name:       "platform",
	Migrations: migrations.FS,
	Sealed: []dialect.SealedColumn{
		{
			Table: "config_secrets", Column: "value", Purpose: purposeConfigSecret,
			Keys: []string{"kind", "key", "name"},
			RowID: func(k []string) string {
				return configSecretID(configdoc.ConfigKind(k[0]), k[1], k[2])
			},
		},
		{
			Table: "instance", Column: "private_key", Purpose: purposeInstanceKey,
			Keys: []string{"instance_id"}, RowID: dialect.FirstKey,
		},
	},
}

// watched reports whether a watch is registered for kind, so a Put of a
// kind nobody watches skips reading the document it replaces.
func (s *Store) watched(kind configdoc.ConfigKind) bool {
	return slices.ContainsFunc(s.watches, func(w configdoc.Watch) bool { return w.Kind == kind })
}

// notify runs the watches of kind for key.
func (s *Store) notify(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	for _, w := range s.watches {
		if w.Kind != kind {
			continue
		}
		if err := w.Changed(ctx, key); err != nil {
			return err //nolint:wrapcheck // the watch's error names its operation
		}
	}
	return nil
}
