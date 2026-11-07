// Package inmem is the reference in-memory implementation of pkg/store.
//
// # Design
//
// Each table is a map of Go values. Values are deep-copied on the way in and
// on the way out, so callers never share memory with the store, and on the
// way in every time is normalised to UTC microseconds and every duration to
// milliseconds, exactly what the SQL columns keep. One cancellable gate serialises
// access; Tx holds it for the whole callback (calls made with the
// transaction's context skip re-locking) and restores a snapshot of every
// table when the callback fails, giving the all-or-nothing behaviour of the
// SQL backends. It serves unit tests and embedders that need no
// durability.
package inmem

import (
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
type Store struct {
	gate   chan struct{}
	tables map[string]snapshotter
	seqs   map[string]int64
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		gate:   make(chan struct{}, 1),
		tables: map[string]snapshotter{},
		seqs:   map[string]int64{},
	}
}
