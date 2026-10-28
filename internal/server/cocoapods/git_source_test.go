package cocoapods

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
)

func seedGitSource(t *testing.T, up *fakeupstream.Server, submodules bool) map[string]string {
	t.Helper()
	dir := "../../../test/fixtures/cocoapods-git/"
	data, err := os.ReadFile(dir + "refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]string
	if err = json.Unmarshal(data, &refs); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ name, path, hash string }{{"root", "main", refs["commit"]}, {"submodule", "dep", refs["submodule"]}} {
		packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
		advertised := packet(
			"# service=git-upload-pack\n",
		) + "0000" + packet(
			fixture.hash+" HEAD\x00ofs-delta\n",
		) + packet(
			fixture.hash+" refs/heads/main\n",
		)
		if fixture.name == "root" {
			advertised += packet(refs["tag"] + " refs/tags/1.0.0\n")
		}
		advertised += "0000"
		pack, err := os.ReadFile(dir + fixture.name + ".pack")
		if err != nil {
			t.Fatal(err)
		}
		up.Set(
			"/"+fixture.path+".git/info/refs",
			fakeupstream.Resource{
				Body: []byte(advertised),
				Header: http.Header{
					"Content-Type": {"application/x-git-upload-pack-advertisement"},
				},
			},
		)
		up.Set(
			"/"+fixture.path+".git/git-upload-pack",
			fakeupstream.Resource{
				Method: http.MethodPost,
				Status: 200,
				Body:   append([]byte("0008NAK\n"), pack...),
				Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
			},
		)
	}
	spec, err := json.Marshal(
		map[string]any{
			"name":    "HelloPod",
			"version": "1.0.0",
			"source": map[string]any{
				"git":        up.URL + "/main.git",
				"tag":        "1.0.0",
				"submodules": submodules,
			},
			"source_files": "Sources/*.m",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	up.Set("/"+podfmt.SpecPath("HelloPod", "1.0.0"), fakeupstream.Resource{Body: spec})
	up.Set(
		"/"+podfmt.ShardFile("HelloPod"),
		fakeupstream.Resource{Body: []byte("HelloPod/1.0.0\n")},
	)
	return refs
}

func TestAccGitSourceColdVirtualAndOffline(t *testing.T) {
	for _, withModules := range []bool{false, true} {
		t.Run(fmt.Sprintf("submodules=%t", withModules), func(t *testing.T) {
			f := newRouteFixture(t)
			seedGitSource(t, f.up, withModules)
			spec := f.get("pods-virtual", podfmt.SpecPath("HelloPod", "1.0.0"))
			if spec.Code != 200 || strings.Contains(spec.Body.String(), f.up.URL) ||
				!strings.Contains(spec.Body.String(), "source.tar.gz") {
				t.Fatalf("rewrite = %d %s", spec.Code, spec.Body.String())
			}
			archive := f.get("pods-virtual", "sources/HelloPod/1.0.0/source.tar.gz")
			if archive.Code != 200 {
				t.Fatalf("archive = %d %s", archive.Code, archive.Body.String())
			}
			gz, err := gzip.NewReader(bytes.NewReader(archive.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			tr := tar.NewReader(gz)
			files := map[string]string{}
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				files[h.Name] = string(body)
			}
			if files["Sources/HelloPod.m"] != "void hello(void) {}\n" {
				t.Fatalf("files=%v", files)
			}
			if (files["Vendor/Dep.m"] != "") != withModules {
				t.Fatalf("submodule files=%v", files)
			}
			if f.up.Count("POST", "/main.git/git-upload-pack") != 1 {
				t.Fatal("missing root Git fetch")
			}
			cfg, err := f.db.Repos().Get(t.Context(), "pods-remote")
			if err != nil {
				t.Fatal(err)
			}
			offline := true
			cfg.Offline = &offline
			if err = f.db.Repos().Update(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			f.up.Close()
			cached := f.get("pods-virtual", "sources/HelloPod/1.0.0/source.tar.gz")
			if cached.Code != 200 || !bytes.Equal(cached.Body.Bytes(), archive.Body.Bytes()) {
				t.Fatalf("offline archive=%d %s", cached.Code, cached.Body.String())
			}
		})
	}
}

func TestAccGitSourceFailureIsNotCached(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource fakeupstream.Resource
		dep      bool
	}{
		{"corrupt pack", fakeupstream.Resource{Method: "POST", Status: 200, Body: []byte("0008NAK\nPACKcorrupt")}, false},
		{"git redirect", fakeupstream.Resource{Redirect: "http://169.254.169.254/latest"}, false},
		{"missing submodule", fakeupstream.Resource{Status: 404}, true},
		{"submodule outage", fakeupstream.Resource{Status: 503}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouteFixture(t)
			seedGitSource(t, f.up, true)
			path := "/main.git/git-upload-pack"
			if tc.dep {
				path = "/dep.git/info/refs"
			}
			f.up.Set(path, tc.resource)
			result := f.get("pods-remote", "sources/HelloPod/1.0.0/source.tar.gz")
			if result.Code == 200 {
				t.Fatal("invalid Git tree published")
			}
			seedGitSource(t, f.up, true)
			result = f.get("pods-remote", "sources/HelloPod/1.0.0/source.tar.gz")
			if result.Code != 200 {
				t.Fatalf("repair = %d %s", result.Code, result.Body.String())
			}
		})
	}
}

func TestAccGitSourceMutableTagRefreshAndIntegrity(t *testing.T) {
	f := newRouteFixture(t)
	cfg, err := f.db.Repos().Get(t.Context(), "pods-remote")
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	cfg.RetrievalCachePeriodSecs = &zero
	cfg.MetadataRetrievalTimeoutSecs = &zero
	if err = f.db.Repos().Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	refs := seedGitSource(t, f.up, true)
	path := "sources/HelloPod/1.0.0/source.tar.gz"
	first := f.get("pods-remote", path)
	if first.Code != 200 {
		t.Fatalf("cold=%d %s", first.Code, first.Body.String())
	}
	packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	advertised := packet(
		"# service=git-upload-pack\n",
	) + "0000" + packet(
		refs["updated_commit"]+" HEAD\x00ofs-delta\n",
	) + packet(
		refs["updated_tag"]+" refs/tags/1.0.0\n",
	) + "0000"
	f.up.Set(
		"/main.git/info/refs",
		fakeupstream.Resource{
			Body:   []byte(advertised),
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-advertisement"}},
		},
	)
	pack, err := os.ReadFile("../../../test/fixtures/cocoapods-git/updated.pack")
	if err != nil {
		t.Fatal(err)
	}
	f.up.Set(
		"/main.git/git-upload-pack",
		fakeupstream.Resource{
			Method: "POST",
			Status: 200,
			Body:   append([]byte("0008NAK\n"), pack...),
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
		},
	)
	refreshed := f.get("pods-remote", path)
	if refreshed.Code != 200 || bytes.Equal(first.Body.Bytes(), refreshed.Body.Bytes()) {
		t.Fatalf("mutable Git tag not refreshed: %d %s", refreshed.Code, refreshed.Body.String())
	}
	f.up.Set(
		"/main.git/git-upload-pack",
		fakeupstream.Resource{Method: "POST", Status: 200, Body: []byte("0008NAK\nPACKcorrupt")},
	)
	corrupt := f.get("pods-remote", path)
	if corrupt.Code == 200 {
		t.Fatal("corrupt Git refresh fell back to successful stale archive")
	}
}
