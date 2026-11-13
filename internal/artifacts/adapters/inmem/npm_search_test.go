package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// TestUnitPackageSearchContract runs the npm and NuGet search contracts on
// the in-memory adapter, which must find the candidates the SQL stores do.
func TestUnitPackageSearchContract(t *testing.T) {
	factory := func(*testing.T) port.Store { return artinmem.New(memtx.New()) }
	artstoretest.RunNpmSearch(t, factory)
	artstoretest.RunNuGetSearch(t, factory)
}
