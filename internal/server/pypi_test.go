package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Follow pip's negotiation and link-resolution sequence through the full
// server, including the ordinary download path's Range and ETag behavior.
func TestPypiInstallSequence(t *testing.T) {
	for _, mode := range []string{"html", "json"} {
		t.Run(mode, func(t *testing.T) {
			w := newItemWorld(t, false, func(c *Config) {
				c.FormatSettings.PyPI.SimpleJSON = mode == "json"
				c.FormatSettings.PyPI.CoreMetadata = true
			})
			if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&repo.Config{
				Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI,
			})); err != nil {
				t.Fatal(err)
			}
			wheel := pypiWheel(t, "status-pkg", "1.0")
			wantStatus(t, w.do(http.MethodPut, "/artifactory/pypi-local/"+pypiWheelPath,
				"admin", string(wheel)), http.StatusCreated)
			const page = "/artifactory/api/pypi/pypi-local/simple/status-pkg/"
			rec := w.do(
				http.MethodGet,
				page,
				"admin",
				"",
				"Accept",
				"application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.1, text/html;q=0.01",
			)
			wantStatus(t, rec, http.StatusOK)
			var link, digest string
			if mode == "json" {
				var doc struct {
					Files []struct {
						URL    string
						Hashes map[string]string
					}
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc.Files) != 1 {
					t.Fatalf("files: %s", rec.Body)
				}
				link, digest = doc.Files[0].URL, doc.Files[0].Hashes["sha256"]
			} else {
				match := regexp.MustCompile(`href="([^"]+)#sha256=([a-f0-9]+)"`).
					FindStringSubmatch(rec.Body.String())
				if len(match) != 3 {
					t.Fatalf("file link missing: %s", rec.Body)
				}
				link, digest = match[1], match[2]
			}
			base, err := url.Parse("https://reg.example" + page)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := url.Parse(link)
			if err != nil {
				t.Fatal(err)
			}
			path := base.ResolveReference(ref).RequestURI()
			metadata := w.do(http.MethodGet, path+".metadata", "admin", "")
			wantStatus(t, metadata, http.StatusOK)
			if !bytes.Contains(metadata.Body.Bytes(), []byte("Name: status-pkg")) {
				t.Fatalf("metadata: %s", metadata.Body)
			}
			got := w.do(http.MethodGet, path, "admin", "")
			wantStatus(t, got, http.StatusOK)
			if !bytes.Equal(got.Body.Bytes(), wheel) || sha256Hex(got.Body.String()) != digest {
				t.Fatal("download does not match index checksum")
			}
			partial := w.do(http.MethodGet, path, "admin", "", "Range", "bytes=0-3")
			wantStatus(t, partial, http.StatusPartialContent)
			if !bytes.Equal(partial.Body.Bytes(), wheel[:4]) {
				t.Fatal("range differs from wheel")
			}
			wantStatus(
				t,
				w.do(http.MethodGet, path, "admin", "", "If-None-Match", got.Header().Get("ETag")),
				http.StatusNotModified,
			)
			for _, target := range []string{page, path, path + ".metadata"} {
				head := w.do(http.MethodHead, target, "admin", "")
				wantStatus(t, head, http.StatusOK)
				if head.Body.Len() != 0 {
					t.Errorf("HEAD %s returned a body", target)
				}
			}
		})
	}
}
