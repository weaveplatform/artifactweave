package helm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func chartWithEntry(t *testing.T, name string, kind byte, size int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	metadata := "apiVersion: v2\nname: web\nversion: 1.0.0\n"
	if err := tw.WriteHeader(
		&tar.Header{Name: "web/Chart.yaml", Mode: 0o600, Size: int64(len(metadata))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(metadata)); err != nil {
		t.Fatal(err)
	}
	header := &tar.Header{Name: name, Typeflag: kind, Mode: 0o600, Size: size}
	if kind == tar.TypeSymlink || kind == tar.TypeLink {
		header.Linkname = "../../outside"
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if _, err := tw.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAccRemoteChartEntryAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   byte
		size   int64
		status int
	}{
		{"web/templates/ok.yaml", tar.TypeReg, 4, 200},
		{"web/charts/", tar.TypeDir, 0, 200},
		{"web/../../evil.txt", tar.TypeReg, 4, 502},
		{"/etc/evil.txt", tar.TypeReg, 4, 502},
		{"C:\\evil.txt", tar.TypeReg, 4, 502},
		{"web/link", tar.TypeSymlink, 0, 502},
		{"web/hardlink", tar.TypeLink, 0, 502},
		{"web/device", tar.TypeChar, 0, 502},
		{"web/fifo", tar.TypeFifo, 0, 502},
		{"web/oversized", tar.TypeReg, 5<<20 + 1, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, store := range []bool{true, false} {
				f := newRemoteFixture(t)
				f.configure(func(c *repo.Config) { c.StoreArtifactsLocally = &store })
				entry, _ := f.chart("web", "1.0.0", "web-1.0.0.tgz")
				data := chartWithEntry(t, tc.name, tc.kind, tc.size)
				entry.Digest = fmt.Sprintf("%x", sha256.Sum256(data))
				f.up.Set("/charts/web-1.0.0.tgz", fakeupstream.Resource{Body: data})
				f.index(entry)
				got := f.get("web-1.0.0.tgz", "reader")
				if got.Code != tc.status {
					t.Fatalf("status %d, want%d", got.Code, tc.status)
				}
				if tc.status != 200 || !store {
					if _, err := f.items.Store.Items().
						Get(t.Context(), "remote", true, "web-1.0.0.tgz"); err == nil {
						t.Fatal("unadmitted chart published")
					}
				}
			}
		})
	}
}

// ServeContent on Linux/Windows may choose these host-specific .tgz labels.
// Set them explicitly so every development host exercises the same contract.
func TestAccRemoteChartCompressedTarMIME(t *testing.T) {
	for _, contentType := range []string{"application/x-compressed-tar", "application/x-compressed"} {
		t.Run(contentType, func(t *testing.T) {
			for _, persist := range []bool{true, false} {
				f := newRemoteFixture(t)
				f.configure(func(c *repo.Config) { c.StoreArtifactsLocally = &persist })
				entry, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
				f.up.Set(
					"/charts/web-1.0.0.tgz",
					fakeupstream.Resource{
						Body:   data,
						Header: http.Header{"Content-Type": {contentType}},
					},
				)
				f.index(entry)
				expectRemote(t, f.get("web-1.0.0.tgz", "reader"), 200)
			}
		})
	}
}

func TestAccRemoteOfflineProvenanceRechecksCurrentIndex(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, "conditional"} {
		t.Run(method, func(t *testing.T) {
			f := newRemoteFixture(t)
			entry, _ := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			f.index(entry)
			payload := fmt.Sprintf(
				"apiVersion: v2\nname: web\nversion: 1.0.0\n...\nfiles:\n  web-1.0.0.tgz: sha256:%s\n",
				entry.Digest,
			)
			sig := signHelmProvenance(t, payload)
			f.up.Set("/charts/web-1.0.0.tgz.prov", fakeupstream.Resource{Body: sig})
			expectRemote(t, f.get("web-1.0.0.tgz.prov", "reader"), 200)
			entry.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte("new chart revision")))
			f.index(entry)
			f.clock.Advance(6 * time.Second)
			expectRemote(t, f.get("index.yaml", "reader"), 200, entry.Digest)
			f.configure(func(c *repo.Config) { v := true; c.Offline = &v })
			f.up.Close()
			actualMethod := method
			if method == "conditional" {
				actualMethod = http.MethodGet
			}
			req := httptest.NewRequest(actualMethod, remoteEndpoint+"web-1.0.0.tgz.prov", nil)
			req.Header.Set("X-User", "reader")
			if method == "conditional" {
				req.Header.Set("If-None-Match", "*")
			}
			got := httptest.NewRecorder()
			f.h.ServeHTTP(got, req)
			expectRemote(t, got, 502)
			if strings.Contains(got.Body.String(), "PGP SIGNED") || got.Header().Get("ETag") != "" {
				t.Fatal("stale provenance was exposed")
			}
		})
	}
}

func TestAccRemoteExternalRedirectStaysWithinPatterns(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			f := newRemoteFixture(t)
			entry, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			external := fakeupstream.New(t, fakeupstream.Options{})
			finalPath := "/unapproved/web-1.0.0.tgz"
			if allowed {
				finalPath = "/allowed/final/web-1.0.0.tgz"
			}
			external.Set(
				"/allowed/web-1.0.0.tgz",
				fakeupstream.Resource{Redirect: "/allowed/middle.tgz"},
			)
			external.Set("/allowed/middle.tgz", fakeupstream.Resource{Redirect: finalPath})
			external.Set(finalPath, fakeupstream.Resource{Body: data})
			entry.URLs = []string{external.URL + "/allowed/web-1.0.0.tgz"}
			f.configure(func(c *repo.Config) {
				v := true
				c.ExternalDependenciesEnabled = &v
				c.ExternalDependenciesPatterns = []string{external.URL + "/allowed/**"}
				c.Username = "upstream"
				c.Password = "origin-secret"
			})
			f.allowPrivate(t, external.URL)
			f.index(entry)
			expectRemote(t, f.get("index.yaml", "reader"), 200)
			location := "_external/http/" + strings.TrimPrefix(entry.URLs[0], "http://")
			got := f.get(location, "reader", "Authorization", "Bearer caller-secret")
			if allowed {
				expectRemote(t, got, 200)
				if !bytes.Equal(got.Body.Bytes(), data) {
					t.Fatal("redirect changed chart")
				}
			} else {
				expectRemote(t, got, 502)
				if external.Count("", finalPath) != 0 {
					t.Fatal("unapproved redirect fetched")
				}
				if _, err := f.items.Store.Items().
					Get(t.Context(), "remote", true, location); err == nil {
					t.Fatal("unapproved redirect published")
				}
			}
			for _, request := range external.Requests() {
				if request.Header.Get("Authorization") != "" {
					t.Fatal("credential leaked on redirect")
				}
			}
		})
	}
}
