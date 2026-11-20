package pkgindex

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type indexReadStore struct {
	IndexStore
	repositories port.RepoStore
	packages     port.PackageStore
}

func (s indexReadStore) Repos() port.RepoStore       { return s.repositories }
func (s indexReadStore) Packages() port.PackageStore { return s.packages }

type (
	repositoryWithoutIndex struct{ port.RepoStore }
	packagesWithoutIndex   struct{ port.PackageStore }
)

func indexReadFixture(t *testing.T) (*inmem.Store, api.IndexVersion) {
	t.Helper()
	ctx, db := t.Context(), inmem.New()
	if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "packages", Class: repo.Local, PackageType: repo.NPM, ProjectKey: "alpha"})); err != nil {
		t.Fatal(err)
	}
	r, err := db.Repos().(port.RepositoryIndexPages).IndexRepository(ctx, "packages")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Packages().Ensure(ctx, model.Package{RepoKey: r.Key, Type: repo.NPM, Name: "widget"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := db.Items().Put(ctx, model.Item{RepoKey: r.Key, Name: "one.pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Packages().LinkFile(ctx, model.VersionFile{VersionID: v.ID, ItemID: it.ID}); err != nil {
		t.Fatal(err)
	}
	return db, api.IndexVersion{Package: api.IndexPackage{Repository: r, ID: p.ID, Name: p.Name}, ID: v.ID, Version: v.Version}
}

// A backend without bounded capabilities must fail explicitly rather than
// silently use an unbounded store method or return an empty successful result.
func TestUnitIndexReadsRequireBoundedCapabilities(t *testing.T) {
	db, v := indexReadFixture(t)
	for name, source := range map[string]IndexStore{
		"absent store":           nil,
		"unbounded repositories": indexReadStore{IndexStore: db, repositories: repositoryWithoutIndex{db.Repos()}, packages: db.Packages()},
		"unbounded packages":     indexReadStore{IndexStore: db, repositories: db.Repos(), packages: packagesWithoutIndex{db.Packages()}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, operation := range []string{"repositories", "packages", "versions", "current", "files"} {
				assertIndexReadFailure(t, NewReader(source, nil), v, operation, kernel.ErrStore)
			}
		})
	}
}

func assertIndexReadFailure(t *testing.T, reader api.PackageIndex, v api.IndexVersion, operation string, want error) {
	t.Helper()
	ctx, page := t.Context(), kernel.Page{Limit: 1}
	var err error
	var disclosed bool
	switch operation {
	case "repositories":
		var rows []api.IndexRepository
		var next string
		rows, next, err = reader.Repositories(
			ctx,
			api.IndexRepositoryQuery{Projects: []string{"alpha"}, Repositories: []string{"*"}, PackageTypes: []repo.PackageType{repo.NPM}},
			page,
		)
		disclosed = len(rows) != 0 || next != ""
	case "packages":
		var rows []api.IndexPackage
		var next string
		rows, next, err = reader.Packages(ctx, v.Package.Repository, page)
		disclosed = len(rows) != 0 || next != ""
	case "versions":
		var rows []api.IndexVersion
		var next string
		rows, next, err = reader.Versions(ctx, v.Package, 0, page)
		disclosed = len(rows) != 0 || next != ""
	case "current":
		var current api.IndexVersion
		current, err = reader.Version(ctx, v, 0)
		disclosed = current.ID != 0 || current.Protected
	case "files":
		var rows []api.IndexFile
		var next string
		rows, next, err = reader.Files(ctx, v, nil, page)
		disclosed = len(rows) != 0 || next != ""
	default:
		t.Fatal("unknown index fixture operation", operation)
	}
	if !errors.Is(err, want) || disclosed {
		t.Fatalf("%s: error=%v want=%v disclosed=%t", operation, err, want, disclosed)
	}
}

type repositoryIndexFailure struct {
	port.RepoStore
	port.RepositoryIndexPages
	fail string
}

func (r repositoryIndexFailure) IndexRepository(ctx context.Context, key string) (api.IndexRepository, error) {
	if r.fail == "repository" {
		return api.IndexRepository{}, errBoom
	}
	return r.RepositoryIndexPages.IndexRepository(ctx, key)
}

func (r repositoryIndexFailure) IndexRepositoryLayout(ctx context.Context, key string) (string, error) {
	if r.fail == "layout reference" {
		return "", errBoom
	}
	return r.RepositoryIndexPages.IndexRepositoryLayout(ctx, key)
}

func (r repositoryIndexFailure) IndexRepositories(ctx context.Context, q api.IndexRepositoryQuery, p kernel.Page) ([]api.IndexRepository, string, error) {
	if r.fail == "repositories" {
		return nil, "", errBoom
	}
	return r.RepositoryIndexPages.IndexRepositories(ctx, q, p)
}

type packageIndexFailure struct {
	port.PackageStore
	port.PackageIndexPages
	fail string
}

func (p packageIndexFailure) Get(ctx context.Context, id int64) (model.Package, error) {
	if p.fail == "package" {
		return model.Package{}, errBoom
	}
	return p.PackageStore.Get(ctx, id)
}

func (p packageIndexFailure) Version(ctx context.Context, id int64, version string) (model.PackageVersion, error) {
	if p.fail == "version" {
		return model.PackageVersion{}, errBoom
	}
	v, err := p.PackageStore.Version(ctx, id, version)
	if p.fail == "version identity" {
		v.ID++
	}
	return v, err
}

func (p packageIndexFailure) IndexPackages(ctx context.Context, ref api.IndexRepository, page kernel.Page) ([]api.IndexPackage, string, error) {
	if p.fail == "packages" {
		return nil, "", errBoom
	}
	return p.PackageIndexPages.IndexPackages(ctx, ref, page)
}

func (p packageIndexFailure) IndexVersions(ctx context.Context, ref api.IndexPackage, keep int, page kernel.Page) ([]api.IndexVersion, string, error) {
	if p.fail == "versions" {
		return nil, "", errBoom
	}
	return p.PackageIndexPages.IndexVersions(ctx, ref, keep, page)
}

func (p packageIndexFailure) IndexVersion(ctx context.Context, ref api.IndexVersion, keep int) (api.IndexVersion, error) {
	if p.fail == "current" {
		return api.IndexVersion{}, errBoom
	}
	return p.PackageIndexPages.IndexVersion(ctx, ref, keep)
}

func (p packageIndexFailure) IndexFiles(ctx context.Context, ref api.IndexVersion, properties map[string][]string, page kernel.Page) ([]api.IndexFile, string, error) {
	if p.fail == "files" {
		return nil, "", errBoom
	}
	return p.PackageIndexPages.IndexFiles(ctx, ref, properties, page)
}

func failingIndexReader(db *inmem.Store, failure string, formats format.Registry, layouts ...format.Layouts) api.PackageIndex {
	return NewReader(indexReadStore{
		IndexStore:   db,
		repositories: repositoryIndexFailure{db.Repos(), db.Repos().(port.RepositoryIndexPages), failure},
		packages:     packageIndexFailure{db.Packages(), db.Packages().(port.PackageIndexPages), failure},
	}, formats, layouts...)
}

func TestUnitIndexReadsDoNotDiscloseOnMetadataFailure(t *testing.T) {
	db, v := indexReadFixture(t)
	for _, tc := range []struct{ operation, failure string }{
		{"repositories", "repositories"},
		{"packages", "repository"},
		{"packages", "packages"},
		{"versions", "repository"},
		{"versions", "package"},
		{"versions", "versions"},
		{"current", "repository"},
		{"current", "package"},
		{"current", "current"},
		{"files", "repository"},
		{"files", "package"},
		{"files", "version"},
		{"files", "files"},
	} {
		t.Run(tc.operation+"/"+tc.failure, func(t *testing.T) {
			assertIndexReadFailure(t, failingIndexReader(db, tc.failure, nil), v, tc.operation, errBoom)
		})
	}
	assertIndexReadFailure(t, failingIndexReader(db, "version identity", nil), v, "files", kernel.ErrNotFound)
}

type failingIndexLead struct{ format.Handler }

func (failingIndexLead) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return nil, errBoom
}

