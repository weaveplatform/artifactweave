package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestAccRepositoriesWithoutStore(t *testing.T) {
	// A router built without a store answers like the stubs it replaces.
	h := testHandler()
	for _, path := range []string{
		"/artifactory/api/repositories", "/artifactory/api/repositories/x",
		"/artifactory/api/repositories/x/configuration", "/artifactory/api/v2/repositories/x",
		"/artifactory/api/v2/repositories/batch?names=x",
	} {
		if rec := do(t, h, "GET", path, nil, ""); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	ctx := kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	svc := artifactoryService{}
	if _, err := svc.content(); err == nil {
		t.Fatal("content without a store")
	}
	if _, err := svc.GetAllRepositoryConfigurations(ctx, artgenAllConfigs()); err == nil {
		t.Fatal("configurations without a store")
	}
}

func TestAccRequestDetailsReadFailure(t *testing.T) {
	h := requestDetails(
		newDetailOptions(),
	)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("reached the handler")
		}),
	)
	req := httptest.NewRequest(
		"POST",
		"/artifactory/api/repositories/x",
		io.NopCloser(failingReader{}),
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	for path, want := range map[string]bool{
		"/artifactory/api/repositories/x":          true,
		"/artifactory/api/v2/repositories/batch":   true,
		"/artifactory/api/repositories/":           false,
		"/artifactory/api/repositories/x/y":        false,
		"/artifactory/api/repositories/existence2": true,
	} {
		if got := isRepositoryUpdate(path); got != want {
			t.Errorf("%s: %v", path, got)
		}
	}
}

func artgenAllConfigs() artgen.GetAllRepositoryConfigurationsRequestObject {
	return artgen.GetAllRepositoryConfigurationsRequestObject{}
}

// TestAccHandlersCalledDirectly covers what the router always supplies: a
// principal, and the raw update body requestDetails saves.
func TestAccHandlersCalledDirectly(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "a", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: db}}
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
