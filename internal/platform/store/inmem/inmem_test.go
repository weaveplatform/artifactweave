package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/store/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitContract(t *testing.T) {
	storetest.Run(t, func(_ *testing.T, watches ...configdoc.Watch) storetest.Store {
		return inmem.New(memtx.New(), watches...)
	})
}
