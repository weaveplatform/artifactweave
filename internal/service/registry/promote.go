package registry

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrSamePlace is a promotion onto itself: the same repository, image and
// tag.
var ErrSamePlace = errors.New("registry: the source and the target are the same")

// Rename is one tag a promotion takes, and the tag it lands as.
type Rename struct {
	From string
	To   string
}

// Promotion is a JFrog Docker promote (decision 0031): tags of From's image
// copied or moved to To's image, which may be in another repository of the
// same package type, under another name. To.By is who promotes.
type Promotion struct {
	From Owner
	To   Owner
	Tags []Rename
	Copy bool
}

// PromotedTags returns the tags a promotion of o's image takes: tag alone,
// or every tag of the image when tag is empty. store.ErrNotFound when the
// image holds no tag, or tag names no folder holding a manifest.
func (s *Service) PromotedTags(ctx context.Context, o Owner, tag string) ([]string, error) {
	if tag == "" {
		tags, _, err := layout.Tags(ctx, s.Store.Items(), o.Repo.Key, o.Name)
		if err != nil {
			return nil, err //nolint:wrapcheck // the layout names itself
		}
		if len(tags) == 0 {
			return nil, fmt.Errorf("registry: image %s: %w", o.Name, store.ErrNotFound)
		}
		return tags, nil
	}
	if _, err := s.TagManifest(ctx, o, tag); err != nil {
		return nil, err
	}
	return []string{tag}, nil
}

// TagManifest is the manifest file tag holds in o's image; store.ErrNotFound
// for a reserved name, a digest folder or a folder holding none.
func (s *Service) TagManifest(ctx context.Context, o Owner, tag string) (types.Item, error) {
	if layout.ReservedTag(tag) || layout.IsDigestFolder(tag) {
		return types.Item{}, fmt.Errorf(
			"registry: tag %s of %s: %w",
			tag,
			o.Name,
			store.ErrNotFound,
		)
	}
	m, ok, err := layout.FolderManifest(ctx, s.Store.Items(), o.Repo.Key, false, o.Name+"/"+tag)
	switch {
	case err != nil:
		return types.Item{}, err //nolint:wrapcheck // the layout names itself
	case !ok:
		return types.Item{}, fmt.Errorf(
			"registry: tag %s of %s: %w",
			tag,
			o.Name,
			store.ErrNotFound,
		)
	}
	return m, nil
}

// Promote lands each of p's tags in p.To through the item service, so the
// trash can, the package index and events follow as for any copy or move;
// the caller has checked permissions. A tag folder lands with its blobs
// linked, never copied as bytes, and a manifest list takes its platform
// manifests' digest folders along. A target tag holding another manifest is
// replaced as a push replaces it, keeping it under Tag Retention, and the
// target image's tag limits apply afterwards. Without p.Copy each tag folder
// moves, keeping its history, and the platform manifests nothing left in the
// source image names go too. All of it is one transaction.
func (s *Service) Promote(ctx context.Context, p Promotion) error {
	for _, r := range p.Tags {
		if p.From.Repo.Key == p.To.Repo.Key && p.From.Name == p.To.Name && r.From == r.To {
			return ErrSamePlace
		}
	}
	items := s.items()
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var landed, platforms []string
		for _, r := range p.Tags {
			children, err := s.promoteTag(ctx, items, p, r)
			if err != nil {
				return err
			}
			landed = append(landed, r.To)
			platforms = append(platforms, children...)
		}
		if !p.Copy && len(platforms) > 0 {
			orphans, err := s.unreferenced(ctx, p.From, "", platforms)
			if err != nil {
				return err
			}
			for _, f := range orphans {
				dir := p.From.Name + "/" + f
				if _, err := items.Delete(
					ctx,
					item.Target{Repo: p.From.Repo, Path: dir},
					p.To.By,
				); err != nil {
					return fmt.Errorf("registry: remove %s: %w", dir, err)
				}
			}
		}
		return s.retain(ctx, items, Push{Owner: p.To, Folders: landed})
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// promoteTag lands one tag of p's source image in its target folder, and
// returns the platform digests the tag's manifest list names, for a move to
// clear up after.
func (s *Service) promoteTag(
	ctx context.Context,
	items *item.Service,
	p Promotion,
	r Rename,
) ([]string, error) {
	manifest, err := s.TagManifest(ctx, p.From, r.From)
	if err != nil {
		return nil, err
	}
	var children []string
	if manifest.Name == layout.ListManifestFile {
		// Platforms first, while the list is still where they are read from.
		if children, err = s.promotePlatforms(ctx, items, p, manifest); err != nil {
			return nil, err
		}
	}
	src, dst := p.From.Name+"/"+r.From, p.To.Name+"/"+r.To
	exists, same, err := s.folderState(ctx, p.To, r.To, manifest.Actual.SHA256)
	switch {
	case err != nil:
		return nil, err
	case same:
		// The target already holds this manifest, as after a repeated
		// promotion: nothing lands, and a move only leaves the source.
		if !p.Copy {
			if _, err := items.Delete(
				ctx,
				item.Target{Repo: p.From.Repo, Path: src},
				p.To.By,
			); err != nil {
				return nil, fmt.Errorf("registry: remove %s: %w", src, err)
			}
		}
		return children, nil
	case exists:
		if err := s.replace(ctx, items, Push{Owner: p.To}, r.To); err != nil {
			return nil, err
		}
	}
	if err := s.landFolder(ctx, items, p, src, dst, r.To, !p.Copy); err != nil {
		return nil, err
	}
	return children, nil
}

// promotePlatforms copies the digest folders of the platform manifests the
// list names into the target image, where it does not hold them already,
// and returns the digests it names.
func (s *Service) promotePlatforms(
	ctx context.Context,
	items *item.Service,
	p Promotion,
	list types.Item,
) ([]string, error) {
	props, err := s.Store.Items().Properties(ctx, list.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("registry: properties of %s: %w", list.FullPath(), err)
	}
	m, err := s.stored(ctx, aql.Hit{Item: list, Properties: props})
	if err != nil {
		return nil, err
	}
	children := m.Children()
	for _, d := range children {
		hex, ok := layout.SHA256Hex(d)
		if !ok {
			continue
		}
		folder := layout.DigestFolder(hex)
		from, to := p.From.Name+"/"+folder, p.To.Name+"/"+folder
		if found, err := s.isFolder(ctx, p.From, from); err != nil {
			return nil, err
		} else if !found {
			// A platform the source image does not hold has nothing to
			// copy; a push would have refused the list without it.
			continue
		}
		if found, err := s.isFolder(ctx, p.To, to); err != nil || found {
			return nil, err
		}
		if err := s.landFolder(ctx, items, p, from, to, folder, false); err != nil {
			return nil, err
		}
	}
	return children, nil
}

// isFolder reports whether dir exists in o's repository.
func (s *Service) isFolder(ctx context.Context, o Owner, dir string) (bool, error) {
	_, err := s.Store.Items().Get(ctx, o.Repo.Key, false, dir)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("registry: get %s: %w", dir, err)
	}
	return true, nil
}

