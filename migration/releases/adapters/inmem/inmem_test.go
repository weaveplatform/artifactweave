package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	relinmem "github.com/weaveplatform/artifactweave/migration/releases/adapters/inmem"
	relstoretest "github.com/weaveplatform/artifactweave/migration/releases/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/releases/core/port"
)

func TestUnitContract(t *testing.T) {
	relstoretest.Run(t, func(*testing.T) port.Store { return relinmem.New(memtx.New()) })
}
