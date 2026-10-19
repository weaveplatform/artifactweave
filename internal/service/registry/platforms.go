package registry

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// PlatformTag is a descriptive tag a list push adds for one of its
// platforms (decision 0031), and the SHA-256 of the platform manifest it
// holds.
type PlatformTag struct {
	Tag string
	Hex string
}

// unknownPlatform stands in for an os or architecture an index entry's
// platform leaves empty. buildx writes it itself for attestation
// manifests, which so get <tag>-unknown-unknown.
const unknownPlatform = "unknown"

// PlatformTags returns the descriptive tags that the list m adds when it is
// pushed to tag: <tag>-<os>-<architecture> for each entry that names a
// platform, as 7.x adds them from 7.21.2 (docker-repositories,
// "Multi-Architecture Tagging"; RTFACT-26257). A variant is not part of the
// name, which the documentation gives as the os and architecture alone, so
// entries that differ only in variant share one tag [I]. Each name is
// returned once, holding the last entry that gives it, as tagging the
// entries in order would leave it [I]; a name outside the tag grammar, as
// when tag is near the 128-character limit, is left out. An image manifest,
// and an entry with no platform or a digest under another algorithm, give
// none.
func PlatformTags(m layout.Manifest, tag string) []PlatformTag {
	if !m.Index() || layout.IsDigestFolder(tag) {
		return nil
	}
	var out []PlatformTag
	for _, d := range m.Manifests {
		hex, ok := layout.SHA256Hex(d.Digest)
		if !ok || d.Platform == nil {
			continue
		}
		name := tag + "-" + orUnknown(d.Platform.OS) + "-" + orUnknown(d.Platform.Architecture)
		if !layout.ValidTag(name) || layout.ReservedTag(name) {
			continue
		}
		out = slices.DeleteFunc(out, func(p PlatformTag) bool { return p.Tag == name })
		out = append(out, PlatformTag{Tag: name, Hex: hex})
	}
	return out
}

func orUnknown(v string) string {
	if v == "" {
		return unknownPlatform
	}
	return v
}

// tagPlatforms writes the descriptive tags of the list p pushes to folder,
// each a tag folder holding its platform manifest and that manifest's
// blobs, copied by reference from a folder that holds it. They are written
// before the list replaces what folder held, so a platform manifest only
// folder held, as when a list overwrites one of its own platforms, lives on
// in its descriptive tag, where 7.x moved it (RTFACT-26810). A tag the push
// names itself is left to the push, and one already holding its platform is
// left as it is; one holding anything else is replaced as a push replaces a
// tag, keeping it under Tag Retention.
func (s *Service) tagPlatforms(
	ctx context.Context,
	items *item.Service,
	p Push,
	folder string,
) error {
	for _, pt := range PlatformTags(p.Manifest, folder) {
		if slices.Contains(p.Folders, pt.Tag) {
			continue
		}
		exists, same, err := s.folderState(ctx, p.Owner, pt.Tag, pt.Hex)
		switch {
		case err != nil:
			return err
		case same:
			continue
		}
		src, err := layout.FindManifest(ctx, s.Store.Items(), p.Repo.Key, p.Name, pt.Hex)
		if err != nil {
			// The push found every platform before its transaction began.
			return err //nolint:wrapcheck // the layout names itself
		}
		if exists {
			if err := s.replace(ctx, items, p, pt.Tag, false); err != nil {
				return err
			}
		}
		err = s.landFolder(ctx, items, p.Owner, p.Owner, src.Path, p.Name+"/"+pt.Tag, pt.Tag, false)
		if err != nil {
			return err
		}
	}
	return nil
}

