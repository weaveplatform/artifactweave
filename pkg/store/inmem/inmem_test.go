package inmem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return inmem.New() })
}
