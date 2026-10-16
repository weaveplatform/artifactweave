package docker

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler is the format of docker, oci and helmoci repositories, which the
// item service calls on every write and delete in them. An image becomes a
// package and a tag a version (decision 0018) with tag retention; until
// then a write indexes nothing, and a delete's package versions are pruned
// by the item service as for every format.
type Handler struct{}

// Deployed runs after a file of an image was stored.
func (Handler) Deployed(context.Context, store.Store, repo.Config, types.Item) error { return nil }

// Deleting runs before a file of an image is removed.
func (Handler) Deleting(context.Context, store.Store, repo.Config, types.Item) error { return nil }
