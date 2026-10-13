package inmem_test

import (
	"testing"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store/inmem"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return inmem.New() })
}
