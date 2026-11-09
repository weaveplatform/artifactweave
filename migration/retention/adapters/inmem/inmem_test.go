package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	retinmem "github.com/weaveplatform/artifactweave/migration/retention/adapters/inmem"
	retstoretest "github.com/weaveplatform/artifactweave/migration/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
)

func TestUnitContract(t *testing.T) {
	retstoretest.Run(t, func(*testing.T) port.Store { return retinmem.New(memtx.New()) })
}
