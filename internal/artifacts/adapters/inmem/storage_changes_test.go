package inmem_test

import (
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitStorageChanges(t *testing.T) {
	storetest.RunStorageChanges(t, func(_ *testing.T, watch api.StorageWatch) port.Store {
		return artinmem.NewWithStorageWatch(memtx.New(), watch)
	})
}
