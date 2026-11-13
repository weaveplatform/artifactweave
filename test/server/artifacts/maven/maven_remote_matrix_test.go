package maven_test

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

// TestAccMavenRemoteChecksumFilesTakeNoMatrixParameters (#1371): a remote
// Maven repository asks its upstream for an artifact with the request's
// matrix parameters after the path (HttpRepo.downloadResource and
// assembleRetrieveInfoUrl, 7.161.26), and for the artifact's checksum files
// at their own paths without them, as 7.x's
// RemoteRepoBase.getRemoteChecksumFromPath reads them with
// downloadResource(path) and a null request context. Appending the
// parameters to every upstream request would have asked for
// "app-1.0.jar;x=1.sha1", a file no upstream holds.
func TestAccMavenRemoteChecksumFilesTakeNoMatrixParameters(t *testing.T) {
	w := newMavenRemoteWorld(t)
	const path = "org/acme/app/1.0/app-1.0.jar"
	jar := sourceJAR(t, "matrix")
	mavenSource(w, path, jar, "application/java-archive")
	w.up.Set("/"+path+";x=1", fakeupstream.Resource{
		Body: jar, Header: http.Header{"Content-Type": {"application/java-archive"}},
	})
	res := w.request(http.MethodGet, "/artifactory/remote/"+path+";x=1", "reader", "")
	remoteStatus(t, res, http.StatusOK)
	if !bytes.Equal(res.Body.Bytes(), jar) {
		t.Fatal("the artifact's bytes changed")
	}
	var artifact, checksums []string
	for _, r := range w.up.Requests() {
		if strings.HasPrefix(r.Path, "/"+path+".") {
			checksums = append(checksums, r.Path)
		} else {
			artifact = append(artifact, r.Method+" "+r.Path)
		}
	}
	// The HEAD that asks for the artifact's info carries them too, as
	// assembleRetrieveInfoUrl appends them.
	want := []string{"HEAD /" + path + ";x=1", "GET /" + path + ";x=1"}
	if !slices.Equal(artifact, want) {
		t.Errorf("the artifact was asked for with %q, want %q", artifact, want)
	}
	if len(checksums) == 0 || checksums[0] != "/"+path+".sha1" {
		t.Errorf("the checksum files were asked for at %q, want %q first", checksums,
			"/"+path+".sha1")
	}
}
