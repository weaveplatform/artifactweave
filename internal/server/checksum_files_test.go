package server

import (
	"context"
	"net/http"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// TestServerWrittenChecksumFiles (#399): under client-checksums, the
// default, a file's checksum files serve what its client sent. Files the
// server writes itself have their computed checksums as the client's, and
// an npm tarball has what the publish's dist declared, so neither loses
// its .sha1.
func TestServerWrittenChecksumFiles(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	served := func(path, want string) {
		t.Helper()
		rec := w.do(http.MethodGet, path, "admin", "")
		switch {
		case want == "" && rec.Code != http.StatusNotFound:
			t.Errorf("%s: %d %q, want 404", path, rec.Code, rec.Body)
		case want != "" && (rec.Code != http.StatusOK || rec.Body.String() != want):
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Body, want)
		}
	}

	// A build-info document, which the build API writes.
	doc := artifactDoc("app", "1", testStarted, "artifact")
	w.publish(doc)
	file := "/artifactory/artifactory-build-info/" + documentPath("app", "1", testStarted)
	served(file+".sha1", sha1Hex(doc))
	served(file+".md5", md5Hex(doc))

	// A pushed image: the registry writes the finished upload, the manifest
	// and the layer it links into the tag's folder.
	for _, blob := range []string{registryConfig, "registry layer"} {
		wantStatus(t, w.do(http.MethodPost,
			"/v2/docker-local/app/blobs/uploads/?digest="+sha256Digest(blob), "admin", blob),
			http.StatusCreated)
	}
	manifest := registryManifest("checksums")
	wantStatus(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", "admin", manifest,
		"Content-Type", layout.MediaTypeOCIManifest), http.StatusCreated)
	image := "/artifactory/docker-local/app/"
	served(image+"v1/manifest.json.sha1", sha1Hex(manifest))
	served(image+"v1/manifest.json.md5", md5Hex(manifest))
	served(image+"v1/"+layout.BlobFile(registryLayer[7:])+".sha1", sha1Hex("registry layer"))
	served(image+"_uploads/sha256__"+sha256Digest(registryConfig)[7:]+".sha1",
		sha1Hex(registryConfig))

	// An npm tarball: dist.shasum and dist.integrity's SHA-512 are the
	// client's; npm sends no MD5.
	wantStatus(t, w.do(http.MethodPut, npmBase+"npm-local/pkg", "admin",
		npmPublishBody("pkg", "1.0.0"), "Content-Type", "application/json"), http.StatusCreated)
	tgz := string(npmTarballOf("pkg", "1.0.0"))
	tarball := "/artifactory/npm-local/" + npmfmt.CanonicalPath("pkg", "1.0.0")
	served(tarball+".sha1", sha1Hex(tgz))
	served(tarball+".sha512", sha512Hex(tgz))
	served(tarball+".md5", "")
}
