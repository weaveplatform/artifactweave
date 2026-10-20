package registry

import (
	"context"
	"fmt"
	"path"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// loneTwin returns the manifest of a tag's digest twin in o's image: the
// digest folder holding the manifest hex as a push by digest left it, its
// docker.manifest naming the folder itself, when no tag folder outside gone
// holds hex and no list outside gone names it. Helm 3.18 and later push
// every chart by digest and then by tag, so each chart version has a twin
// (research 30, 6.2); Max Unique Tags counts tags, as JFrog intends (known
// issue RTDEV-88173), and a twin that outlived its tag would keep every
// chart the limit removed (decision 0039). A twin another tag holds is that
// tag's too, one a list names is a platform manifest, and a digest folder
// naming a tag is that tag's retained version, so none of them is lone.
func (s *Service) loneTwin(
	ctx context.Context,
	o Owner,
	gone []string,
	hex string,
) (types.Item, bool, error) {
	free, err := s.unreferenced(ctx, o, gone, []string{"sha256:" + hex})
	if err != nil || len(free) == 0 {
		return types.Item{}, false, err
	}
	m, ok, err := layout.FolderManifest(ctx, s.Store.Items(), o.Repo.Key, false,
		o.Name+"/"+free[0])
	if err != nil {
		return types.Item{}, false, err //nolint:wrapcheck // the layout names itself
	}
	if !ok || m.Actual.SHA256 != hex {
		return types.Item{}, false, nil
	}
	props, err := s.Store.Items().Properties(ctx, m.ID)
	if err != nil {
		return types.Item{}, false,
			fmt.Errorf("registry: properties of %s: %w", m.FullPath(), err)
	}
	return m, first(props, layout.ManifestProperty) == free[0], nil
}

// adoptTwin makes the lone twin of hex, the manifest a push is replacing in
// folder of p's image, that tag's retained version under Tag Retention: it
// is the folder the retained copy would move to, so naming the tag in its
// docker.manifest keeps the manifest as the move would have, and retention
// then counts and trims it like any retained version (decision 0039).
// Without that, each chart version Helm 3.18 pushed over would leave its
// previous manifest uncounted, and never removed.
func (s *Service) adoptTwin(
	ctx context.Context,
	items *item.Service,
	p Push,
	folder, hex string,
) error {
	twin, ok, err := s.loneTwin(ctx, p.Owner, []string{folder}, hex)
	if err != nil || !ok {
		return err
	}
	set := types.Properties{}
	set.Set(layout.ManifestProperty, folder)
	if err := items.Annotate(ctx, p.Repo, twin, set, nil); err != nil {
		return fmt.Errorf("registry: retain %s: %w", path.Dir(twin.FullPath()), err)
	}
	return nil
}
