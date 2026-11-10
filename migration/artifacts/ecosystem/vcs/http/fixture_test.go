package vcshttp

// This file holds the fixtures of the tests that call the partial's code
// without the router. A test here cannot build the router, which imports this
// package, so the store and blob store are a kittest world's.

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Store itemkit.Store
}

// newKit returns the item kit over c. The router's overlays are left out:
// the tests call the partial's reads directly, and no VCS route is an
// item GET.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{Store: c.Store})
}

// itemWorld is the store and blob store of the router's item world, for
// the tests that call the partial's code over them.
type itemWorld struct {
	db    *inmem.Store
	blobs blob.Store
}

// newItemWorld returns a kittest world.
func newItemWorld(t *testing.T) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	return &itemWorld{db: k.Store, blobs: k.Blobs}
}
