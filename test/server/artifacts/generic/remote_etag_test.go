package generic_test

// This file holds the remote validator suite of #1484: a remote file
// answers with its SHA-1 as its ETag, never the upstream's, and is matched
// against that alone, whether the upstream's answer or the cached copy
// serves it.

import (
	"crypto/sha1" //nolint:gosec // the file's SHA-1, as 7.x sends it
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccGenericRemoteFileETagIsItsSHA1: a remote file whose upstream
// sends its own ETag and its X-Checksum-Sha1 answers a GET and a HEAD with
// that SHA-1, bare, as its ETag, beside X-Checksum-Sha1, on the first
// download and again, on a remote that stores it and one that does not. The
// upstream's ETag then matches nothing, and the SHA-1 answers 304. 7.161.26's
// RequestResponseHelper.setBasicHeaders sets every downloaded resource's
// ETag to its SHA-1, a remote one's included; the stream sent the
// upstream's ETag and matched it.
func TestAccGenericRemoteFileETagIsItsSHA1(t *testing.T) {
	sum := sha1.Sum([]byte(coldJar)) //nolint:gosec // the file's SHA-1
	sha := hex.EncodeToString(sum[:])
	upstreamTag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(coldJar)))
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := newRemoteWorld(t)
			w.c.StoreArtifactsLocally = new(store)
			w.update()
			for i, method := range []string{http.MethodGet, http.MethodHead} {
				file := "/etag/file-" + strconv.Itoa(i) + ".jar"
				w.up.Set(file, fakeupstream.Resource{Body: []byte(coldJar), Header: http.Header{
					"Content-Type": {javaArchive}, "X-Checksum-Sha1": {sha},
				}})
				path := "/artifactory/remote" + file
				for _, pass := range []string{"first download", "again"} {
					rec := w.request(method, path, "reader", "")
					remoteStatus(t, rec, http.StatusOK)
					if got := rec.Header().Get("ETag"); got != sha ||
						rec.Header().Get("X-Checksum-Sha1") != sha {
						t.Fatalf("%s %s: ETag %q, X-Checksum-Sha1 %q; want %q for both", pass,
							method, got, rec.Header().Get("X-Checksum-Sha1"), sha)
					}
				}
				wantDownload(t, w.request(method, path, "reader", "", "If-None-Match", upstreamTag),
					method, javaArchive, coldJar)
				remoteStatus(t, w.request(method, path, "reader", "", "If-None-Match", sha),
					http.StatusNotModified)
			}
		})
	}
}

// TestAccMavenRemoteMetadataETagIsItsSHA1: a remote's maven-metadata.xml,
// which the cache keeps as a document without an item, answers a GET, a GET
// from the cache and a HEAD with its SHA-1, bare, as its ETag, and the
// upstream's ETag matches nothing. 7.161.26 serves a cached file with its
// SHA-1 as ETag (RequestResponseHelper.setBasicHeaders); the item route
// sent the upstream's (#1484).
func TestAccMavenRemoteMetadataETagIsItsSHA1(t *testing.T) {
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{
		Key: w.c.Key, Class: repo.Remote, PackageType: repo.Maven, URL: w.up.URL,
	})
	w.update()
	metadata := `<?xml version="1.0" encoding="UTF-8"?><metadata><groupId>org.acme</groupId>` +
		`<artifactId>app</artifactId><versioning><latest>1.0</latest><versions>` +
		`<version>1.0</version></versions></versioning></metadata>`
	upstream(w, "/org/acme/app/maven-metadata.xml", "text/xml", metadata)
	sum := sha1.Sum([]byte(metadata)) //nolint:gosec // the document's SHA-1
	sha := hex.EncodeToString(sum[:])
	path := "/artifactory/remote/org/acme/app/maven-metadata.xml"
	for _, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		rec := w.request(method, path, "reader", "")
		remoteStatus(t, rec, http.StatusOK)
		if got := rec.Header().
			Get("ETag"); got != sha ||
			rec.Header().Get("X-Checksum-Sha1") != sha {
			t.Fatalf("%s: ETag %q, X-Checksum-Sha1 %q; want %q for both", method, got,
				rec.Header().Get("X-Checksum-Sha1"), sha)
		}
	}
	upstreamTag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(metadata)))
	remoteStatus(t, w.request(http.MethodGet, path, "reader", "", "If-None-Match", upstreamTag),
		http.StatusOK)
	remoteStatus(t, w.request(http.MethodGet, path, "reader", "", "If-None-Match", sha),
		http.StatusNotModified)
}
