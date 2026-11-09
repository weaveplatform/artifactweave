package sql

import "github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"

// Store is the SQL implementation of the artifacts module's item, trash,
// package, blob index, replica index and archived-item stores and of the
// cold tier's indexes. It is built over a shared dialect.Store, so every
// call joins the transaction that the caller's context carries, whichever
// module's store began it. The repository, remote cache, build, Git LFS
// lock and Bower stores, and with them the module's registration with the
// SQL layer, stay with the metadata store's SQL store until STO-18, so
// Store is not yet a whole port.Store.
type Store struct{ *dialect.Store }

// New returns the artifacts module's stores over db, which must hold the
// module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }
