package inmem_test

import (
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// The store's Tx and AfterCommit are those of the embedded *memtx.Store.
// The store.Store assertion in inmem.go implies this one only while the
// aggregate embeds tx.Runner; this one outlasts the aggregate.
var _ tx.Runner = (*inmem.Store)(nil)
