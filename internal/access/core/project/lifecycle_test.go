package project_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type lifecycleRepositories struct {
	artifactsapi.Repositories
	db         *inmem.Store
	failAfter  bool
	failUpdate string
}

func (r lifecycleRepositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	return r.db.Repos().List(ctx, support.RepoFilter(f))
}

func (r lifecycleRepositories) Update(ctx context.Context, c repo.Config) error {
	if err := r.db.Repos().Update(ctx, c); err != nil {
		return err
	}
	if c.Key == r.failUpdate {
		return errStoreFailure
	}
	return nil
}

func (r lifecycleRepositories) Create(ctx context.Context, c repo.Config) error {
	if err := r.db.Repos().Create(ctx, c); err != nil {
		return err
	}
	if r.failAfter {
		return errStoreFailure
	}
	return nil
}

func lifecycleWorld() (project.Service, *inmem.Store) {
	db := inmem.New()
	return project.Service{Store: db, Repositories: lifecycleRepositories{db: db}}, db
}

func TestUnitProjectLifecycleCreatesBuildInfoAtomically(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	p := model.Project{Key: "alpha", DisplayName: "Alpha", StorageQuotaBytes: -1}
	must(t, s.CreateProject(ctx, p))
	got, err := s.Project(ctx, p.Key)
	must(t, err)
	if got != p {
		t.Fatalf("project settings: got %+v, want %+v", got, p)
	}
	c, err := db.Repos().Get(ctx, "alpha-build-info")
	must(t, err)
	if c.ProjectKey != "alpha" || c.PackageType != repo.BuildInfo || c.Class != repo.Local ||
		len(c.Environments) != 1 || c.Environments[0] != "DEV" {
		t.Fatalf("project-owned build-info configuration: %+v", c)
	}
	if err := s.CreateProject(ctx, p); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("duplicate project: %v", err)
	}
	all, err := s.Projects(ctx)
	must(t, err)
	if len(all) != 1 {
		t.Fatalf("project count after conflict: %d", len(all))
	}

	s.Repositories = lifecycleRepositories{db: db, failAfter: true}
	if err := s.CreateProject(ctx, model.Project{Key: "beta", DisplayName: "Beta"}); !errors.Is(err, errStoreFailure) {
		t.Fatalf("failure after repository allocation: %v", err)
	}
	if _, err := s.Project(ctx, "beta"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("project survived failed creation: %v", err)
	}
	if _, err := db.Repos().Get(ctx, "beta-build-info"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("build-info survived failed creation: %v", err)
	}
}

func TestUnitProjectLifecyclePreservesPreexistingRepository(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	old := repo.Config{Key: "alpha-build-info", Class: repo.Local, PackageType: repo.Generic, Description: "existing content"}
	must(t, db.Repos().Create(ctx, old))
	err := s.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"})
	if !errors.Is(err, project.ErrConflict) {
		t.Fatalf("occupied build-info key: %v", err)
	}
	if _, err := s.Project(ctx, "alpha"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("project survived repository conflict: %v", err)
	}
	got, err := db.Repos().Get(ctx, old.Key)
	must(t, err)
	if got.Description != old.Description || got.PackageType != old.PackageType {
		t.Fatalf("existing repository replaced: %+v", got)
	}
}

func TestUnitProjectLifecycleValidatesSettingsAndUpdates(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	for _, p := range []model.Project{
		{Key: "x", DisplayName: "short key"},
		{Key: "Alpha", DisplayName: "uppercase key"},
		{Key: "alpha", DisplayName: " "},
		{Key: "alpha", DisplayName: "Alpha", StorageQuotaBytes: -2},
	} {
		if err := s.CreateProject(ctx, p); !errors.Is(err, project.ErrInvalid) {
			t.Errorf("invalid project %+v: %v", p, err)
		}
	}
	must(t, s.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
	if err := s.CreateProject(ctx, model.Project{Key: "beta", DisplayName: "Alpha"}); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("duplicate display name: %v", err)
	}
	must(t, s.CreateProject(ctx, model.Project{Key: "beta", DisplayName: "Beta"}))
	before, err := db.Projects().Authority(ctx, "alpha")
	must(t, err)
	updated := model.Project{DisplayName: "Renamed", SoftLimit: true, StorageQuotaBytes: 123}
	must(t, s.UpdateProject(ctx, "alpha", updated))
	got, err := s.Project(ctx, "alpha")
	must(t, err)
	if got.Key != "alpha" || got.DisplayName != "Renamed" || !got.SoftLimit || got.StorageQuotaBytes != 123 {
		t.Fatalf("updated settings: %+v", got)
	}
	after, err := db.Projects().Authority(ctx, "alpha")
	must(t, err)
	if before.Incarnation != after.Incarnation || before.Revision == after.Revision {
		t.Fatalf("settings update authority: before %+v, after %+v", before, after)
	}
	if err := s.UpdateProject(ctx, "alpha", model.Project{Key: "other", DisplayName: "Other"}); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("immutable key: %v", err)
	}
	if err := s.UpdateProject(ctx, "alpha", model.Project{DisplayName: "Beta"}); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("duplicate name on update: %v", err)
	}
	if err := s.UpdateProject(ctx, "missing", model.Project{DisplayName: "Missing"}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("update missing project: %v", err)
	}
}
