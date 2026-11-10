package inmem_test

import (
	"testing"

	ntfinmem "github.com/weaveplatform/artifactweave/internal/notifications/adapters/inmem"
	ntfstoretest "github.com/weaveplatform/artifactweave/internal/notifications/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitContract(t *testing.T) {
	ntfstoretest.Run(t, func(*testing.T) port.Store { return ntfinmem.New(memtx.New()) })
}
