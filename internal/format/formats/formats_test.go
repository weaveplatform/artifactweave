package formats

import (
	"context"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestGenericKeepsNoIndex(t *testing.T) {
	h := For(repo.Generic)
	ctx := context.Background()
	if err := h.Deployed(ctx, format.Env{}, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(ctx, format.Env{}, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(
		ctx,
		format.Env{},
		repo.Config{},
		[]types.Item{{Path: "a/b", Name: "c"}},
	); err != nil {
		t.Fatal(err)
	}
}

// Docker, OCI and Helm OCI repositories share the image format, which
// the manifest push goes through. A file outside any image touches no
// index, so the empty environment's store is never reached.
func TestImagesHaveTheirFormat(t *testing.T) {
	ctx := context.Background()
	for _, pt := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		h := For(pt)
		if _, ok := h.(docker.Handler); !ok {
			t.Fatalf("%s: %T", pt, h)
		}
		if err := h.Deployed(ctx, format.Env{}, repo.Config{}, types.Item{}); err != nil {
			t.Fatal(err)
		}
		if err := h.Deleting(ctx, format.Env{}, repo.Config{}, types.Item{}); err != nil {
			t.Fatal(err)
		}
		if err := h.Deleted(
			ctx,
			format.Env{},
			repo.Config{},
			[]types.Item{{Name: "x"}},
		); err != nil {
			t.Fatal(err)
		}
	}
}

// Settings reach the format that takes them, and For is the zero
// Settings: each format with its default, which is 7.x's.
func TestSettings(t *testing.T) {
	s := Settings{NPM: npm.Settings{TagLatestByPublish: true}}
	if h, ok := s.For(repo.NPM).(npm.Handler); !ok || !h.Settings.TagLatestByPublish {
		t.Fatalf("npm with settings: %#v", s.For(repo.NPM))
	}
	if h, ok := For(repo.NPM).(npm.Handler); !ok || h.Settings != (npm.Settings{}) {
		t.Fatalf("npm by default: %#v", For(repo.NPM))
	}
	for _, pt := range []repo.PackageType{repo.Maven, repo.Go, repo.Docker, repo.Generic} {
		if !reflect.DeepEqual(s.For(pt), For(pt)) {
			t.Errorf("%s: %#v and %#v", pt, s.For(pt), For(pt))
		}
	}
}
