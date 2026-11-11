package inmem_test

import (
	"testing"

	accinmem "github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	accstoretest "github.com/weaveplatform/artifactweave/internal/access/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitContract(t *testing.T) {
	accstoretest.Run(t, func(*testing.T) port.Store { return accinmem.New(memtx.New()) })
}
