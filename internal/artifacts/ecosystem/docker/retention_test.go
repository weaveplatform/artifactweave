package docker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// Manifests lists the manifests of one image, in its tag and digest
// folders alike, from the repository's own items: retention runs after a
// push, so a remote repository's cache gives none.
func TestUnitManifests(t *testing.T) {
	x := newImages(t)
	digest := layout.DigestFolder(sum("m2"))
	for _, c := range []repo.Config{x.local, x.remote} {
		x.write(c, "app/v1/sha256__"+sum("layer"), "layer")
		x.write(c, "app/v1/manifest.json", "m1")
		x.write(c, "app/"+digest+"/manifest.json", "m2")
		x.write(c, "app/web/v1/manifest.json", "m3")
	}
	for key, want := range map[string][]string{
		x.local.Key:  {"app/" + digest + "/manifest.json", "app/v1/manifest.json"},
		x.remote.Key: nil,
	} {
		ms, err := layout.Manifests(context.Background(), x.db.Items(), key, "app")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range ms {
			got = append(got, m.FullPath())
		}
		slices.Sort(got)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: manifests of app (-want +got):\n%s", key, diff)
		}
	}
}
