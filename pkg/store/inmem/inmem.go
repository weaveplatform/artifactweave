// Package inmem is the reference in-memory implementation of pkg/store.
//
// # Design
//
// Store embeds a *memtx.Store, the in-memory unit of work, so the gate,
// the transactions with their snapshots, the row copies and the paging are
// memtx's, and so are Tx, AfterCommit and Close. Each entity store here keeps
// its rows in memtx tables and makes every call through Do. It serves unit
// tests and embedders that need no durability.
package inmem

import (
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
type Store struct{ *memtx.Store }

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store { return &Store{Store: memtx.New()} }
