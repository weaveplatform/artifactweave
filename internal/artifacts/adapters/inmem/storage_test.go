package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitStorageUsage(t *testing.T) {
	storetest.RunStorageUsage(t, func(*testing.T) port.Store { return artinmem.New(memtx.New()) })
}
