package registry

import (
	"context"
	"fmt"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// DeleteTag removes the tag folder of o's image with everything in it, as
// RemoveTag does with nothing else: its files go to the trash can when it
// is on and the package index follows (decisions 0018 and 0031). The
// manifest stays readable by digest from any other folder holding it.
// store.ErrNotFound when the folder holds no manifest.
func (s *Service) DeleteTag(ctx context.Context, o Owner, tag string) error {
	return s.RemoveTag(ctx, o, tag, Removal{})
}

// DeleteManifest removes every folder of o's image that holds the
// manifest whose SHA-256 is hex, its tag folders and its digest folder, in
// one transaction, so that neither the digest nor a tag that named it
// answers afterwards. An index's platform manifests stay: they are
// manifests of their own. store.ErrNotFound when no folder holds it.
func (s *Service) DeleteManifest(ctx context.Context, o Owner, hex string) error {
	folders, err := layout.ManifestFolders(ctx, s.Store.Items(), o.Repo.Key, o.Name, hex)
	if err != nil {
		return err //nolint:wrapcheck // the layout names itself
	}
	if len(folders) == 0 {
		return fmt.Errorf("registry: manifest %s in %s: %w", hex, o.Name, store.ErrNotFound)
	}
	return s.deleteFolders(ctx, o, folders)
}

// deleteFolders deletes folders of o's image in one transaction.
func (s *Service) deleteFolders(ctx context.Context, o Owner, folders []string) error {
	items := s.items()
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		for _, f := range folders {
			dir := o.Name + "/" + f
			if _, err := items.Delete(ctx, item.Target{Repo: o.Repo, Path: dir}, o.By); err != nil {
				return fmt.Errorf("registry: delete %s: %w", dir, err)
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}
