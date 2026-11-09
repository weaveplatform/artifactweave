package inmem_test

import (
	"testing"

	fedinmem "github.com/weaveplatform/artifactweave/migration/federation/adapters/inmem"
	fedstoretest "github.com/weaveplatform/artifactweave/migration/federation/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

func TestUnitContract(t *testing.T) {
	fedstoretest.Run(t, func(*testing.T) port.Store { return fedinmem.New(memtx.New()) })
}
