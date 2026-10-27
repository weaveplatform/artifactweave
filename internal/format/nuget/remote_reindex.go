package nuget

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// reindexRemote rebuilds from the actual cached files. Upstream representations
// live outside this tree and remain untouched. Decompression and hashing finish
// before the publication transaction takes the source lifetime lock shared with
// cache admission, invalidation and repository edits.
func reindexRemote(ctx context.Context, env format.Env, c repo.Config) error {
	source, err := env.Store.RemoteCache().Source(ctx, c.Key)
	if err != nil {
		return fmt.Errorf("nuget: reindex source: %w", err)
	}
	if env.ReindexSource != nil && source != *env.ReindexSource {
		return staleReindex()
	}
	hits, err := env.Store.Items().Search(ctx, candidateQuery(c, false), aql.Options{})
	if err != nil {
		return fmt.Errorf("nuget: reindex candidates: %w", err)
	}
	inspected := map[int64]*Nuspec{}
	snapshot := map[int64]types.Item{}
	for _, hit := range hits {
		snapshot[hit.Item.ID] = hit.Item
		if kindOf(hit.Item.FullPath()) == other {
			continue
		}
		n, err := inspectReindexPackage(ctx, env, hit.Item)
		if err != nil {
			return fmt.Errorf("nuget: cached package %s: %w", hit.Item.FullPath(), err)
		}
		inspected[hit.Item.ID] = n
	}
	err = env.Store.Tx(
		ctx,
		func(ctx context.Context) error {
			current, err := env.Store.RemoteCache().Source(ctx, c.Key)
			if errors.Is(err, store.ErrNotFound) || err == nil && current != source {
				return staleReindex()
			}
			if err != nil {
				return fmt.Errorf("nuget: reindex publication source: %w", err)
			}
			if env.CheckReindex != nil {
				if err := env.CheckReindex(ctx); err != nil {
					return fmt.Errorf("nuget: current reindex authority: %w", err)
				}
			}
			// Source() locks the repository lifetime until commit. No current cache
			// admission or deletion can pass us after this snapshot is checked.
			latest, err := env.Store.Items().Search(ctx, candidateQuery(c, true), aql.Options{})
			if err != nil {
				return fmt.Errorf("nuget: reindex publication candidates: %w", err)
			}
			if !sameReindexItems(snapshot, latest) {
				return fmt.Errorf(
					"nuget: cached packages changed during reindex: %w",
					store.ErrConflict,
				)
			}
			groups := map[versionKey][]member{}
			for _, hit := range latest {
				n, k := inspected[hit.Item.ID], kindOf(hit.Item.FullPath())
				if n == nil || !canonical(hit.Item.FullPath(), k, n) {
					continue
				}
				if k == pkgFile {
					if err := patchProperties(
						ctx,
						env.Store,
						hit.Item,
						hit.Properties,
						n,
						env.Now(),
					); err != nil {
						return err
					}
				}
				key := keyOf(n)
				groups[key] = append(groups[key], member{it: hit.Item, kind: k, nuspec: n})
			}
			return reconcile(ctx, env, c, groups, Metadata{})
		},
	)
	if err != nil {
		return fmt.Errorf("nuget: reindex publication: %w", err)
	}
	return nil
}

func staleReindex() error {
	return fmt.Errorf("%w: nuget reindex source changed; schedule a new reindex", work.ErrPermanent)
}

func sameReindexItems(before map[int64]types.Item, after []aql.Hit) bool {
	if len(before) != len(after) {
		return false
	}
	for _, hit := range after {
		old, ok := before[hit.Item.ID]
		now := hit.Item
		// Repair format-owned annotations against their current values, retaining
		// unrelated edits. A property edit does not change the validated bytes.
		old.PropertiesModified = now.PropertiesModified
		if !ok || old != now {
			return false
		}
	}
	return true
}

func inspectReindexPackage(ctx context.Context, env format.Env, it types.Item) (*Nuspec, error) {
	rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if info.Size != it.Size {
		return nil, fmt.Errorf("cached size: %w", blob.ErrDigestMismatch)
	}
	// Content-addressed storage is not a substitute for checking the bytes being
	// indexed: a corrupt object must not repair metadata with unverified content.
	h := blob.NewHasher()
	count, err := io.Copy(h, io.LimitReader(&validationReader{ctx: ctx, r: rc}, it.Size+1))
	if err != nil {
		return nil, fmt.Errorf("hash: %w", err)
	}
	if count != it.Size {
		return nil, fmt.Errorf("cached length: %w", blob.ErrDigestMismatch)
	}
	if err = blob.Verify(blob.Digests(it.Actual), h.Sum()); err != nil {
		return nil, fmt.Errorf("cached integrity: %w", err)
	}
	ra := archive.ReaderAt(rc)
	if err = remoteZIPDirectory(ra, info.Size); err != nil {
		return nil, err
	}
	n, err := ReadPackage(ra, info.Size)
	if err != nil {
		return nil, err
	}
	if err = validateRemoteContents(ctx, ra, info.Size); err != nil {
		return nil, err
	}
	return n, nil
}