type failingIndexLayoutLead struct{ format.Handler }

func (failingIndexLayoutLead) CleanupLeadWithLayout(*layout.Layout, model.PackageVersion) (format.CleanupLeadSelector, error) {
	return nil, errBoom
}

type failingIndexLayout struct{}

func (failingIndexLayout) Layout(context.Context, string) (*layout.Layout, error) {
	return nil, errBoom
}

func TestUnitIndexLeadFailuresNeverReleaseFileSnapshots(t *testing.T) {
	db, v := indexReadFixture(t)
	for _, handler := range []format.Handler{failingIndexLead{}, failingIndexLayoutLead{}} {
		registry := func(repo.PackageType) format.Handler { return handler }
		assertIndexReadFailure(t, NewReader(db, registry), v, "files", errBoom)
	}
	registry := func(repo.PackageType) format.Handler { return failingIndexLayoutLead{} }
	assertIndexReadFailure(t, failingIndexReader(db, "layout reference", registry), v, "files", errBoom)
	assertIndexReadFailure(t, NewReader(db, registry, failingIndexLayout{}), v, "files", errBoom)
}

func TestUnitIndexReadsRejectStaleIdentityAndInvalidRetention(t *testing.T) {
	db, v := indexReadFixture(t)
	reader := NewReader(db, nil)
	if _, _, err := reader.Versions(t.Context(), v.Package, -1, kernel.Page{}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("negative retention accepted", err)
	}
	if _, err := reader.Version(t.Context(), v, -1); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("negative current retention accepted", err)
	}
	forged := v
	forged.Package.Name = "foreign-package"
	assertIndexReadFailure(t, reader, forged, "versions", kernel.ErrNotFound)
	c, err := db.Repos().Get(t.Context(), v.Package.Repository.Key)
	if err != nil {
		t.Fatal(err)
	}
	c.Class = repo.Virtual
	if err := db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	assertIndexReadFailure(t, reader, v, "packages", kernel.ErrInvalid)
}
