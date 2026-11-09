package inmem_test

import (
	"testing"

	fedport "github.com/weaveplatform/artifactweave/migration/federation/core/port"
	ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	relport "github.com/weaveplatform/artifactweave/migration/releases/core/port"
	retport "github.com/weaveplatform/artifactweave/migration/retention/core/port"
	wrkport "github.com/weaveplatform/artifactweave/migration/workers/core/port"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
)

func TestUnitContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return inmem.New() })
}

// The in-memory store must satisfy each module's port Store, which declares
// only the part of the metadata store that module uses, so a module can take
// this store as its own until its adapters move (STO-14, STO-15). The
// aggregate's accessors return the bridged port interfaces, which are the same
// types.
var (
	_ ntfport.Store = (*inmem.Store)(nil)
	_ retport.Store = (*inmem.Store)(nil)
	_ fedport.Store = (*inmem.Store)(nil)
	_ relport.Store = (*inmem.Store)(nil)
	_ wrkport.Store = (*inmem.Store)(nil)
)
