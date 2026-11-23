package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
)

func TestUnitStorageQuotaComposition(t *testing.T) {
	vol := &appstoretest.Volume{}
	appstoretest.RunStorageQuota(t, vol, func(*testing.T) appstoretest.Store { return inmem.NewWithCapacity(vol) })
}
