package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
)

// TestUnitHosted: a federated repository holds its own files as a local
// one does, so each member derives their Maven metadata; a remote or
// virtual repository does not.
func TestUnitHosted(t *testing.T) {
	for class, want := range map[repo.Class]bool{
		repo.Local: true, repo.Federated: true, repo.Remote: false, repo.Virtual: false,
	} {
		if got := maven.Hosted(repo.Config{Class: class}); got != want {
			t.Errorf("Hosted(%s) = %v, want %v", class, got, want)
		}
	}
}
