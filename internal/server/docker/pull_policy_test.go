package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A pull from a blacked-out repository answers NAME_UNKNOWN, as a push to
// it does; a manifest or blob its patterns reject answers as a missing one.
// Both carry Artifactory's message.
func TestAccPullPolicy(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("team/app", imageConfig, "layer")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/team/app/manifests/v1", image("layer"),
		ociManifest), http.StatusCreated, "")
	patch := func(f func(*repo.Config)) {
		t.Helper()
		ctx := context.Background()
		c, err := w.db.Repos().Get(ctx, "docker-local")
		if err != nil {
			t.Fatal(err)
		}
		f(&c)
		if err := w.db.Repos().Update(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "/v2/docker-local/team/app/manifests/v1"
	blob := "/v2/docker-local/team/app/blobs/" + digestOf("layer")
	accept := map[string]string{"Accept": ociManifest["Content-Type"]}
	refused := func(target string, code, msg string) {
		t.Helper()
		rec := w.do(http.MethodGet, target, "", accept)
		expect(t, rec, http.StatusNotFound, code)
		if !strings.Contains(rec.Body.String(), msg) {
			t.Fatalf("%s: %s, want %q", target, rec.Body, msg)
		}
		if head := w.do(http.MethodHead, target, "", accept); head.Code != http.StatusNotFound {
			t.Fatalf("HEAD %s: %d", target, head.Code)
		}
	}

	excluded, empty, on, off := "team/**", "", true, false
	patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	refused(manifest, CodeManifestUnknown, "due to its include/exclude pattern settings.")
	refused(blob, CodeBlobUnknown, "due to its include/exclude pattern settings.")

	patch(func(c *repo.Config) {
		c.ExcludesPattern = &empty
		c.BlackedOut = &on
	})
	const dark = "The repository 'docker-local' is blacked out and cannot serve artifact " +
		"'docker-local:team/app'."
	refused(manifest, CodeNameUnknown, dark)
	refused(blob, CodeNameUnknown, dark)

	patch(func(c *repo.Config) { c.BlackedOut = &off })
	expect(t, w.do(http.MethodGet, manifest, "", accept), http.StatusOK, "")
	expect(t, w.do(http.MethodGet, blob, "", nil), http.StatusOK, "")
}
