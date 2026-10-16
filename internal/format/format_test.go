package format

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestGenericKeepsNoIndex(t *testing.T) {
	h := For(repo.Generic)
	ctx := context.Background()
	if err := h.Deployed(ctx, nil, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(ctx, nil, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(
		ctx,
		nil,
		repo.Config{},
		[]types.Item{{Path: "a/b", Name: "c"}},
	); err != nil {
		t.Fatal(err)
	}
}

// Docker, OCI and Helm OCI repositories share the image format, which
// the manifest push goes through. A file outside any image touches no
// index, so a nil store is never reached.
func TestImagesHaveTheirFormat(t *testing.T) {
	ctx := context.Background()
	for _, pt := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		h := For(pt)
		if _, ok := h.(docker.Handler); !ok {
			t.Fatalf("%s: %T", pt, h)
		}
		if err := h.Deployed(ctx, nil, repo.Config{}, types.Item{}); err != nil {
			t.Fatal(err)
		}
		if err := h.Deleting(ctx, nil, repo.Config{}, types.Item{}); err != nil {
			t.Fatal(err)
		}
		if err := h.Deleted(ctx, nil, repo.Config{}, []types.Item{{Name: "x"}}); err != nil {
			t.Fatal(err)
		}
	}
}
