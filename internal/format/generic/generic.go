// Package generic is the format of generic repositories: files without
// package metadata, so nothing is indexed.
package generic

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler implements format.Handler for generic content.
type Handler struct{}

// Deployed does nothing: a generic file belongs to no package.
func (Handler) Deployed(context.Context, store.Store, repo.Config, types.Item) error { return nil }

// Deleting does nothing.
func (Handler) Deleting(context.Context, store.Store, repo.Config, types.Item) error { return nil }
