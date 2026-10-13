// Package pkgindex maintains the package index of decision 0018 for the
// format handlers: which package and version each file belongs to. The item
// tree stays the source of truth; the index is derived from it.
package pkgindex

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Version names one version of a package in a repository.
type Version struct {
	RepoKey     string
	PackageType repo.PackageType
	Package     string
	Version     string
}

// Link records that the item with itemID is a file of v, creating the
// package and version when they are new.
func Link(ctx context.Context, st store.Store, v Version, itemID int64, at time.Time) error {
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: v.RepoKey, Name: v.Package, Type: v.PackageType, Created: at,
	})
	if err != nil {
		return fmt.Errorf("pkgindex: package %s: %w", v.Package, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, v.Version)
	if errors.Is(err, store.ErrNotFound) {
		pv, err = st.Packages().PutVersion(ctx, types.PackageVersion{
			PackageID: p.ID, Version: v.Version, Created: at,
		})
	}
	if err != nil {
		return fmt.Errorf("pkgindex: version %s of %s: %w", v.Version, v.Package, err)
	}
	if err := st.Packages().
		LinkFile(ctx, types.VersionFile{VersionID: pv.ID, ItemID: itemID}); err != nil {
		return fmt.Errorf("pkgindex: link %s %s: %w", v.Package, v.Version, err)
	}
	return nil
}

// VersionOf returns the version the item with itemID belongs to; ok is
// false when it belongs to none.
func VersionOf(
	ctx context.Context,
	st store.Store,
	itemID int64,
) (types.PackageVersion, bool, error) {
	pv, err := st.Packages().VersionOf(ctx, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return pv, false, nil
	}
	if err != nil {
		return pv, false, fmt.Errorf("pkgindex: version of item %d: %w", itemID, err)
	}
	return pv, true, nil
}

// Prune runs after items were deleted, which removes their file links with
// them: each of versions left with no files is deleted, and with it a
// package left with no versions.
func Prune(ctx context.Context, st store.Store, versions []types.PackageVersion) error {
	done := map[int64]bool{}
	for _, pv := range versions {
		if done[pv.ID] {
			continue
		}
		done[pv.ID] = true
		files, err := st.Packages().Files(ctx, pv.ID)
		if err != nil {
			return fmt.Errorf("pkgindex: files of version %d: %w", pv.ID, err)
		}
		if len(files) > 0 {
			continue
		}
		if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
			return fmt.Errorf("pkgindex: delete version %d: %w", pv.ID, err)
		}
		rest, err := st.Packages().Versions(ctx, pv.PackageID)
		if err != nil {
			return fmt.Errorf("pkgindex: versions of package %d: %w", pv.PackageID, err)
		}
		if len(rest) == 0 {
			if err := st.Packages().Delete(ctx, pv.PackageID); err != nil {
				return fmt.Errorf("pkgindex: delete package %d: %w", pv.PackageID, err)
			}
		}
	}
	return nil
}
