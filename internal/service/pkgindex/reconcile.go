package pkgindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// pageSize is how many packages each read of a repository's index asks for.
const pageSize = 200

// Indexed is a package as its format reads it from the item tree: what the
// index must hold for it.
type Indexed struct {
	Name string
	// Created dates the package when Reconcile creates it. A package already
	// indexed keeps its date, which its first push set.
	Created  time.Time
	Versions []IndexedVersion
}

// IndexedVersion is one version of an Indexed package.
type IndexedVersion struct {
	Version  string
	Created  time.Time
	Metadata []byte
	// Files are the IDs of the version's items.
	Files []int64
}

// Sync makes version v of package p what the tree says: created when
// absent, dated and described as v, and linked to exactly v.Files. A
// version linking anything else is recreated with its dates, download
// statistics and metadata, as the store cannot unlink one file.
func Sync(
	ctx context.Context,
	st store.Store,
	p types.Package,
	v IndexedVersion,
) (types.PackageVersion, error) {
	pv, err := st.Packages().Version(ctx, p.ID, v.Version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		pv, err = st.Packages().PutVersion(ctx, types.PackageVersion{
			PackageID: p.ID, Version: v.Version, Created: v.Created, Metadata: v.Metadata,
		})
	case err == nil && (!pv.Created.Equal(v.Created) || !bytes.Equal(pv.Metadata, v.Metadata)):
		pv.Created, pv.Metadata = v.Created, v.Metadata
		pv, err = st.Packages().PutVersion(ctx, pv)
	}
	if err != nil {
		return pv, fmt.Errorf("pkgindex: version %s of %s: %w", v.Version, p.Name, err)
	}
	linked, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return pv, fmt.Errorf("pkgindex: files of %s %s: %w", p.Name, v.Version, err)
	}
	if slices.ContainsFunc(linked, func(id int64) bool { return !slices.Contains(v.Files, id) }) {
		if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
			return pv, fmt.Errorf("pkgindex: relink %s %s: %w", p.Name, v.Version, err)
		}
		if pv, err = st.Packages().PutVersion(ctx, pv); err != nil {
			return pv, fmt.Errorf("pkgindex: relink %s %s: %w", p.Name, v.Version, err)
		}
	}
	for _, id := range v.Files {
		if err := st.Packages().
			LinkFile(ctx, types.VersionFile{VersionID: pv.ID, ItemID: id}); err != nil {
			return pv, fmt.Errorf("pkgindex: link item %d to %s %s: %w", id, p.Name, v.Version, err)
		}
	}
	return pv, nil
}

// Reconcile brings the index of repository c in line with want, a format's
// reading of c's whole tree, which stays the source of truth (decision
// 0018): packages and versions want lacks go, and every version want lists
// is Synced, in name order so the index changes the same way on every
// store. What want keeps keeps its download statistics and its package's
// date, so on an index the format's hooks kept right Reconcile changes
// nothing. Call it inside a transaction.
func Reconcile(ctx context.Context, st store.Store, c repo.Config, want []Indexed) error {
	byName := make(map[string]Indexed, len(want))
	for _, p := range want {
		byName[p.Name] = p
	}
	for page := (store.Page{Limit: pageSize}); ; {
		pkgs, next, err := st.Packages().List(ctx, c.Key, page)
		if err != nil {
			return fmt.Errorf("pkgindex: packages of %s: %w", c.Key, err)
		}
		for _, p := range pkgs {
			if err := dropUnbacked(ctx, st, p, byName); err != nil {
				return err
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	want = slices.Clone(want)
	slices.SortFunc(want, func(a, b Indexed) int { return strings.Compare(a.Name, b.Name) })
	for _, w := range want {
		p, err := st.Packages().Ensure(ctx, types.Package{
			RepoKey: c.Key, Name: w.Name, Type: c.PackageType, Created: w.Created,
		})
		if err != nil {
			return fmt.Errorf("pkgindex: package %s: %w", w.Name, err)
		}
		versions := slices.Clone(w.Versions)
		slices.SortFunc(versions, func(a, b IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		for _, v := range versions {
			if _, err := Sync(ctx, st, p, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropUnbacked removes package p when want has no package of its name, and
// otherwise each of its versions want does not list.
func dropUnbacked(
	ctx context.Context,
	st store.Store,
	p types.Package,
	want map[string]Indexed,
) error {
	w, ok := want[p.Name]
	if !ok {
		if err := st.Packages().Delete(ctx, p.ID); err != nil {
			return fmt.Errorf("pkgindex: drop package %s: %w", p.Name, err)
		}
		return nil
	}
	vs, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("pkgindex: versions of %s: %w", p.Name, err)
	}
	for _, v := range vs {
		if slices.ContainsFunc(
			w.Versions,
			func(x IndexedVersion) bool { return x.Version == v.Version },
		) {
			continue
		}
		if err := st.Packages().DeleteVersion(ctx, v.ID); err != nil {
			return fmt.Errorf("pkgindex: drop version %s of %s: %w", v.Version, p.Name, err)
		}
	}
	return nil
}

// RecordDownload records a download of the item with itemID as its
// version's last download, when the item belongs to a version, as the item
// records its own (decision 0018).
func RecordDownload(ctx context.Context, st store.Store, itemID int64, at time.Time) error {
	pv, ok, err := VersionOf(ctx, st, itemID)
	if err != nil || !ok {
		return err
	}
	if err := st.Packages().RecordVersionDownload(ctx, pv.ID, at); err != nil {
		return fmt.Errorf("pkgindex: download of version %d: %w", pv.ID, err)
	}
	return nil
}
