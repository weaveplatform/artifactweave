package cocoapods

import (
	"archive/zip"
	"bytes"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
)

func TestAccSourceZIPRejectsLocalTraversalWithoutCaching(t *testing.T) {
	f := newRouteFixture(t)
	f.seedRemote(t)
	f.up.Set("/"+podfmt.SpecPath("RemotePod", "2.0.0"), fakeupstream.Resource{Body: []byte(
		`{"name":"RemotePod","version":"2.0.0","source":{"http":"` + f.up.URL + `/RemotePod.zip"}}`,
	)})
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	entry, err := writer.Create("RemotePod.m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("void remote(void) {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	valid := output.Bytes()
	malformed := bytes.Clone(valid)
	copy(malformed[30:41], "../bad.m!!!")
	f.up.Set("/RemotePod.zip", fakeupstream.Resource{Body: malformed})
	path := "sources/RemotePod/2.0.0/source.zip"
	for range 2 {
		if response := f.get("pods-remote", path); response.Code == http.StatusOK {
			t.Fatal("ambiguous ZIP was served or cached")
		}
	}
	if got := f.up.Count(http.MethodGet, "/RemotePod.zip"); got != 2 {
		t.Fatalf("rejected ZIP entered the successful cache: %d requests", got)
	}
	f.up.Set("/RemotePod.zip", fakeupstream.Resource{Body: valid})
	if response := f.get("pods-remote", path); response.Code != http.StatusOK ||
		!bytes.Equal(response.Body.Bytes(), valid) {
		t.Fatalf("repaired ZIP did not publish: %d %s", response.Code, response.Body.String())
	}
}
