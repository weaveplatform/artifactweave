package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// TestUnitVersionNamesContract runs the version names contract on the
// module's in-memory adapter.
func TestUnitVersionNamesContract(t *testing.T) {
	artstoretest.RunVersionNames(
		t,
		func(*testing.T) port.Store { return artinmem.New(memtx.New()) },
	)
}
