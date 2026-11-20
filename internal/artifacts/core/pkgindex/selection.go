package pkgindex

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// IndexStore is the existing metadata unit of work behind package projections.
// Bounded capabilities are checked at use, so construction reads no dependency.
type IndexStore interface {
	tx.Runner
	Repos() port.RepoStore
	Packages() port.PackageStore
}

type indexReader struct {
	store   IndexStore
	formats format.Registry
	layouts format.Layouts
}

// NewReader publishes index metadata without exposing artifacts' core models.
// It performs no I/O. The caller supplies current authority; each member read
// also checks its descriptive parent context so a stale/mixed reference cannot
// cross a repository's ownership or package identity. The optional layout reader
// resolves descriptor overrides; an embedding without one uses bundled layouts.
func NewReader(store IndexStore, formats format.Registry, layouts ...format.Layouts) api.PackageIndex {
	r := indexReader{store: store, formats: formats}
	if len(layouts) > 0 {
		r.layouts = layouts[0]
	}
	return r
}

func (r indexReader) sources() (port.RepositoryIndexPages, port.PackageIndexPages, error) {
	if r.store == nil {
		return nil, nil, fmt.Errorf("package index store unavailable: %w", kernel.ErrStore)
	}
	repositories, ok := r.store.Repos().(port.RepositoryIndexPages)
	if !ok {
		return nil, nil, fmt.Errorf("bounded repository index reader unavailable: %w", kernel.ErrStore)
	}
	packages, ok := r.store.Packages().(port.PackageIndexPages)
	if !ok {
		return nil, nil, fmt.Errorf("bounded package index reader unavailable: %w", kernel.ErrStore)
	}
	return repositories, packages, nil
}

func (r indexReader) repository(ctx context.Context, source port.RepositoryIndexPages, expected api.IndexRepository) error {
	actual, err := source.IndexRepository(ctx, expected.Key)
	if err != nil {
		return fmt.Errorf("package repository context: %w", err)
	}
	if actual.Class != repo.Local && actual.Class != repo.Remote {
		return fmt.Errorf("repository has no stored package index: %w", kernel.ErrInvalid)
	}
	if actual.Project != expected.Project || actual.Class != expected.Class || actual.PackageType != expected.PackageType ||
		!slices.Equal(actual.Environments, expected.Environments) {
		return fmt.Errorf("package repository context changed: %w", kernel.ErrConflict)
	}
	return nil
}

func (r indexReader) packageReference(ctx context.Context, source port.RepositoryIndexPages, ref api.IndexPackage) error {
	if err := r.repository(ctx, source, ref.Repository); err != nil {
		return err
	}
	p, err := r.store.Packages().Get(ctx, ref.ID)
	if err != nil {
		return fmt.Errorf("package reference: %w", err)
	}
	if p.RepoKey != ref.Repository.Key || p.Type != ref.Repository.PackageType || p.Name != ref.Name {
		return kernel.ErrNotFound
	}
	return nil
}

func (r indexReader) Repositories(ctx context.Context, query api.IndexRepositoryQuery, page kernel.Page) ([]api.IndexRepository, string, error) {
	repositories, _, err := r.sources()
	if err != nil {
		return nil, "", err
	}
	out, next, err := repositories.IndexRepositories(ctx, query, page)
	if err != nil {
		return nil, "", fmt.Errorf("index repositories: %w", err)
	}
	return out, next, nil
}

func (r indexReader) Packages(ctx context.Context, ref api.IndexRepository, page kernel.Page) ([]api.IndexPackage, string, error) {
	repositories, packages, err := r.sources()
	if err != nil {
		return nil, "", err
	}
	var out []api.IndexPackage
	var next string
	err = r.store.Tx(ctx, func(ctx context.Context) error {
		if err := r.repository(ctx, repositories, ref); err != nil {
			return err
		}
		var err error
		out, next, err = packages.IndexPackages(ctx, ref, page)
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("index packages: %w", err)
	}
	return out, next, nil
}

func (r indexReader) Versions(ctx context.Context, ref api.IndexPackage, keep int, page kernel.Page) ([]api.IndexVersion, string, error) {
	if keep < 0 {
		return nil, "", fmt.Errorf("negative version retention: %w", kernel.ErrInvalid)
	}
	repositories, packages, err := r.sources()
	if err != nil {
		return nil, "", err
	}
	var out []api.IndexVersion
	var next string
	err = r.store.Tx(ctx, func(ctx context.Context) error {
		if err := r.packageReference(ctx, repositories, ref); err != nil {
			return err
		}
		var err error
		out, next, err = packages.IndexVersions(ctx, ref, keep, page)
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("index versions: %w", err)
	}
	return out, next, nil
}

func (r indexReader) Version(ctx context.Context, ref api.IndexVersion, keep int) (api.IndexVersion, error) {
	if keep < 0 {
		return api.IndexVersion{}, fmt.Errorf("negative version retention: %w", kernel.ErrInvalid)
	}
	repositories, packages, err := r.sources()
	if err != nil {
		return api.IndexVersion{}, err
	}
	var out api.IndexVersion
	err = r.store.Tx(ctx, func(ctx context.Context) error {
		if err := r.packageReference(ctx, repositories, ref.Package); err != nil {
			return err
		}
		var err error
		out, err = packages.IndexVersion(ctx, ref, keep)
		return err
	})
	if err != nil {
		return api.IndexVersion{}, fmt.Errorf("current indexed version: %w", err)
	}
	return out, nil
}

func (r indexReader) Files(ctx context.Context, ref api.IndexVersion, properties map[string][]string, page kernel.Page) ([]api.IndexFile, string, error) {
	repositories, packages, err := r.sources()
	if err != nil {
		return nil, "", err
	}
	var out []api.IndexFile
	var next string
	err = r.store.Tx(ctx, func(ctx context.Context) error {
		if err := r.packageReference(ctx, repositories, ref.Package); err != nil {
			return err
		}
		current, err := r.store.Packages().Version(ctx, ref.Package.ID, ref.Version)
		if err != nil {
			return fmt.Errorf("index version reference: %w", err)
		}
		if current.ID != ref.ID {
			return kernel.ErrNotFound
		}
		var selectLead format.CleanupLeadSelector
		if r.formats != nil {
			handler := r.formats(ref.Package.Repository.PackageType)
			if resolver, ok := handler.(format.CleanupLayoutLeadResolver); ok {
				name, layoutErr := repositories.IndexRepositoryLayout(ctx, ref.Package.Repository.Key)
				if layoutErr != nil {
					return fmt.Errorf("index descriptor layout reference: %w", layoutErr)
				}
				name = cmp.Or(name, repo.LayoutMaven2)
				var compiled *layout.Layout
				if r.layouts != nil {
					compiled, layoutErr = r.layouts.Layout(ctx, name)
				} else {
					compiled, layoutErr = layout.Lookup(name)
				}
				if layoutErr != nil && !errors.Is(layoutErr, layout.ErrPattern) {
					return fmt.Errorf("index descriptor layout: %w", layoutErr)
				}
				selectLead, err = resolver.CleanupLeadWithLayout(compiled, current)
			} else if resolver, ok := handler.(format.CleanupLeadResolver); ok {
				selectLead, err = resolver.CleanupLead(current)
			}
			if err != nil {
				return fmt.Errorf("classify indexed version lead: %w", err)
			}
		}
		out, next, err = packages.IndexFiles(ctx, ref, properties, page)
		if err == nil && selectLead != nil {
			for i := range out {
				lead := selectLead(out[i])
				out[i].Lead = &lead
			}
		}
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("index files: %w", err)
	}
	return out, next, nil
}
