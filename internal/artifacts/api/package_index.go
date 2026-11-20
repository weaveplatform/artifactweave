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
	// Class optionally narrows stored local/remote indexes. Cleanup supplies
	// Local: remote/virtual repositories are outside package cleanup scope.
	Class        repo.Class
	PackageTypes []repo.PackageType
	Environments []string
	AllProjects  bool
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
	// Lead is nil when this format has no lead contract. A nonnil value comes
	// from the format's metadata-only classifier, never a guessed blob path.
	Lead *IndexLead
}

// IndexLead identifies a format's cleanup lead among linked version files.
// Single selects the greatest Rank, then TieBreak, across all file pages;
// otherwise every Found file is a lead. The rank is not a continuation key.
type IndexLead struct {
	Found, Single bool
	Rank          int64
	TieBreak      string
}

// PackageIndex supplies bounded, deterministic index projections. The owning
// use case refreshes current authority and repository scope before each page;
// these snapshots alone never authorize deletion. File traversal stays in the
// derived index and reads metadata only, without accessing blob contents.
// Each call joins the caller's metadata transaction. Parent identities and
// ownership/environment snapshots must remain current: missing parents return
// ErrNotFound, changed repository context ErrConflict, invalid limits/cursors
// ErrInvalid, and unavailable bounded adapter capabilities ErrStore (wrapped
// kernel sentinels). Callers must not share a transaction across goroutines.
// Continuations are opaque kernel.Page tokens; an empty continuation ends a
// walk, including when a page has no matching rows. Concurrent deletion or
// insertion need not preserve an earlier selection snapshot.
type PackageIndex interface {
	// Repositories filters before LIMIT and orders by repository key, bytewise.
	// Empty types/repositories/project scope admits none; AllProjects explicitly
	// opens the project scope. Only stored local/remote indexes are available.
	Repositories(context.Context, IndexRepositoryQuery, kernel.Page) ([]IndexRepository, string, error)
	// Packages orders existing index rows by opaque package name, bytewise.
	Packages(context.Context, IndexRepository, kernel.Page) ([]IndexPackage, string, error)
	// Versions orders traversal by version string, bytewise. The int is the
	// keep count: negative is invalid; zero still protects the newest version.
	// Protected uses the complete comparator set, with version string breaking
	// tied order keys, before caller inclusion/exclusion filtering.
	Versions(context.Context, IndexPackage, int, kernel.Page) ([]IndexVersion, string, error)
	// Version refreshes identity and protection against the current complete
	// version set using the same keep-count semantics as Versions; a snapshot's
	// old Protected value cannot authorize selection. A replaced ID is a miss.
	Version(context.Context, IndexVersion, int) (IndexVersion, error)
	// Files orders linked files by item ID and returns one coherent content,
	// requested-property and download-statistics projection per statement. Map
	// keys select property names; values are caller criteria, not query filters.
	// Lead comes from this format's classifier, without reading blob contents.
	Files(context.Context, IndexVersion, map[string][]string, kernel.Page) ([]IndexFile, string, error)
}
