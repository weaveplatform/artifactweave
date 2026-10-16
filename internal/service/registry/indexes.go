package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Referrers returns the descriptors of the manifests in o's image whose
// subject is the digest subject, of artifactType alone when it is set: what
// GET .../referrers/<digest> lists (decision 0031). Each manifest appears
// once whichever folders hold it, sorted by digest, with the media type it
// was pushed with, its size, its artifactType and its annotations. None is
// an empty list, never an error: the referrers API answers no 404.
func (s *Service) Referrers(
	ctx context.Context,
	o Owner,
	subject, artifactType string,
) ([]layout.Descriptor, error) {
	hits, err := layout.FindReferrers(ctx, s.Store.Items(), o.Repo.Key, o.Name,
		subject, artifactType)
	if err != nil {
		return nil, err //nolint:wrapcheck // the layout names itself
	}
	out := []layout.Descriptor{}
	seen := map[string]bool{}
	for _, h := range hits {
		hex := h.Item.Actual.SHA256
		if seen[hex] {
			continue
		}
		seen[hex] = true
		m, err := s.stored(ctx, h)
		if err != nil {
			return nil, err
		}
		out = append(out, layout.Descriptor{
			MediaType:    m.MediaType,
			Digest:       "sha256:" + hex,
			Size:         h.Item.Size,
			ArtifactType: first(h.Properties, layout.ArtifactTypeProperty),
			Annotations:  m.Annotations,
		})
	}
	slices.SortFunc(
		out,
		func(a, b layout.Descriptor) int { return strings.Compare(a.Digest, b.Digest) },
	)
	return out, nil
}

// UnreferencedPlatforms returns the digest folders, below o's image, of the
// platform manifests that the list manifest in folder lists and that no
// other tag folder holds and no other index in the image lists: what
// deleting the multi-architecture tag also removes, as Artifactory does from
// 7.118.0 (decision 0031), and what tag retention removes with a list
// manifest. A folder holding an image manifest, or nothing, has none. The
// list's own digest folder, when it was pushed by digest too, is another
// index that lists them, so its platforms stay.
func (s *Service) UnreferencedPlatforms(
	ctx context.Context,
	o Owner,
	folder string,
) ([]string, error) {
	items := s.Store.Items()
	list, err := items.Get(ctx, o.Repo.Key, false, o.Name+"/"+folder+"/"+layout.ListManifestFile)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get the list in %s/%s: %w", o.Name, folder, err)
	}
	props, err := items.Properties(ctx, list.ID)
	if err != nil {
		return nil, fmt.Errorf("registry: properties of the list in %s/%s: %w", o.Name, folder, err)
	}
	m, err := s.stored(ctx, aql.Hit{Item: list, Properties: props})
	if err != nil {
		return nil, err
	}
	hits, err := layout.ImageManifests(ctx, items, o.Repo.Key, o.Name)
	if err != nil {
		return nil, err //nolint:wrapcheck // the layout names itself
	}
	kept := map[string]bool{}
	for _, h := range hits {
		f := path.Base(h.Item.Path)
		switch {
		case f == folder:
		case h.Item.Name == layout.ListManifestFile:
			other, err := s.stored(ctx, h)
			if err != nil {
				return nil, err
			}
			for _, d := range other.Children() {
				kept[d] = true
			}
		case !strings.HasPrefix(f, layout.DigestFolder("")):
			kept["sha256:"+h.Item.Actual.SHA256] = true
		}
	}
	var out []string
	for _, d := range m.Children() {
		hex, ok := layout.SHA256Hex(d)
		if !ok || kept[d] {
			continue
		}
		kept[d] = true // a list naming a platform twice names it once
		dir := o.Name + "/" + layout.DigestFolder(hex)
		if _, err := items.Get(ctx, o.Repo.Key, false, dir); errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("registry: get %s: %w", dir, err)
		}
		out = append(out, layout.DigestFolder(hex))
	}
	return out, nil
}

// DeleteMultiArch removes the tag folder of o's image together with the
// platform manifests UnreferencedPlatforms names, in one transaction, sending
// them to the trash can when it is on (decision 0010), by o.By. It returns
// the folders removed, the tag first; store.ErrNotFound when the image has
// no such tag. The caller has checked Delete on the image, which covers its
// digest folders. DELETE /artifactory/api/docker/<repo>/v2/delete (#226)
// calls it.
func (s *Service) DeleteMultiArch(ctx context.Context, o Owner, tag string) ([]string, error) {
	var removed []string
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		removed = nil
		dir := o.Name + "/" + tag
		f, err := s.Store.Items().Get(ctx, o.Repo.Key, false, dir)
		if err == nil && !f.Folder {
			err = store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("registry: tag %s: %w", dir, err)
		}
		platforms, err := s.UnreferencedPlatforms(ctx, o, tag)
		if err != nil {
			return err
		}
		items := s.items()
		for _, folder := range append([]string{tag}, platforms...) {
			t := item.Target{Repo: o.Repo, Path: o.Name + "/" + folder}
			if _, err := items.Delete(ctx, t, o.By); err != nil {
				return fmt.Errorf("registry: delete %s: %w", t.Path, err)
			}
			removed = append(removed, folder)
		}
		return nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the transaction returns ours
	}
	return removed, nil
}

// stored parses the manifest a hit found, as it was pushed: with the media
// type it was pushed with, or for one deployed through the REST API without
// that property, its own mediaType or its file name's type. A file that is
// no manifest the registry stores has no fields; only a failure to read it
// is an error.
func (s *Service) stored(ctx context.Context, h aql.Hit) (layout.Manifest, error) {
	r, _, err := s.Blobs.Open(ctx, h.Item.Actual.SHA256)
	if err != nil {
		return layout.Manifest{}, fmt.Errorf("registry: open %s: %w", h.Item.FullPath(), err)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, layout.MaxManifestSize+1))
	if err != nil {
		return layout.Manifest{}, fmt.Errorf("registry: read %s: %w", h.Item.FullPath(), err)
	}
	t := first(h.Properties, layout.ManifestTypeProperty)
	m, err := layout.ParseManifest(t, body, h.Item.Actual.SHA256)
	if err != nil && t == "" {
		// An OCI manifest may leave its mediaType out; its file names it.
		byName := layout.MediaTypeOCIManifest
		if h.Item.Name == layout.ListManifestFile {
			byName = layout.MediaTypeOCIIndex
		}
		m, err = layout.ParseManifest(byName, body, h.Item.Actual.SHA256)
	}
	if err != nil {
		return layout.Manifest{MediaType: t}, nil
	}
	return m, nil
}

// first is the first value of key in p, "" when it has none.
func first(p types.Properties, key string) string {
	if v := p[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}
