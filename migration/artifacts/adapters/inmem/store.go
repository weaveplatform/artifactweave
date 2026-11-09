package inmem

import "github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"

// Store is the in-memory implementation of the artifacts module's item,
// trash, package, blob index, replica index, build and archived-item stores
// and of the cold tier's indexes. It keeps its rows in tables of a shared
// memtx.Store, under the names of the SQL tables, so every call joins the
// transaction that the caller's context carries, whichever module's store
// began it. The repository, remote cache, Git LFS lock and Bower stores stay
// with the metadata store's in-memory store until STO-18, so Store is not
// yet a whole port.Store.
type Store struct{ *memtx.Store }

// New returns the artifacts module's stores over db.
func New(db *memtx.Store) *Store { return &Store{Store: db} }
