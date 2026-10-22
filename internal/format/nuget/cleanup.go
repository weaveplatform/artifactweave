package nuget

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// KindSnapshots applies a package's maxUniqueSnapshots (decision 0038),
// keyed by "<repository key>/<package>", the package being the id
// lower-cased; repository keys hold no slash. It is periodic work: a
// deploy of a pre-release marks the package, and the runtime runs the
// marked packages every nuget.snapshot_cleanup_interval
// (work.Queue.HandleEvery), as 7.x's cleanup runs every five minutes (KB
// 000005403).
const KindSnapshots = "nuget.snapshots"

// Deleter is who the cleanup's deletes are recorded as, Artifactory's
// system user.
const Deleter = "_system_"

// WorkKinds are the kinds the NuGet format runs. A reindex is every
// format's format.KindReindex.
func (Handler) WorkKinds() []string { return []string{KindSnapshots, KindSymbols} }

// Owed makes a debugger read wait for committed source changes before
// looking up the extracted item. Package metadata and snapshot cleanup
// need no work on read.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if c.Class == repo.Local && !cached && strings.HasPrefix(p, symbolPrefix) {
		return []format.WorkRef{{Kind: KindSymbols, Key: c.Key}}
	}
	return nil
}

// Work runs the cleanup of the package the item names. A repository that
// is gone, or no longer a NuGet local repository, owes nothing, and nor
// does an item of a kind the format no longer runs, which an earlier build
// may have left in the queue.
func (Handler) Work(ctx context.Context, env format.Env, it types.WorkItem) error {
	if it.Kind == KindSymbols {
		return symbolWork(ctx, env, it.Key)
	}
	key, name, ok := strings.Cut(it.Key, "/")
	if it.Kind != KindSnapshots || !ok {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("nuget: repository %s: %w", key, err)
	case !Serves(c.PackageType) || (c.Class != repo.Local && c.Class != repo.Remote):
		return nil
	}
	return cleanSnapshots(ctx, env, c, name)
}

// markCleanup marks the package of key for its cleanup when a pre-release
// package, it, lands in a local repository with a limit.
func markCleanup(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	key versionKey,
) error {
	if (c.Class != repo.Local && c.Class != repo.Remote) ||
		it.Cached != (c.Class == repo.Remote) || format.UniqueSnapshots(c) == 0 {
		return nil
	}
	if err := env.Enqueue(ctx, KindSnapshots, c.Key+"/"+key.pkg, nil); err != nil {
		return fmt.Errorf("nuget: queue the cleanup of %s: %w", key.pkg, err)
	}
	return nil
}

// cleanSnapshots applies package name's maxUniqueSnapshots in c (decision
// 0038): the pre-release versions with a package that share their numeric
// parts are the unique snapshots of one release, and while a release has
// more than format.UniqueSnapshots(c), read as the run finds it, the one
// deployed first goes, a tie broken by NuGet order. Releases are never
// counted, nor is a version with symbols alone. A version goes with all its
// files, its .nupkg and .snupkg, through the item service in one
// transaction, so the trash can and the index see them.
func cleanSnapshots(ctx context.Context, env format.Env, c repo.Config, name string) error {
	limit := format.UniqueSnapshots(c)
	if limit == 0 {
		return nil
	}
	st := env.Store
	p, err := st.Packages().Find(ctx, c.Key, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("nuget: package %s: %w", name, err)
	}
	vs, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("nuget: versions of %s: %w", name, err)
	}
	releases := map[string][]types.PackageVersion{}
	for _, v := range vs {
		m, err := DecodeMetadata(v.Metadata)
		if err != nil {
			return err
		}
		if !m.HasPackage() || !m.IsPrerelease() {
			continue
		}
		release, _, _ := strings.Cut(v.Version, "-")
		releases[release] = append(releases[release], v)
	}
	var doomed []string
	for _, r := range slices.Sorted(maps.Keys(releases)) {
		list := releases[r]
		slices.SortFunc(list, func(a, b types.PackageVersion) int {
			return cmp.Or(a.Created.Compare(b.Created), types.CompareNuGet(a.Version, b.Version))
		})
		for _, v := range list[:max(len(list)-limit, 0)] {
			paths, err := filePaths(ctx, st, v)
			if err != nil {
				return err
			}
			doomed = append(doomed, paths...)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	slices.Sort(doomed)
	return env.Remove(ctx, c, doomed, Deleter) //nolint:wrapcheck // the item service's own
}

// filePaths are the paths of v's files.
func filePaths(ctx context.Context, st store.Store, v types.PackageVersion) ([]string, error) {
	ids, err := st.Packages().Files(ctx, v.ID)
	if err != nil {
		return nil, fmt.Errorf("nuget: files of version %s: %w", v.Version, err)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("nuget: file %d of version %s: %w", id, v.Version, err)
		}
		out = append(out, it.FullPath())
	}
	return out, nil
}
