// Package format holds the per-package-type hooks the item service calls on
// every write and delete, so a format's package index entries (decision 0018)
// stay consistent with the item tree, which remains the source of truth.
// Each ecosystem has a subpackage named after it (repo-layout.md); a format
// package never imports internal/server.
package format

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler is one package type's hooks. Both run inside the transaction that
// changes the item, so an error rolls the change back.
type Handler interface {
	// Deployed runs after a file was stored at it (new or overwritten).
	Deployed(ctx context.Context, st store.Store, c repo.Config, it types.Item) error
	// Deleting runs for each file before it is removed from the tree.
	Deleting(ctx context.Context, st store.Store, c repo.Config, it types.Item) error
}

// For returns the handler of a package type. Every type is generic until
// its format lands (P7 onwards): generic keeps no package index.
func For(repo.PackageType) Handler { return generic.Handler{} }
