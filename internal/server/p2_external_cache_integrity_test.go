package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Cached plugin archives can outlive an artifacts.xml refresh. The current
// metadata checksum must therefore be checked against delivered plugin bytes,
// even when the source cache returns a stored Item without running Validate.
func TestAccP2CachedArchiveRejectsChangedPublishedPluginChecksum(t *testing.T) {
	pluginPath := "plugins/org.eclipse.glsp.layout.source_2.7.0.jar"
	oldPlugin := p2Fixture(t, "org.eclipse.glsp.layout.source_2.7.0.jar")
	newPlugin := p2Fixture(t, "artifacts.jar")
	root := []byte(strings.Replace(p2ExternalArtifactsComposite,
		"https://child.example/site", "https://child.example/site.zip!/", 1))
	var pluginArchiveSHA, metadataArchiveSHA string
	var pluginDeliveries int
	w := newItemWorld(t, false, func(cfg *Config) {
		backend := sourceBackend(
			func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
				if permit.Intent != source.CacheOnly || permit.UpstreamNetworkAllowed {
					t.Fatalf("cached P2 child fetched upstream: %+v", permit)
				}
				if q.Resource.Path == "compositeArtifacts.xml" {
					return source.Result{
						Metadata: source.MetadataResponse{
							BaseURL: "https://root.example/site/compositeArtifacts.xml",
						},
						Stream: &source.Stream{
							Body: io.NopCloser(bytes.NewReader(root)), Status: http.StatusOK,
							Representation: source.Representation{Size: int64(len(root))},
						},
					}, nil
				}
				var sha string
				switch {
				case strings.HasSuffix(q.Resource.Path, "/artifacts.xml"):
					sha = metadataArchiveSHA
				case strings.HasSuffix(q.Resource.Path, "/"+pluginPath):
					sha = pluginArchiveSHA
				default:
					return source.Result{}, store.ErrNotFound
				}
				return source.Result{
					Item: &types.Item{
						RepoKey: q.Repo.Key, Path: path.Dir(q.Resource.Path),
						Name: path.Base(q.Resource.Path), Cached: true,
						Actual: types.Checksums{SHA256: sha},
					},
					Used: func(context.Context) error {
						if strings.HasSuffix(q.Resource.Path, "/"+pluginPath) {
							pluginDeliveries++
						}
						return nil
					},
				}, nil
			},
		)
		cfg.RemoteSources = &source.Service{Resolve: sourceIdentity, Cache: backend}
	})
	oldArchive := p2ArchiveWithArtifact(t, pluginPath, oldPlugin)
	newArchive := p2ArchiveWithArtifact(t, pluginPath, newPlugin)
	pluginArchiveSHA = blobtest.Put(t.Context(), t, w.blobs, oldArchive).Digests.SHA256
	metadataArchiveSHA = pluginArchiveSHA
	newMetadataArchiveSHA := blobtest.Put(t.Context(), t, w.blobs, newArchive).Digests.SHA256
	c := *repo.Defaults(&repo.Config{
		Key: "p2-cache-integrity", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet,
		"/artifactory/p2-cache-integrity-cache/compositeArtifacts.xml", "admin", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("cached parent composite leaked origin: %d %s", res.Code, res.Body)
	}
	child := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	if child == "" {
		t.Fatalf("cached parent composite lost child path: %s", res.Body)
	}
	pluginURL := "/artifactory/p2-cache-integrity-cache/" + child + "/" + pluginPath
	res = w.do(http.MethodGet, pluginURL, "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), oldPlugin) ||
		pluginDeliveries != 1 {
		t.Fatalf("matching cached archive denied: %d, deliveries %d", res.Code, pluginDeliveries)
	}
	metadataArchiveSHA = newMetadataArchiveSHA
	res = w.do(http.MethodGet, pluginURL, "admin", "")
	if res.Code == http.StatusOK || pluginDeliveries != 1 ||
		strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("stale cached archive passed changed published checksum: %d, deliveries %d, %s",
			res.Code, pluginDeliveries, res.Body)
	}
}

func p2ArchiveWithArtifact(t *testing.T, pluginPath string, plugin []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(plugin)
	metadata := []byte(
		fmt.Sprintf(
			`<repository type="org.eclipse.equinox.p2.artifact.repository.simpleRepository">`+
				`<artifacts size="1"><artifact classifier="osgi.bundle" id="org.eclipse.glsp.layout.source" version="2.7.0">`+
				`<properties size="1"><property name="download.checksum.sha-256" value="%s"/>`+
				`</properties></artifact></artifacts></repository>`,
			hex.EncodeToString(sum[:]),
		),
	)
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for name, body := range map[string][]byte{"artifacts.xml": metadata, pluginPath: plugin} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
