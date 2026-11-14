package npm_test

import (
	"crypto/sha1" //nolint:gosec // npm's dist.shasum
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// A remote tarball the upstream labels text/html answers 409 with 7.x's
// message in npm's error envelope, as HttpRepo.blockMismatchingMimeTypes
// refuses it on every remote read (7.161.26), and the refusal is
// remembered: the next request asks the upstream nothing. The npm route
// answered 502 and asked again (#1392).
func TestAccNPMRemoteTarballMIMERefusal(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL})
	w.Update()
	data := support.NPMTarballOf("pkg", "1.0.0")
	sum := sha1.Sum(data) //nolint:gosec // npm's dist.shasum
	const file = "/pkg/-/pkg-1.0.0.tgz"
	w.Upstream.Set(
		file,
		fakeupstream.Resource{Body: data, Header: http.Header{"Content-Type": {"text/html"}}},
	)
	doc, _ := json.Marshal(map[string]any{
		"name": "pkg", "dist-tags": map[string]string{"latest": "1.0.0"},
		"versions": map[string]any{"1.0.0": map[string]any{
			"name":    "pkg",
			"version": "1.0.0",
			"dist": map[string]string{
				"tarball": w.Upstream.URL + file,
				"shasum":  hex.EncodeToString(sum[:]),
			},
		}},
	})
	w.Upstream.Set(
		"/pkg",
		fakeupstream.Resource{Body: doc, Header: http.Header{"Content-Type": {"application/json"}}},
	)
	want := `{"error":"Error fetching ` + w.Upstream.URL + file + `: Mismatching mime types found in request: ` +
		`[application/x-gzip] and response: text/html, and this repo blocks text/html on mismatch; ` +
		`Path: 'remote:pkg/-/pkg-1.0.0.tgz'"}` + "\n"
	for _, pass := range []string{"first", "again"} {
		rec := w.Request(
			http.MethodGet,
			"/artifactory/api/npm/remote/pkg/-/pkg-1.0.0.tgz",
			"reader",
			"",
		)
		if rec.Code != http.StatusConflict || rec.Body.String() != want {
			t.Errorf("%s: %d %q", pass, rec.Code, rec.Body)
		}
	}
	if got := w.Upstream.Count(http.MethodGet, file); got != 1 {
		t.Fatalf("the upstream was asked for the tarball %d times, want 1", got)
	}
}
