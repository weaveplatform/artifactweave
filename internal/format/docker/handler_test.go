package docker_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A tag's manifest is metadata, since a push moves the tag; digest
// manifests, layers and files outside images are unique content.
func TestClassify(t *testing.T) {
	h := layout.Handler{}
	c := repo.Config{Key: "docker-local", PackageType: repo.Docker}
	for p, want := range map[string]format.Class{
		"app/v1/manifest.json":                format.ClassMetadata,
		"team/app/v1/list.manifest.json":      format.ClassMetadata,
		"app/sha256__abc/manifest.json":       format.ClassUnique,
		"app/v1/sha256__abc":                  format.ClassUnique,
		"manifest.json":                       format.ClassUnique,
		"app/_uploads/sha256__abc":            format.ClassUnique,
		"repository.catalog/manifest.json.md": format.ClassUnique,
	} {
		if got := format.ClassOf(h, c, p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

// Images keep the deploy policy of every package type.
func TestAdmitIsGeneric(t *testing.T) {
	blacked := true
	c := repo.Config{Key: "docker-local", PackageType: repo.Docker, BlackedOut: &blacked}
	_, err := layout.Handler{}.Admit(context.Background(), format.Env{}, c,
		format.Write{Op: format.OpDeploy, Path: "app/v1/manifest.json"})
	var refused *format.Rejection
	if !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Fatalf("admit %v", err)
	}
}
