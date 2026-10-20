package helm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// index makes the chart archive it, which holds ch, a file of the version
// ch names, beside the other archives of that name and version, and sets
// the properties that follow from ch. An archive that was a file of another
// version, because it held another chart before an overwrite, leaves that
// one first.
func index(ctx context.Context, env format.Env, c repo.Config, it types.Item, ch *Chart) error {
	st := env.Store
	if pv, ok, err := pkgindex.VersionOf(ctx, st, it.ID); err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	} else if ok {
		p, err := st.Packages().Get(ctx, pv.PackageID)
		if err != nil {
			return fmt.Errorf("helm: package %d: %w", pv.PackageID, err)
		}
		if p.Name != ch.Name || pv.Version != ch.Version {
			if err := detach(ctx, env, pv, it.ID); err != nil {
				return err
			}
		}
	}
	if err := setProperties(ctx, st, it, ch); err != nil {
		return err
	}
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: ch.Name, Type: c.PackageType, Created: deployed(it),
	})
	if err != nil {
		return fmt.Errorf("helm: package %s: %w", ch.Name, err)
	}
	ids := []int64{it.ID}
	var prev *Metadata
	pv, err := st.Packages().Version(ctx, p.ID, ch.Version)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return fmt.Errorf("helm: version %s of %s: %w", ch.Version, ch.Name, err)
	default:
		linked, err := st.Packages().Files(ctx, pv.ID)
		if err != nil {
			return fmt.Errorf("helm: files of %s %s: %w", ch.Name, ch.Version, err)
		}
		for _, id := range linked {
			if id != it.ID {
				ids = append(ids, id)
			}
		}
		if prev, err = previous(pv); err != nil {
			return err
		}
	}
	return settle(ctx, env, p, ch.Version, ids, map[int64]*Chart{it.ID: ch}, prev)
}

// unindex takes the file it, which holds no chart now, out of the version
// it was a file of, and reports whether it was one.
func unindex(ctx context.Context, env format.Env, it types.Item) (bool, error) {
	pv, ok, err := pkgindex.VersionOf(ctx, env.Store, it.ID)
	if err != nil || !ok {
		return false, err //nolint:wrapcheck // pkgindex wraps its own
	}
	return true, detach(ctx, env, pv, it.ID)
}

// detach makes pv's files those it has but the item with id: gone with
// its package when that leaves none, as pkgindex.Prune drops one.
func detach(ctx context.Context, env format.Env, pv types.PackageVersion, id int64) error {
	st := env.Store
	linked, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("helm: files of version %d: %w", pv.ID, err)
	}
	rest := slices.DeleteFunc(slices.Clone(linked), func(x int64) bool { return x == id })
	if len(rest) == 0 {
		return drop(ctx, st, pv)
	}
	p, err := st.Packages().Get(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("helm: package %d: %w", pv.PackageID, err)
	}
	prev, err := previous(pv)
	if err != nil {
		return err
	}
	return settle(ctx, env, p, pv.Version, rest, nil, prev)
}

// drop removes pv, and its package when no version is left.
func drop(ctx context.Context, st store.Store, pv types.PackageVersion) error {
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("helm: drop version %s: %w", pv.Version, err)
	}
	rest, err := st.Packages().Versions(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("helm: versions of package %d: %w", pv.PackageID, err)
	}
	if len(rest) > 0 {
		return nil
	}
	if err := st.Packages().Delete(ctx, pv.PackageID); err != nil {
		return fmt.Errorf("helm: drop package %d: %w", pv.PackageID, err)
	}
	return nil
}

