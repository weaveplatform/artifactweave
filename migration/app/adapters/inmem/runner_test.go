package inmem_test

import (
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// The store's Tx and AfterCommit are those of the embedded *memtx.Store.
var _ tx.Runner = (*inmem.Store)(nil)
