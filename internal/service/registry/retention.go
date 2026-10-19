package registry

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// errNotKept is a previous manifest Tag Retention could not move whole to
// its digest folder; the push fails rather than keep half of it.
var errNotKept = errors.New("not every item moved")

// Removal says what goes with a tag that RemoveTag removes.
type Removal struct {
	// Platforms removes, with a manifest list or image index, its
	// descriptive platform tags and the digest folders of its platform
	// manifests that no list left in the image names (decision 0031).
	Platforms bool
	// Retained removes the tag's previous manifests that Tag Retention kept.
	Retained bool
}

// RemoveTag removes the tag folder of o's image through the item service:
// its files go to the trash can when it is on, and the package index
// (decision 0018) drops the version in the same transaction, so a caller
// keeps nothing in step itself. A manifest DELETE by tag removes with no
// Removal, the multi-architecture delete with Platforms, and Max Unique
// Tags with both. store.ErrNotFound when the image has no such tag.
func (s *Service) RemoveTag(ctx context.Context, o Owner, tag string, r Removal) error {
	items := s.items()
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		dir := o.Name + "/" + tag
		_, ok, err := layout.FolderManifest(ctx, s.Store.Items(), o.Repo.Key, false, dir)
		switch {
		case err != nil:
			return err //nolint:wrapcheck // the layout names itself
		case !ok || layout.IsDigestFolder(tag):
			return fmt.Errorf("registry: tag %s of %s: %w", tag, o.Name, store.ErrNotFound)
		}
		return s.removeTag(ctx, items, o, tag, r)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

func (s *Service) removeTag(
	ctx context.Context,
	items *item.Service,
	o Owner,
	tag string,
	r Removal,
) error {
	if err := s.removeFolder(ctx, items, o, tag, r.Platforms); err != nil {
		return err
	}
	if !r.Retained {
		return nil
	}
	kept, err := layout.Retained(ctx, s.Store.Items(), o.Repo.Key, o.Name, tag)
	if err != nil {
		return err //nolint:wrapcheck // the layout names itself
	}
	for _, m := range kept {
		if err := s.removeFolder(ctx, items, o, path.Base(m.Path), r.Platforms); err != nil {
			return err
		}
	}
	return nil
}

// removeFolder deletes one tag or digest folder of o's image, to the trash
// can when it is on. With platforms, a list manifest in it takes along its
// descriptive platform tags and the platform manifests nothing else in the
// image holds or lists.
func (s *Service) removeFolder(
	ctx context.Context,
	items *item.Service,
	o Owner,
	folder string,
	platforms bool,
) error {
	var tags, orphans []string
	if platforms {
		var err error
		if tags, orphans, err = s.listUnit(ctx, o, folder); err != nil {
			return err
		}
	}
	return removeFolders(ctx, items, o, slices.Concat([]string{folder}, tags, orphans))
}

// setting is a repository's numeric setting, or def when unset.
func setting(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

// replace clears folder of p's image for a different manifest, an index
// or list when list is set. With Tag Retention at 2 or more, a tag's
// previous manifest moves with its blobs to its own digest folder, still
// naming the tag (decision 0031), unless that folder already exists because
// the manifest was also pushed by digest; otherwise the folder's contents
// go, as an overwrite replaces a file. A list overwritten by an image that
// way takes its descriptive tags and the platform manifests nothing else
// names along, as 7.x removes "all of the sub-manifests related to the
// original revision" (additional-docker-information, "Overwriting List
// Manifests with a Manifest and Vice-Versa"; RTFACT-29681). A list
// overwritten by a list keeps them, 7.x's default, which overwrites "only
// the list manifest" (DV-084).
func (s *Service) replace(
	ctx context.Context,
	items *item.Service,
	p Push,
	folder string,
	list bool,
) error {
	dir := p.Name + "/" + folder
	if !layout.IsDigestFolder(folder) &&
		setting(p.Repo.DockerTagRetention, repo.DefaultDockerTagRetention) >= 2 {
		old, ok, err := layout.FolderManifest(ctx, s.Store.Items(), p.Repo.Key, false, dir)
		if err != nil {
			return err //nolint:wrapcheck // the layout names itself
		}
		if ok {
			to := p.Name + "/" + layout.DigestFolder(old.Actual.SHA256)
			_, err := s.Store.Items().Get(ctx, p.Repo.Key, false, to)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return s.keep(ctx, items, p, dir, to)
			case err != nil:
				return fmt.Errorf("registry: get %s: %w", to, err)
			}
		}
	}
	var tags, orphans []string
	if !list {
		var err error
		if tags, orphans, err = s.listUnit(ctx, p.Owner, folder); err != nil {
			return err
		}
	}
	if _, err := items.Purge(ctx, item.Target{Repo: p.Repo, Path: dir}); err != nil {
		return fmt.Errorf("registry: replace %s: %w", dir, err)
	}
	return removeFolders(ctx, items, p.Owner, slices.Concat(tags, orphans))
}

// keep moves a tag folder to the digest folder to, keeping its times and
// properties, as Tag Retention keeps a previous version.
func (s *Service) keep(ctx context.Context, items *item.Service, p Push, dir, to string) error {
	res, err := items.Transfer(ctx, item.Transfer{
		From: item.Target{Repo: p.Repo, Path: dir},
		To:   item.Target{Repo: p.Repo, Path: to},
		Move: true, FailFast: true, By: p.By,
	})
	if err == nil && res.Failed > 0 {
		err = fmt.Errorf("%w: %d items: %v", errNotKept, res.Failed, res.Messages)
	}
	if err != nil {
		return fmt.Errorf("registry: retain %s: %w", dir, err)
	}
	return nil
}

// retain applies the repository's Tag Retention and Max Unique Tags to the
// pushed image after a push to a tag (decision 0031): each pushed tag
// keeps at most dockerTagRetention minus one previous manifests, and the
// image at most maxUniqueTags tags, a list's descriptive tags counting with
// the list. The oldest pushed go first, latest included, each with its
// retained versions, its descriptive tags and the platform manifests no
// other list names. A push by digest adds no tag and checks nothing.
func (s *Service) retain(ctx context.Context, items *item.Service, p Push) error {
	var tags []string
	for _, f := range p.Folders {
		if !layout.IsDigestFolder(f) {
			tags = append(tags, f)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	if keep := setting(p.Repo.DockerTagRetention, repo.DefaultDockerTagRetention); keep >= 2 {
		for _, tag := range tags {
			kept, err := layout.Retained(ctx, s.Store.Items(), p.Repo.Key, p.Name, tag)
			if err != nil {
				return err //nolint:wrapcheck // the layout names itself
			}
			for _, m := range kept[:max(0, len(kept)-(keep-1))] {
				if err := s.removeFolder(ctx, items, p.Owner, path.Base(m.Path), true); err != nil {
					return err
				}
			}
		}
	}
	limit := setting(p.Repo.MaxUniqueTags, 0)
	if limit <= 0 {
		return nil
	}
	pkg, err := s.Store.Packages().Find(ctx, p.Repo.Key, p.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("registry: package %s: %w", p.Name, err)
	}
	versions, err := s.Store.Packages().Versions(ctx, pkg.ID)
	if err != nil {
		return fmt.Errorf("registry: tags of %s: %w", p.Name, err)
	}
	derived, err := s.derivedTags(ctx, p.Owner)
	if err != nil {
		return err
	}
	versions = slices.DeleteFunc(versions, func(v types.PackageVersion) bool {
		return derived[v.Version]
	})
	for _, v := range versions[:max(0, len(versions)-limit)] {
		if err := s.removeTag(ctx, items, p.Owner, v.Version,
			Removal{Platforms: true, Retained: true}); err != nil {
			return err
		}
	}
	return nil
}