// previous is pv's Metadata, which names its newest archive.
func previous(pv types.PackageVersion) (*Metadata, error) {
	m, err := DecodeMetadata(pv.Metadata)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// settle makes version of p the files with ids: dated and described by the
// newest of them (newestFirst). known holds the charts already read, by item ID,
// and prev the version's Metadata as it stood, whose chart serves when its
// archive is still the newest and unchanged; any other newest archive is
// read. A file whose content turns out to hold no chart of this version is
// left out, so the version never describes itself by one; none left drops
// the version.
func settle(
	ctx context.Context,
	env format.Env,
	p types.Package,
	version string,
	ids []int64,
	known map[int64]*Chart,
	prev *Metadata,
) error {
	st := env.Store
	items := make([]types.Item, 0, len(ids))
	for _, id := range ids {
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("helm: item %d: %w", id, err)
		}
		items = append(items, it)
	}
	slices.SortFunc(items, newestFirst)
	for i, it := range items {
		ch, err := describe(ctx, env, it, known, prev)
		if err != nil {
			return err
		}
		if ch == nil || ch.Name != p.Name || ch.Version != version {
			continue
		}
		files := make([]int64, 0, len(items)-i)
		for _, rest := range items[i:] {
			files = append(files, rest.ID)
		}
		slices.Sort(files)
		_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
			Version: version, Created: deployed(it), Metadata: metadataOf(ch, it).encode(),
			Files: files,
		})
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	pv, err := st.Packages().Version(ctx, p.ID, version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("helm: version %s of %s: %w", version, p.Name, err)
	}
	return drop(ctx, st, pv)
}

// describe is the chart the archive it holds: known's, prev's when prev
// describes this very archive, or else what its content gives.
func describe(
	ctx context.Context,
	env format.Env,
	it types.Item,
	known map[int64]*Chart,
	prev *Metadata,
) (*Chart, error) {
	if ch, ok := known[it.ID]; ok {
		return ch, nil
	}
	if prev != nil && prev.Path == it.FullPath() && prev.Digest == it.Actual.SHA256 {
		return &prev.Chart, nil
	}
	return ReadStored(ctx, env.Blobs, it.Actual.SHA256)
}

// handOver runs before the archive it, a file of pv, is deleted: when it is
// the version's newest archive and another is left, that one's Chart.yaml
// describes the version from now on (decision 0039). The links are left to
// the item service, which drops the deleted file's and prunes a version left
// with none, so pv keeps its ID, which the item service has read.
func handOver(ctx context.Context, env format.Env, pv types.PackageVersion, it types.Item) error {
	m, err := DecodeMetadata(pv.Metadata)
	if err != nil {
		return err
	}
	if m.Path != it.FullPath() {
		return nil
	}
	st := env.Store
	linked, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("helm: files of version %d: %w", pv.ID, err)
	}
	p, err := st.Packages().Get(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("helm: package %d: %w", pv.PackageID, err)
	}
	var rest []types.Item
	for _, id := range linked {
		if id == it.ID {
			continue
		}
		other, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("helm: item %d: %w", id, err)
		}
		rest = append(rest, other)
	}
	slices.SortFunc(rest, newestFirst)
	for _, next := range rest {
		ch, err := ReadStored(ctx, env.Blobs, next.Actual.SHA256)
		if err != nil {
			return err
		}
		if ch == nil || ch.Name != p.Name || ch.Version != pv.Version {
			continue
		}
		pv.Created, pv.Metadata = deployed(next), metadataOf(ch, next).encode()
		if _, err := st.Packages().PutVersion(ctx, pv); err != nil {
			return fmt.Errorf("helm: version %s of %s: %w", pv.Version, p.Name, err)
		}
		return nil
	}
	return nil
}

// setProperties sets chart.name, chart.version and, when ch has one,
// chart.appVersion on it, removing an appVersion ch lacks. Writing them
// straight to the store runs no Annotated hook, which the index has none
// of anyway. They follow from the content, so they are written at the time
// the content was, it.Updated.
func setProperties(ctx context.Context, st store.Store, it types.Item, ch *Chart) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("helm: properties of %s: %w", it.FullPath(), err)
	}
	want := types.Properties{
		PropName:    {types.TruncatePropertyValue(ch.Name, types.PropertyValueMax)},
		PropVersion: {types.TruncatePropertyValue(ch.Version, types.PropertyValueMax)},
	}
	var remove []string
	if ch.AppVersion != "" {
		want[PropAppVersion] = []string{
			types.TruncatePropertyValue(ch.AppVersion, types.PropertyValueMax),
		}
	} else if _, ok := props[PropAppVersion]; ok {
		remove = append(remove, PropAppVersion)
	}
	set := types.Properties{}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if !slices.Equal(props[k], want[k]) {
			set[k] = want[k]
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, remove, it.Updated); err != nil {
		return fmt.Errorf("helm: properties of %s: %w", it.FullPath(), err)
	}
	return nil
}
