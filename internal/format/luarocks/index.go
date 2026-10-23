package luarocks

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func indexedVersion(version string, its []types.Item) pkgindex.IndexedVersion {
	v := pkgindex.IndexedVersion{Version: version}
	for _, it := range its {
		if v.Created.IsZero() || it.Created.Before(v.Created) {
			v.Created = it.Created
		}
		v.Files = append(v.Files, it.ID)
	}
	slices.Sort(v.Files)
	return v
}

func syncVersion(ctx context.Context, env format.Env, c repo.Config, id Identity) error {
	all, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	var its []types.Item
	for _, it := range all {
		other, ok := Parse(it.FullPath())
		if ok && other.Name == id.Name && other.Version == id.Version {
			its = append(its, it)
		}
	}
	if len(its) == 0 {
		// The item service unlinks and prunes the last file before Deleted.
		return nil
	}
	v := indexedVersion(id.Version, its)
	p, err := env.Store.Packages().
		Ensure(ctx, types.Package{RepoKey: c.Key, Name: id.Name, Type: c.PackageType, Created: v.Created})
	if err != nil {
		return fmt.Errorf("luarocks: package %s: %w", id.Name, err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, v)
	return err //nolint:wrapcheck
}

func setProperties(ctx context.Context, st store.Store, it types.Item, id Identity) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("luarocks: properties: %w", err)
	}
	want := types.Properties{
		PropName:     {types.TruncatePropertyValue(id.Name, types.PropertyValueMax)},
		PropVersion:  {types.TruncatePropertyValue(id.Version, types.PropertyValueMax)},
		PropRevision: {id.Revision}, PropArch: {id.Arch},
	}
	set := types.Properties{}
	for key, values := range want {
		if !slices.Equal(props[key], values) {
			set[key] = values
		}
	}
	if len(set) == 0 {
		return nil
	}
	return st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated) //nolint:wrapcheck
}

func VersionID(ctx context.Context, st store.Store, c repo.Config, id Identity) (int64, error) {
	p, err := st.Packages().Find(ctx, c.Key, id.Name)
	if err != nil {
		return 0, fmt.Errorf("luarocks: package %s: %w", id.Name, err)
	}
	v, err := st.Packages().Version(ctx, p.ID, id.Version)
	if err != nil {
		return 0, fmt.Errorf("luarocks: version %s: %w", id.Version, err)
	}
	return v.ID, nil
}

// VersionFile resolves the numeric version id returned to the native upload
// client. It returns the rockspec's flat filename and refuses a version that
// has since been deleted. Scanning packages avoids ephemeral server state.
func VersionFile(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	wanted int64,
) (Identity, error) {
	page := store.Page{Limit: store.MaxLimit}
	for {
		ps, next, err := st.Packages().List(ctx, c.Key, page)
		if err != nil {
			return Identity{}, fmt.Errorf("luarocks: list packages: %w", err)
		}
		for _, p := range ps {
			vs, err := st.Packages().Versions(ctx, p.ID)
			if err != nil {
				return Identity{}, fmt.Errorf("luarocks: list versions: %w", err)
			}
			for _, v := range vs {
				if v.ID == wanted {
					_, rev, _ := strings.Cut(v.Version, "-")
					return Identity{
						Name:     p.Name,
						Version:  v.Version,
						Revision: rev,
						Arch:     "rockspec",
					}, nil
				}
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return Identity{}, store.ErrNotFound
}
