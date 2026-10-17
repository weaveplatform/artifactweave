package registry_test

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// annotatedImages is the image format, recording the property changes it
// is told of.
type annotatedImages struct {
	layout.Handler
	tagged []string
}

func (f *annotatedImages) Annotated(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	it types.Item,
	_ format.Annotation,
) error {
	f.tagged = append(f.tagged, c.Key+":"+it.FullPath())
	return nil
}

// The properties a promotion sets on the landed manifest reach its format.
func TestPromotionAnnotatesThroughTheFormat(t *testing.T) {
	w := newWorld(t, inmem.New())
	f := &annotatedImages{}
	w.svc.Formats = func(repo.PackageType) format.Handler { return f }
	prod := w.prod()
	w.upload(imageConfig, layerA)
	body, digest := manifestOf(layerA)
	if err := w.push(body, digest, "v1"); err != nil {
		t.Fatal(err)
	}
	f.tagged = nil
	if err := w.svc.Promote(context.Background(), registry.Promotion{
		From: w.owner("app"), To: registry.Owner{Repo: prod, Name: "app", By: "bob"},
		Tags: []registry.Rename{{From: "v1", To: "v1"}}, Copy: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.tagged, "docker-prod:app/v1/manifest.json") {
		t.Errorf("tagged %v", f.tagged)
	}
}
