package api

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// IndexRepositoryQuery narrows descriptive index reads before package paging.
// An empty Projects list admits none unless AllProjects is explicitly set;
// the empty project key denotes unassigned repositories. Environments nil
// admits every environment. This query is not an authorization capability.
type IndexRepositoryQuery struct {
	Projects, Repositories, ExcludedRepositories []string
	PackageTypes                                 []repo.PackageType
	Environments                                 []string
	AllProjects                                  bool
}

// IndexRepository captures ownership without opening repository credentials.
// A caller holds its current publication guard across each authorized page.
type IndexRepository struct {
	Key, Project string
	Class        repo.Class
	PackageType  repo.PackageType
	Environments []string
}

// IndexPackage identifies one existing index row in an admitted repository.
type IndexPackage struct {
	Repository IndexRepository
	ID         int64
	Name       string
}

// IndexVersion is a descriptive snapshot, including the retention cutoff
// computed from the package's comparator order before inclusion filtering.
// Version string, not the mutable comparator rank, is the continuation key.
// CLN-04 must recheck current identity, criteria and authority before effects.
type IndexVersion struct {
	Package                 IndexPackage
	ID                      int64
	Version                 string
	OrderKey                int64
	Protected               bool
	Created, LastDownloaded time.Time
}

// IndexFile captures a linked item's content and mutable selection facts. IDs
// and Created survive overwrites, so neither is sufficient as a deletion fence.
// Properties contains only requested values that are currently present; it does
// not hydrate arbitrary properties or expose upstream URLs and actor names.
type IndexFile struct {
	Version                            IndexVersion
	ID                                 int64
	Path                               string
	Cached                             bool
	Size                               int64
	Actual                             kernel.Checksums
	Created, Modified, Updated         time.Time
	PropertiesModified, LastDownloaded time.Time
	Properties                         map[string][]string
}

// PackageIndex supplies bounded, deterministic index projections. The owning
// use case refreshes current authority and repository scope before each page;
// these snapshots alone never authorize deletion. File traversal stays in the
// derived index and reads metadata only, without accessing blob contents.
type PackageIndex interface {
	Repositories(context.Context, IndexRepositoryQuery, kernel.Page) ([]IndexRepository, string, error)
	Packages(context.Context, IndexRepository, kernel.Page) ([]IndexPackage, string, error)
	Versions(context.Context, IndexPackage, int, kernel.Page) ([]IndexVersion, string, error)
	Files(context.Context, IndexVersion, map[string][]string, kernel.Page) ([]IndexFile, string, error)
}
