package generic_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // Maven's .sha1 sidecar names the algorithm.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// noStoreFile is one remote path a family serves from an upstream that marks
// every answer Cache-Control: no-store. metadata says whether a zero
// retrieval cache period must send its second read upstream again; nil
// leaves that count to the family's own tests.
type noStoreFile struct {
	path     string
	metadata *bool
}

// An upstream Cache-Control: no-store does not keep an answer out of the
// remote cache. 6.20's remote fetch (HttpRepo) never reads it, and 7.x sends
// it on Docker manifests and Maven metadata for the proxies in front of it
// (RTFACT-27199, RTFACT-15016, RTFACT-17758) while a 7.x remote over a 7.x
// upstream still caches that metadata (RTFACT-30785). So the second read is
// served from the cache, and the cache answers once the upstream is gone;
// mutable metadata still follows the repository's own retrieval cache
// period.
func TestAccRemoteKeepsUpstreamNoStoreAnswers(t *testing.T) {
	for _, family := range []string{"generic", "maven", "npm", "pypi"} {
		for _, zero := range []bool{false, true} {
			name := family
			if zero {
				name += " zero period"
			}
			t.Run(name, func(t *testing.T) {
				w := newRemoteWorld(t)
				files := noStoreFamily(w, family)
				if zero {
					w.c.RetrievalCachePeriodSecs = new(0)
				}
				w.update()
				bodies := map[string][]byte{}
				for _, f := range files {
					first := w.request(http.MethodGet, f.path, "reader", "")
					remoteStatus(t, first, http.StatusOK)
					bodies[f.path] = first.Body.Bytes()
					calls := len(w.up.Requests())
					second := w.request(http.MethodGet, f.path, "reader", "")
					remoteStatus(t, second, http.StatusOK)
					refetched := len(w.up.Requests()) - calls
					switch {
					case !zero && refetched != 0:
						t.Fatalf("%s: second read went upstream %d times", f.path, refetched)
					case zero && f.metadata != nil && *f.metadata != (refetched > 0):
						t.Fatalf("%s: zero period refetched %d times", f.path, refetched)
					}
				}
				w.up.Close()
				for _, f := range files {
					gone := w.request(http.MethodGet, f.path, "reader", "")
					remoteStatus(t, gone, http.StatusOK)
					if !bytes.Equal(gone.Body.Bytes(), bodies[f.path]) {
						t.Fatalf("%s: kept answer changed: %q", f.path, gone.Body)
					}
				}
			})
		}
	}
}

// noStoreFamily points w's remote at family's layout over its fake upstream,
// serves each path marked no-store, and returns the remote paths to read.
func noStoreFamily(w *remoteWorld, family string) []noStoreFile {
	set := func(path string, body []byte, contentType string) {
		w.up.Set(path, fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": {contentType}, "Cache-Control": {"no-store"}},
		})
	}
	retype := func(t repo.PackageType) {
		w.c = *repo.Defaults(&repo.Config{Key: w.c.Key, Class: repo.Remote, PackageType: t, URL: w.up.URL})
	}
	switch family {
	case "maven":
		retype(repo.Maven)
		jar := []byte("PK maven jar body")
		sum := sha1.Sum(jar) //nolint:gosec // the sidecar's algorithm
		set("/org/acme/app/1.0/app-1.0.jar", jar, "application/java-archive")
		set("/org/acme/app/1.0/app-1.0.jar.sha1", []byte(hex.EncodeToString(sum[:])), "text/plain")
		set("/org/acme/app/maven-metadata.xml", []byte(`<?xml version="1.0" encoding="UTF-8"?>`+
			`<metadata><groupId>org.acme</groupId><artifactId>app</artifactId><versioning>`+
			`<latest>1.0</latest><release>1.0</release><versions><version>1.0</version></versions>`+
			`<lastUpdated>20260101000000</lastUpdated></versioning></metadata>`), "application/xml")
		return []noStoreFile{
			{"/artifactory/remote/org/acme/app/1.0/app-1.0.jar", new(false)},
			{"/artifactory/remote/org/acme/app/maven-metadata.xml", new(true)},
		}
	case "npm":
		retype(repo.NPM)
		tarball := noStoreTgz("package/package.json", `{"name":"demo","version":"1.0.0"}`)
		sha512Sum := sha512.Sum512(tarball)
		sha1Sum := sha1.Sum(tarball) //nolint:gosec // npm's shasum
		packument, _ := json.Marshal(map[string]any{
			"name":      "demo",
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"versions": map[string]any{"1.0.0": map[string]any{
				"name": "demo", "version": "1.0.0", "dist": map[string]any{
					"tarball":   w.up.URL + "/demo/-/demo-1.0.0.tgz",
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:]),
					"shasum":    hex.EncodeToString(sha1Sum[:]),
				},
			}},
		})
		set("/demo/-/demo-1.0.0.tgz", tarball, "application/octet-stream")
		set("/demo", packument, "application/json")
		return []noStoreFile{
			{"/artifactory/api/npm/remote/demo", new(true)},
			{"/artifactory/api/npm/remote/demo/-/demo-1.0.0.tgz", nil},
		}
	case "pypi":
		retype(repo.PyPI)
		w.c.PyPIRegistryURL = w.up.URL
		sdist := noStoreTgz(
			"demo-1.0/PKG-INFO",
			"Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n",
		)
		sum := sha256.Sum256(sdist)
		set("/packages/demo-1.0.tar.gz", sdist, "application/octet-stream")
		set(
			"/simple/demo/",
			[]byte(`<!DOCTYPE html><html><body><a href="../../packages/demo-1.0.tar.gz#sha256=`+
				hex.EncodeToString(sum[:])+`">demo-1.0.tar.gz</a></body></html>`),
			"text/html",
		)
		return []noStoreFile{
			{"/artifactory/api/pypi/remote/simple/demo/", new(true)},
			{"/artifactory/api/pypi/remote/demo/1.0/demo-1.0.tar.gz", nil},
		}
	default:
		set("/lib/a.bin", []byte("generic body"), "application/octet-stream")
		return []noStoreFile{{"/artifactory/remote/lib/a.bin", new(false)}}
	}
}

func noStoreTgz(name, content string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(
		&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg},
	)
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
