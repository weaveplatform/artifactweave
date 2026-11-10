package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// TestUnitContract runs the artifacts contract suite on the module's
// in-memory adapter alone.
func TestUnitContract(t *testing.T) {
	artstoretest.Run(t, func(*testing.T) port.Store { return artinmem.New(memtx.New()) })
}
