package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// A remote source archive the upstream labels text/html answers 409 with 7.x's
// message in the PyPI API's error envelope, as
// HttpRepo.blockMismatchingMimeTypes refuses it on every remote read
// (7.161.26), and the refusal is remembered: the next request asks the
// upstream nothing. The PyPI route answered 502 and asked again (#1392).
func TestAccPypiRemoteFileMIMERefusal(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
		PyPIRegistryURL: w.Upstream.URL,
	})
	w.Update()
	// A zip source archive, whose type, application/zip, the label does not
	// match; a wheel's, application/octet-stream, matches any label.
	data := zipBytes(
		t,
		map[string]string{"demo-1.0/PKG-INFO": "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n"},
	)
	sum := sha256.Sum256(data)
	const name = "demo-1.0.zip"
	w.Upstream.Set(
		"/files/"+name,
		fakeupstream.Resource{Body: data, Header: http.Header{"Content-Type": {"text/html"}}},
	)
	doc, _ := json.Marshal(map[string]any{
		"meta": map[string]string{"api-version": "1.1"}, "name": "demo",
		"files": []map[string]any{{
			"filename": name, "url": "/files/" + name,
			"hashes": map[string]string{"sha256": hex.EncodeToString(sum[:])},
		}},
	})
	w.Upstream.Set("/simple/demo/", fakeupstream.Resource{
		Body: doc, Header: http.Header{"Content-Type": {"application/vnd.pypi.simple.v1+json"}},
	})
	harness.WantStatus(t, w.Request(http.MethodGet, "/artifactory/api/pypi/remote/simple/demo/",
		"reader", "", "Accept", "application/vnd.pypi.simple.v1+json"), http.StatusOK)
	want := `{"errors":[{"message":"Error fetching ` + w.Upstream.URL + `/files/` + name +
		`: Mismatching mime types found in request: [application/zip] and response: text/html, ` +
		`and this repo blocks text/html on mismatch; Path: 'remote:demo/1.0/` + name + `'","status":409}]}` + "\n"
	for _, pass := range []string{"first", "again"} {
		rec := w.Request(
			http.MethodGet,
			"/artifactory/api/pypi/remote/demo/1.0/"+name,
			"reader",
			"",
		)
		if rec.Code != http.StatusConflict || rec.Body.String() != want {
			t.Errorf("%s: %d %q", pass, rec.Code, rec.Body)
		}
	}
	if got := w.Upstream.Count(http.MethodGet, "/files/"+name); got != 1 {
		t.Fatalf("the upstream was asked for the archive %d times, want 1", got)
	}
}
