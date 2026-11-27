package pkgindex

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CleanupStore supplies bounded index projections and the existing lifetime
// lock shared with repository configuration and deletion.
type CleanupStore interface {
	IndexStore
	RemoteCache() port.RemoteCacheStore
}

type cleanupMetadata struct {
	store  CleanupStore
	guards []func(context.Context) error
}

// NewCleanupMetadata opens no store or network dependency.
func NewCleanupMetadata(store CleanupStore, guards ...func(context.Context) error) api.CleanupMetadata {
	return cleanupMetadata{store: store, guards: slices.Clone(guards)}
}

func (r cleanupMetadata) Repository(ctx context.Context, key string) (api.CleanupRepository, error) {
	if r.store == nil {
		return api.CleanupRepository{}, fmt.Errorf("cleanup repository metadata unavailable: %w", kernel.ErrStore)
	}
	var out api.CleanupRepository
	err := r.store.Tx(ctx, func(ctx context.Context) error {
		for _, guard := range r.guards {
			if guard == nil {
				continue
			}
			if err := guard(ctx); err != nil {
				return fmt.Errorf("guard cleanup metadata authority: %w", err)
			}
		}
		source, err := r.store.RemoteCache().Source(ctx, key)
		if err != nil {
			return fmt.Errorf("read cleanup repository metadata: %w", err)
		}
		index, indexed := r.store.Repos().(port.RepositoryIndexPages)
		members, projected := r.store.Repos().(port.CleanupMemberReader)
		if !indexed || !projected {
			return fmt.Errorf("bounded cleanup repository metadata unavailable: %w", kernel.ErrStore)
		}
		out.Repository, err = index.IndexRepository(ctx, key)
		if err != nil {
			return fmt.Errorf("read cleanup repository metadata: %w", err)
		}
		out.Members, err = members.CleanupMembers(ctx, key)
		out.Incarnation, out.Generation = string(source.Repository), string(source.Generation)
		if err != nil {
			return fmt.Errorf("read cleanup repository members: %w", err)
		}
		return nil
	})
	if err != nil {
		return api.CleanupRepository{}, fmt.Errorf("cleanup repository context: %w", err)
	}
	if out.Incarnation == "" || out.Generation == "" {
		return api.CleanupRepository{}, fmt.Errorf("cleanup repository has no lifetime: %w", kernel.ErrStore)
	}
	return out, nil
}

func (r cleanupMetadata) Downloads(ctx context.Context, expected api.CleanupRepository, versions []api.DownloadVersion) ([]api.VersionDownload, error) {
	if r.store == nil {
		return nil, fmt.Errorf("cleanup download metadata unavailable: %w", kernel.ErrStore)
	}
	if len(versions) == 0 || len(versions) > api.MaxDownloadVersions {
		return nil, fmt.Errorf("cleanup download query exceeds its bound: %w", kernel.ErrInvalid)
	}
	requested := make(map[api.DownloadVersion]bool, len(versions))
	for _, version := range versions {
		if version.Package == "" || version.Version == "" || len(version.Package) > 4096 || len(version.Version) > 4096 ||
			!utf8.ValidString(
				version.Package,
			) || !utf8.ValidString(version.Version) || strings.ContainsRune(version.Package, 0) || strings.ContainsRune(version.Version, 0) || requested[version] {
			return nil, fmt.Errorf("cleanup download query has invalid version identities: %w", kernel.ErrInvalid)
		}
		requested[version] = true
	}
	var out []api.VersionDownload
	err := r.store.Tx(ctx, func(ctx context.Context) error {
		current, err := r.Repository(ctx, expected.Repository.Key)
		if err != nil {
			return fmt.Errorf("read cleanup repository metadata: %w", err)
		}
		if current.Incarnation != expected.Incarnation || current.Generation != expected.Generation ||
			current.Repository.Project != expected.Repository.Project || current.Repository.Class != expected.Repository.Class ||
			current.Repository.PackageType != expected.Repository.PackageType {
			return fmt.Errorf("cleanup download repository changed: %w", kernel.ErrConflict)
		}
		downloads, ok := r.store.Packages().(port.PackageDownloadEvidence)
		if !ok {
			return fmt.Errorf("bounded version download metadata unavailable: %w", kernel.ErrStore)
		}
		out, err = downloads.VersionDownloads(ctx, current.Repository.Key, versions)
		if err != nil {
			return fmt.Errorf("read cleanup download batch: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cleanup version downloads: %w", err)
	}
	for _, row := range out {
		if !requested[row.DownloadVersion] || row.ID <= 0 || row.Revision == "" {
			return nil, fmt.Errorf("cleanup download projection returned an unrequested identity: %w", kernel.ErrStore)
		}
		delete(requested, row.DownloadVersion)
	}
	return out, nil
}
