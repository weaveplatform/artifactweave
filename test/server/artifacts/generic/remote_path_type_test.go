package generic_test

// This file holds the remote stream type suite of #1477: a remote file the
// item route serves from a stream, the upstream's or a document the cache
// keeps without an item, answers with the type of its path, as its kept
// copy does, never with the upstream's label.

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccGenericRemoteStreamTypedByPath: on a remote with
// storeArtifactsLocally false, a .jar its upstream labels application/zip
// answers application/java-archive on a GET and a HEAD, and a file whose
// name the table has no type for answers application/octet-stream, the
// type its kept copy answers on a remote that stores it. 7.161.26 answers
// a remote that stores nothing from the RemoteRepoResource, whose
// getMimeType is NamingUtils.getMimeType of the path, and
// RequestResponseHelper sends that type on both; the upstream's label
// serves only the MIME check.
func TestAccGenericRemoteStreamTypedByPath(t *testing.T) {
	w := newRemoteWorld(t)
	upstream(w, "/typed/file.jar", "application/zip", coldJar)
	upstream(w, "/typed/data.unknownext", "text/plain", "unknown bytes")

	kept := w.request(http.MethodGet, "/artifactory/remote/typed/data.unknownext", "reader", "")
	wantDownload(t, kept, http.MethodGet, octetStream, "unknown bytes")

	w.c.StoreArtifactsLocally = new(false)
	w.update()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wantDownload(t, w.request(method, "/artifactory/remote/typed/file.jar", "reader", ""),
			method, javaArchive, coldJar)
		wantDownload(t,
			w.request(method, "/artifactory/remote/typed/data.unknownext", "reader", ""),
			method, kept.Header().Get("Content-Type"), "unknown bytes")
	}
	if got := w.up.Count(http.MethodGet, "/typed/file.jar"); got != 1 {
		t.Fatalf("the HEAD was not answered from the upstream's HEAD: %v", w.up.Requests())
	}
}

// TestAccMavenRemoteMetadataTypedByPath: a remote's maven-metadata.xml,
// which the cache keeps as a document without an item, answers with the
// type of its path, application/xml, whatever its upstream labels it, on
// the first GET, a GET and a HEAD from the cache, as 7.x serves a cached
// file through FileResource.getMimeType, typed by its path when it carries
// no content-type property.
func TestAccMavenRemoteMetadataTypedByPath(t *testing.T) {
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{
		Key: w.c.Key, Class: repo.Remote, PackageType: repo.Maven, URL: w.up.URL,
	})
	w.update()
	metadata := `<?xml version="1.0" encoding="UTF-8"?><metadata><groupId>org.acme</groupId>` +
		`<artifactId>app</artifactId><versioning><latest>1.0</latest><versions>` +
		`<version>1.0</version></versions></versioning></metadata>`
	upstream(w, "/org/acme/app/maven-metadata.xml", "text/xml", metadata)
	path := "/artifactory/remote/org/acme/app/maven-metadata.xml"
	for _, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		wantDownload(t, w.request(method, path, "reader", ""), method, "application/xml", metadata)
	}
	if got := w.up.Count(http.MethodGet, "/org/acme/app/maven-metadata.xml"); got != 1 {
		t.Fatalf("the metadata was fetched %d times, want once", got)
	}
}
