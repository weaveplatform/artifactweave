package inmem_test

import (
	"testing"

	accinmem "github.com/weaveplatform/artifactweave/migration/access/adapters/inmem"
	accstoretest "github.com/weaveplatform/artifactweave/migration/access/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

func TestUnitContract(t *testing.T) {
	accstoretest.Run(t, func(*testing.T) port.Store { return accinmem.New(memtx.New()) })
}
