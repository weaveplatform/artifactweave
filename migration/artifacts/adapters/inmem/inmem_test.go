package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/inmem"
	artstoretest "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// TestUnitContract runs the artifacts contract suite on the module's
// in-memory adapter alone.
func TestUnitContract(t *testing.T) {
	artstoretest.Run(t, func(*testing.T) port.Store { return artinmem.New(memtx.New()) })
}