// heldPlatformTags returns the descriptive tags of the list m in folder of
// o's image that still hold the platform manifest they were written for: a
// tag of that name since pushed over is the pusher's, not the list's.
func (s *Service) heldPlatformTags(
	ctx context.Context,
	o Owner,
	m layout.Manifest,
	folder string,
) ([]string, error) {
	var out []string
	for _, pt := range PlatformTags(m, folder) {
		_, same, err := s.folderState(ctx, o, pt.Tag, pt.Hex)
		if err != nil {
			return nil, err
		}
		if same {
			out = append(out, pt.Tag)
		}
	}
	return out, nil
}

// folderList parses the list manifest folder of o's image holds; ok is
// false when it holds none.
func (s *Service) folderList(
	ctx context.Context,
	o Owner,
	folder string,
) (layout.Manifest, bool, error) {
	items := s.Store.Items()
	list, err := items.Get(ctx, o.Repo.Key, false, o.Name+"/"+folder+"/"+layout.ListManifestFile)
	if errors.Is(err, store.ErrNotFound) {
		return layout.Manifest{}, false, nil
	}
	if err != nil {
		return layout.Manifest{}, false,
			fmt.Errorf("registry: get the list in %s/%s: %w", o.Name, folder, err)
	}
	props, err := items.Properties(ctx, list.ID)
	if err != nil {
		return layout.Manifest{}, false,
			fmt.Errorf("registry: properties of the list in %s/%s: %w", o.Name, folder, err)
	}
	m, err := s.stored(ctx, aql.Hit{Item: list, Properties: props})
	return m, err == nil, err
}

// listUnit returns what goes with the list manifest in folder of o's image
// when the list is removed, or overwritten by an image (decision 0031): its
// descriptive tags that still hold their platforms, then the digest folders
// of its platform manifests that no other tag holds and no other list in
// the image names once those are gone. A folder holding no list has none.
func (s *Service) listUnit(
	ctx context.Context,
	o Owner,
	folder string,
) ([]string, []string, error) {
	m, ok, err := s.folderList(ctx, o, folder)
	if err != nil || !ok {
		return nil, nil, err
	}
	tags, err := s.heldPlatformTags(ctx, o, m, folder)
	if err != nil {
		return nil, nil, err
	}
	gone := append([]string{folder}, tags...)
	platforms, err := s.unreferenced(ctx, o, gone, m.Children())
	if err != nil {
		return nil, nil, err
	}
	return tags, platforms, nil
}

// derivedTags returns the descriptive tags in o's image that still hold the
// platform a list tag of the image named them for. Max Unique Tags counts
// a list with its descriptive tags as the one tag the client pushed [I]:
// otherwise a list push would count once per platform, and the limit could
// remove the list it had just stored.
func (s *Service) derivedTags(ctx context.Context, o Owner) (map[string]bool, error) {
	hits, err := layout.ImageManifests(ctx, s.Store.Items(), o.Repo.Key, o.Name)
	if err != nil {
		return nil, err //nolint:wrapcheck // the layout names itself
	}
	held := map[string]string{}
	for _, h := range hits {
		held[path.Base(h.Item.Path)] = h.Item.Actual.SHA256
	}
	out := map[string]bool{}
	for _, h := range hits {
		folder := path.Base(h.Item.Path)
		if h.Item.Name != layout.ListManifestFile || layout.IsDigestFolder(folder) {
			continue
		}
		m, err := s.stored(ctx, h)
		if err != nil {
			return nil, err
		}
		for _, pt := range PlatformTags(m, folder) {
			if held[pt.Tag] == pt.Hex {
				out[pt.Tag] = true
			}
		}
	}
	return out, nil
}

// removeFolders deletes each folder of o's image through the item service,
// to the trash can when it is on.
func removeFolders(ctx context.Context, items *item.Service, o Owner, folders []string) error {
	for _, f := range folders {
		dir := o.Name + "/" + f
		if _, err := items.Delete(ctx, item.Target{Repo: o.Repo, Path: dir}, o.By); err != nil {
			return fmt.Errorf("registry: remove %s: %w", dir, err)
		}
	}
	return nil
}
