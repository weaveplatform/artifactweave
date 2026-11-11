package inmem

import "github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"

// Store is the in-memory implementation of the retention module's stores. It
// keeps its rows in tables of a shared memtx.Store, under the names of the
// SQL tables, so every call joins the transaction that the caller's context
// carries, whichever module's store began it.
type Store struct{ *memtx.Store }

// New returns the retention module's stores over db.
func New(db *memtx.Store) *Store { return &Store{Store: db} }