// landFolder copies, or with move moves, the image folder from to the
// target folder to, then names the target image and tag in the manifest's
// properties, so jf and the registry read it as their own.
func (s *Service) landFolder(
	ctx context.Context,
	items *item.Service,
	p Promotion,
	from, to, folder string,
	move bool,
) error {
	res, err := items.Transfer(ctx, item.Transfer{
		From:     item.Target{Repo: p.From.Repo, Path: from},
		To:       item.Target{Repo: p.To.Repo, Path: to},
		Move:     move,
		FailFast: true, CanOverwrite: true, By: p.To.By,
	})
	if err == nil && res.Failed > 0 {
		err = fmt.Errorf("%w: %d items: %v", errNotKept, res.Failed, res.Messages)
	}
	if err != nil {
		return fmt.Errorf("registry: promote %s: %w", from, err)
	}
	landed, ok, err := layout.FolderManifest(ctx, s.Store.Items(), p.To.Repo.Key, false, to)
	switch {
	case err != nil:
		return err //nolint:wrapcheck // the layout names itself
	case !ok:
		return nil
	}
	set := types.Properties{}
	set.Set(layout.RepoNameProperty, p.To.Name)
	set.Set(layout.ManifestProperty, folder)
	if err := items.Annotate(ctx, p.To.Repo, landed, set, nil); err != nil {
		return fmt.Errorf("registry: properties of %s: %w", landed.FullPath(), err)
	}
	return nil
}

// Parent is a manifest list or image index that names a given manifest,
// with the tags it is pushed under.
type Parent struct {
	Digest string
	Tags   []string
}

// ParentManifests returns the manifest lists and image indexes of o's image
// that name the manifest tag holds (decision 0031), each once with the tags
// it sits under, ordered by digest. A push stores a list only where its
// platforms already are, so the image is all there is to search.
// store.ErrNotFound when tag holds no manifest.
func (s *Service) ParentManifests(ctx context.Context, o Owner, tag string) ([]Parent, error) {
	child, err := s.TagManifest(ctx, o, tag)
	if err != nil {
		return nil, err
	}
	want := "sha256:" + child.Actual.SHA256
	hits, err := layout.ImageManifests(ctx, s.Store.Items(), o.Repo.Key, o.Name)
	if err != nil {
		return nil, err //nolint:wrapcheck // the layout names itself
	}
	byDigest := map[string]*Parent{}
	var parents []*Parent
	for _, h := range hits {
		if h.Item.Name != layout.ListManifestFile {
			continue
		}
		digest := "sha256:" + h.Item.Actual.SHA256
		parent, seen := byDigest[digest]
		if !seen {
			m, err := s.stored(ctx, h)
			if err != nil {
				return nil, err
			}
			if slices.Contains(m.Children(), want) {
				parent = &Parent{Digest: digest, Tags: []string{}}
				parents = append(parents, parent)
			}
			byDigest[digest] = parent
		}
		if folder := path.Base(h.Item.Path); parent != nil && !layout.IsDigestFolder(folder) {
			parent.Tags = append(parent.Tags, folder)
		}
	}
	slices.SortFunc(parents, func(a, b *Parent) int { return strings.Compare(a.Digest, b.Digest) })
	out := make([]Parent, 0, len(parents))
	for _, p := range parents {
		slices.SortFunc(p.Tags, layout.Compare)
		out = append(out, *p)
	}
	return out, nil
}
