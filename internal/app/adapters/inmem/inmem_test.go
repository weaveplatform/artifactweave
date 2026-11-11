package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
)

func TestUnitContract(t *testing.T) {
	appstoretest.Run(t, func(*testing.T) appstoretest.Store { return inmem.New() })
}
