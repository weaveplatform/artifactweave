package build

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tagging is a generic format that records the properties it is told of.
type tagging struct {
	generic.Handler
	tagged []string
}

func (f *tagging) Annotated(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	it types.Item,
	_ format.Annotation,
) error {
	f.tagged = append(f.tagged, c.Key+":"+it.FullPath())
	return nil
}

// A promotion's properties reach the format of the files it tags.
func TestPromotionTagsThroughTheFormat(t *testing.T) {
	w := promotionWorld(t)
	f := &tagging{}
	w.svc.Formats = func(repo.PackageType) format.Handler { return f }
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	f.tagged = nil
	if _, err := w.svc.Promote(context.Background(), Promotion{
		Name: "app", Number: "1", Artifacts: true,
		Properties: types.Properties{"stage": {"qa"}}, FailFast: true, By: "admin",
	}, everyone); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.tagged, []string{"libs-local:app/a.bin"}) {
		t.Errorf("tagged %v", f.tagged)
	}
}
