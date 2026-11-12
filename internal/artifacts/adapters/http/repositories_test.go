package artifactshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestAccHandlersCalledDirectly covers what the router always supplies: a
// principal, and the raw update body requestDetails saves.
func TestAccHandlersCalledDirectly(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "a", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	svc := newArtifactoryAPI(Config{Store: db})
	if _, err := svc.GetRepositoriesByTypeAndProject(
		ctx,
		artgen.GetRepositoriesByTypeAndProjectRequestObject{},
	); !errors.Is(err, kernel.ErrUnauthenticated) {
		t.Fatalf("no principal: %v", err)
	}
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
	// Without the saved body the decoded one is used, which cannot express
	// a null reset but still merges.
	if _, err := svc.UpdateRepositoryConfiguration(
		admin,
		artgen.UpdateRepositoryConfigurationRequestObject{
			RepoKey: "a", JSONBody: &repo.Config{Key: "a", Class: repo.Local, Notes: "direct"},
		},
	); err != nil {
		t.Fatal(err)
	}
	// The re-encoded body always carries notes, so the batch repeats it.
	if _, err := svc.UpdateMultipleRepositories(
		admin,
		artgen.UpdateMultipleRepositoriesRequestObject{
			JSONBody: &[]repo.Config{{Key: "a", Description: "batch", Notes: "direct"}},
		},
	); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.Repos().Get(ctx, "a"); c.Notes != "direct" || c.Description != "batch" {
		t.Fatalf("stored %+v", c)
	}
	if _, err := svc.CreateMultipleRepositories(admin,
		artgen.CreateMultipleRepositoriesRequestObject{}); err == nil {
		t.Fatal("a batch without a body was accepted")
	}
}

// racedStore leaves hide out of every repository listing, as a repository
// another write creates after a handler listed the repositories.
type racedStore struct {
	*inmem.Store
	hide string
}

func (s racedStore) Repos() port.RepoStore {
	return racedRepos{RepoStore: s.Store.Repos(), hide: s.hide}
}

type racedRepos struct {
	port.RepoStore
	hide string
}

func (r racedRepos) List(ctx context.Context, f port.RepoFilter) ([]repo.Config, error) {
	all, err := r.RepoStore.List(ctx, f)
	return slices.DeleteFunc(
		all,
		func(c repo.Config) bool { return c.Key == r.hide },
	), err //nolint:wrapcheck // test double
}

// A P2 remote repository planned for p2Urls whose key another write takes
// before the plan is stored answers as a create of a taken key does, 400
// singly and 409 in a batch, and stores nothing.
func TestUnitP2PlannedRemoteKeyTaken(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "race.example", Class: repo.Local, PackageType: repo.Generic},
		{Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	svc := newArtifactoryAPI(Config{Store: racedStore{Store: db, hide: "race.example"}})
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
	body := repo.Config{
		Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
		P2URLs: []string{"https://race.example/site"},
	}
	refused := func(name string, err error, status int) {
		t.Helper()
		var e *apierror.Error
		if !errors.As(err, &e) || e.Status != status || e.Message != msgKeyExists {
			t.Errorf("%s: %v, want %d %q", name, err, status, msgKeyExists)
		}
	}
	_, err := svc.UpdateRepositoryConfiguration(admin,
		artgen.UpdateRepositoryConfigurationRequestObject{RepoKey: "p2-virtual", JSONBody: &body})
	refused("update", err, http.StatusBadRequest)
	_, err = svc.UpdateMultipleRepositories(admin,
		artgen.UpdateMultipleRepositoriesRequestObject{JSONBody: &[]repo.Config{body}})
	refused("batch update", err, http.StatusConflict)
	created := body
	created.Key = "p2-new"
	_, err = svc.CreateRepository(admin,
		artgen.CreateRepositoryRequestObject{RepoKey: "p2-new", JSONBody: &created})
	refused("create", err, http.StatusBadRequest)
	if c, _ := db.Repos().Get(ctx, "p2-virtual"); len(c.Repositories) != 0 || len(c.P2URLs) != 0 {
		t.Errorf("a refused update was stored: %+v", c)
	}
	if _, err := db.Repos().Get(ctx, "p2-new"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a refused create was stored: %v", err)
	}
}
