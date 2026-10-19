package npm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// KindSnapshots applies maxUniqueSnapshots to a package's pre-releases,
// keyed by "<repository key>/<package name>": repository keys hold no
// slash, and a scoped name does. It is periodic work, run on Maven's timer
// (maven.snapshot_cleanup_interval), since 7.x runs one cleanup for every
// package type it applies the limit to: a deploy marks the package and
// the timer deletes the builds over the limit (decision 0034).
const KindSnapshots = "npm.snapshots"

// remover is recorded as the deleter of what the cleanup removes:
// Artifactory's system user, as Maven's cleanup records its own deletes
// (maven.Deriver).
const remover = "_system_"

// queueCleanup marks t's package for its cleanup when a pre-release lands
// at its canonical path in a local repository with a limit, as 7.x's
// cleanup notes each integration file deployed. 7.x applies the limit to
// npm repositories too (known issue RTDEV-45622, from 7.35.4).
func queueCleanup(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	t *Tarball,
) error {
	if it.Cached || c.Class != repo.Local || format.UniqueSnapshots(c) == 0 ||
		!prerelease(t.Version) {
		return nil
	}
	if err := env.Enqueue(ctx, KindSnapshots, c.Key+"/"+t.Name, nil); err != nil {
		return fmt.Errorf("npm: queue the cleanup of %s: %w", t.Name, err)
	}
	return nil
}

// prereleaseBuild is one pre-release the cleanup counts, as the unique
// build of its base version.
type prereleaseBuild struct {
	version, path string
	// created is when its tarball was first stored, which dates it, as
	// Artifactory dates a build by its earliest-created file.
	created time.Time
}

// cleanPrereleases applies maxUniqueSnapshots to package name in c. A
// pre-release is a snapshot, as the release and snapshot handling classes
// it (checkPolicy), and each pre-release of one base version, such as
// 1.0.0-beta.1 and 1.0.0-rc.1 of 1.0.0, is one unique build of that
// snapshot: Artifactory groups the builds it counts by base revision. For
// each base version, while more pre-releases are indexed than the limit
// allows, the oldest goes, by when its tarball was first stored, then by
// SemVer, the lower first. Releases are never counted. The limit is
// format.UniqueSnapshots, read as the run finds it.
//
// [I] 7.x's npm-default layout reads a pre-release's last hyphen as the
// one between module and base revision, so it gives no integration
// revision; which npm versions 7.x's cleanup counts, and how it groups
// them, are unknown (#216).
//
// The deletes go through the item service (Env.Remove), into the trash can
// when it is on, so the index sees them, latest moves by rule 4, and the
// folders they empty are pruned.
func cleanPrereleases(ctx context.Context, env format.Env, c repo.Config, name string) error {
	limit := format.UniqueSnapshots(c)
	if limit == 0 {
		return nil
	}
	p, err := env.Store.Packages().Find(ctx, c.Key, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("npm: package %s: %w", name, err)
	}
	versions, err := env.Store.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("npm: versions of %s: %w", name, err)
	}
	byBase := map[string][]prereleaseBuild{}
	for _, v := range versions {
		base, _, ok := strings.Cut(v.Version, "-")
		if !ok {
			continue
		}
		b, err := buildOf(ctx, env.Store, v)
		if err != nil {
			return err
		}
		// A version without its tarball is pruned with the delete that
		// took it; an empty path would name the repository's root.
		if b.path != "" {
			byBase[base] = append(byBase[base], b)
		}
	}
	var doomed []string
	for _, base := range slices.Sorted(maps.Keys(byBase)) {
		builds := byBase[base]
		slices.SortFunc(builds, func(a, b prereleaseBuild) int {
			return cmp.Or(a.created.Compare(b.created), types.CompareSemver(a.version, b.version))
		})
		for _, b := range builds[:max(len(builds)-limit, 0)] {
			doomed = append(doomed, b.path)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	slices.Sort(doomed)
	return env.Remove(ctx, c, doomed, remover) //nolint:wrapcheck // the item service's own
}

// buildOf is v as a build the cleanup counts: its tarball, the one file of
// an npm version.
func buildOf(ctx context.Context, st store.Store, v types.PackageVersion) (prereleaseBuild, error) {
	files, err := st.Packages().Files(ctx, v.ID)
	if err != nil {
		return prereleaseBuild{}, fmt.Errorf("npm: files of version %s: %w", v.Version, err)
	}
	b := prereleaseBuild{version: v.Version}
	for _, id := range files {
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return prereleaseBuild{}, fmt.Errorf("npm: tarball of version %s: %w", v.Version, err)
		}
		b.path, b.created = it.FullPath(), it.Created
	}
	return b, nil
}
