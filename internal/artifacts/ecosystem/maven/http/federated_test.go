package mavenhttp

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// TestAccMavenOperationsServeFederated: a federated repository holds its
// own files as a local one does (7.x's FederatedRepoDescriptor extends
// LocalRepoDescriptor), so calculateMetadata serves it rather than
// answering "Unable to find local repository".
func TestAccMavenOperationsServeFederated(t *testing.T) {
	w := newItemWorld(t)
	c := repo.Config{Key: "maven-fed", Class: repo.Federated, PackageType: repo.Maven}
	if err := w.world.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.world.Store.Items().Put(t.Context(), model.Item{
		RepoKey: "maven-fed", Name: "com/acme", Folder: true, CreatedBy: kittest.Admin.Name,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(http.MethodPost, calc+"maven-fed/com/acme", admin); rec.Code != http.StatusOK {
		t.Fatalf("calculateMetadata of a federated repository: %d %s", rec.Code, rec.Body)
	}
}
