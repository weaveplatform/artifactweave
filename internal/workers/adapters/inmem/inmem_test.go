package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	wrkinmem "github.com/weaveplatform/artifactweave/internal/workers/adapters/inmem"
	wrkstoretest "github.com/weaveplatform/artifactweave/internal/workers/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/workers/core/port"
)

func TestUnitContract(t *testing.T) {
	wrkstoretest.Run(t, func(*testing.T) port.Store { return wrkinmem.New(memtx.New()) })
}
