package docker

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler is the format of docker, oci and helmoci repositories, which the
// item service calls on every write and delete in them. It keeps the
// package index of decision 0018: an image is a package while it holds any
// manifest, tagged or not, and each tag folder holding a manifest is a
// version whose files are the folder's items, ordered by the tag's last
// push. Digest folders and _uploads are no versions.
type Handler struct{}

// Deployed indexes a file stored in an image. A manifest in a tag folder
// makes the folder a version and links every file already in it, which
// covers copies, moves and restores that land the manifest after its
// blobs; another file joins its folder's version once the folder is a tag.
// A manifest in a digest folder keeps its image a package.
func (Handler) Deployed(ctx context.Context, st store.Store, c repo.Config, it types.Item) error {
	name, folder, ok := imageFolder(it)
	switch {
	case !ok || folder == UploadsFolder:
		return nil
	case IsDigestFolder(folder):
		if !isManifest(it.Name) {
			return nil
		}
		_, err := ensurePackage(ctx, st, c, name)
		return err
	case isManifest(it.Name):
		return syncTag(ctx, st, c, it.Cached, name, folder)
	}
	pv, ok, err := tagVersion(ctx, st, c.Key, name, folder)
	if err != nil || !ok {
		return err
	}
	return link(ctx, st, pv, it)
}

// Deleting runs before a file of an image is removed. The item service
// drops the file's version link with it; Deleted settles the rest.
func (Handler) Deleting(context.Context, store.Store, repo.Config, types.Item) error { return nil }

// Deleted brings the index back in line with the tree after files of
// images were removed: a tag folder left without a manifest is no longer a
// version even if blobs remain in it, and an image is a package exactly
// while it still holds a manifest somewhere, which the item service's
// pruning of empty versions cannot know.
func (Handler) Deleted(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	removed []types.Item,
) error {
	var a affected
	for _, it := range removed {
		a.add(it)
	}
	return a.sync(ctx, st, c)
}
