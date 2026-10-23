package support

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// SHA256Digest is the registry digest of a fixture body.
func SHA256Digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

const RegistryConfig = `{"architecture":"amd64","os":"linux","config":{"Labels":{"team":"matrix"}}}`

var RegistryLayer = SHA256Digest("registry layer")

// DockerRoutes is the Docker and OCI server-assembly route inventory. The
// standalone Docker status and principal tables each cover every route.
func DockerRoutes() map[string]bool {
	const manifest = "/v2/{repo}/{name}/manifests/{reference}"
	const blob = "/v2/{repo}/{name}/blobs/{digest}"
	const uploads = "/v2/{repo}/{name}/blobs/uploads/"
	const upload = "/v2/{repo}/{name}/blobs/uploads/{session}"
	return map[string]bool{
		"GET /v2/": true,
		"GET /artifactory/api/docker/{repoKey}/v2/": true,
		"GET /v2/token": true,
		"GET /artifactory/api/docker/{repoKey}/v2/token": true,
		"GET /artifactory/api/docker/{repoKey}/v2/auth":  true,
		"GET /artifactory/api/docker/{repoKey}/v1/auth":  true,
		"HEAD " + blob:       true,
		"GET " + blob:        true,
		"DELETE " + blob:     true,
		"POST " + uploads:    true,
		"PATCH " + upload:    true,
		"PUT " + upload:      true,
		"GET " + upload:      true,
		"DELETE " + upload:   true,
		"HEAD " + manifest:   true,
		"GET " + manifest:    true,
		"PUT " + manifest:    true,
		"DELETE " + manifest: true,
		"GET /v2/{repo}/{name}/referrers/{digest}":                        true,
		"GET /v2/{repo}/{name}/tags/list":                                 true,
		"GET /v2/_catalog":                                                true,
		"GET /v2/{repo}/_catalog":                                         true,
		"GET /artifactory/api/docker/{repo-key}/v2/_catalog":              true,
		"GET /artifactory/api/docker/{repo-key}/v2/{imageName}/tags/list": true,
		"DELETE /artifactory/api/docker/{repoKey}/v2/delete":              true,
		"POST /artifactory/api/docker/{repo-key}/v2/promote":              true,
		"POST /artifactory/api/docker/{repo-key}/v2/parentManifests":      true,
	}
}

// RegistryManifest is the OCI image manifest used by the shared Docker and
// Helm OCI server-assembly fixtures. note varies its digest.
func RegistryManifest(note string) string {
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` +
		SHA256Digest(RegistryConfig) + `","size":` + strconv.Itoa(len(RegistryConfig)) + `},` +
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` +
		RegistryLayer + `","size":14}],"annotations":{"note":"` + note + `"}}`
}

// PushImage stores RegistryConfig in the image and pushes an OCI manifest to
// the tag; RegistryLayer must already be present in the image.
func PushImage(
	t *testing.T,
	db store.Store,
	blobs blob.Store,
	c repo.Config,
	name, tag, body string,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{
			Repo: c,
			Path: name + "/_uploads/sha256__" + SHA256Digest(RegistryConfig)[7:],
		},
		Body: strings.NewReader(RegistryConfig),
	}); err != nil {
		t.Fatal(err)
	}
	m, err := layout.ParseManifest(
		layout.MediaTypeOCIManifest,
		[]byte(body),
		SHA256Digest(body)[7:],
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&registry.Service{Store: db, Blobs: blobs}).PutManifest(ctx, registry.Push{
		Owner:    registry.Owner{Repo: c, Name: name, By: "admin"},
		Manifest: m, Body: []byte(body), Folders: []string{tag},
	}); err != nil {
		t.Fatal(err)
	}
}
