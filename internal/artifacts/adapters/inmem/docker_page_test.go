package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// TestUnitDockerPagesContract runs the image layout page reader's contract
// on the in-memory adapter, the reference the SQL engines are held to.
func TestUnitDockerPagesContract(t *testing.T) {
	artstoretest.RunDockerPages(t, func(*testing.T) port.Store { return artinmem.New(memtx.New()) })
}
