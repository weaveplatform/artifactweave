package helm_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func namespaceHelm(t *testing.T, w *harness.RemoteWorld, enabled bool, ddr string) {
	t.Helper()
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.UseNamespaces = &enabled
	c.DefaultDeploymentRepo = ddr
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func putHelmBytes(t *testing.T, w *harness.RemoteWorld, key, p string, body []byte) {
	t.Helper()
	r := w.Request("PUT", "/artifactory/"+key+"/"+p, "admin", string(body))
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAccHelmNamespaceReadsSelectOneMember(t *testing.T) {
	for _, relative := range []bool{true, false} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			w := virtualHelmWorld(t, relative)
			namespaceHelm(t, w, true, "")
			first := virtualChart(t, w, "first", "same.tgz", "web", "1.0.0")
			second := []byte(support.TarGz(t, map[string]string{"web/Chart.yaml": "apiVersion: v2\nname: web\nversion: 1.0.0\ndescription: second member\n"}))
			putHelmBytes(t, w, "second", "same.tgz", second)
			putHelmBytes(t, w, "second", "same.tgz.prov", []byte("second provenance"))
			putHelmBytes(t, w, "second", "only-in-second.tgz", second)
			putHelmBytes(t, w, "second", "first/collision.tgz", second)
			remote, _ := upstreamChart(t, w, "first/same.tgz", "remote", "1.0.0")
			upstreamHelmIndex(t, w, remote)
			index := readVirtualIndex(t, w, "admin", "X-JFrog-Override-Base-Url", "https://proxy.example/prefix")
			for _, versions := range index.Entries {
				for _, v := range versions {
					for _, location := range v.URLs {
						if relative {
							if !strings.HasPrefix(location, "first/") && !strings.HasPrefix(location, "second/") && !strings.HasPrefix(location, "remote/") {
								t.Fatal(location)
							}
						} else if !strings.HasPrefix(location, "https://proxy.example/prefix/artifactory/virtual/") {
							t.Fatal(location)
						}
					}
				}
			}
			for _, root := range []string{"/artifactory/api/helm/virtual/", "/artifactory/virtual/"} {
				for p, want := range map[string][]byte{"first/same.tgz": first, "second/same.tgz": second, "second/same.tgz.prov": []byte("second provenance"), "same.tgz": first} {
					r := w.Request("GET", root+p, "admin", "")
					if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), want) {
						t.Fatal(root+p, r.Code, r.Body.String())
					}
					head := w.Request("HEAD", root+p, "admin", "")
					if head.Code != 200 || head.Body.Len() != 0 {
						t.Fatal("HEAD", root+p, head.Code)
					}
				}
				for _, p := range []string{"first/only-in-second.tgz", "first/collision.tgz"} {
					r := w.Request("GET", root+p, "admin", "")
					if r.Code != 404 {
						t.Fatal("named miss fell through", root+p, r.Code)
					}
				}
				r := w.Request("GET", root+"first/same.tgz", "reader", "")
				if r.Code != 404 {
					t.Fatal("unreadable named member fell through", r.Code, r.Body.String())
				}
			}
			namespaceHelm(t, w, false, "")
			plain := readVirtualIndex(t, w, "admin")
			if strings.HasPrefix(plain.Entries["web"][0].URLs[0], "first/") {
				t.Fatal("cached namespaced index survived flag change")
			}
		})
	}
}

func TestAccHelmVirtualExternalURLs(t *testing.T) {
	for _, relative := range []bool{true, false} {
		for _, namespaces := range []bool{true, false} {
			t.Run(fmt.Sprintf("relative=%t/namespaces=%t", relative, namespaces), func(t *testing.T) {
				external := fakeupstream.New(t, fakeupstream.Options{})
				w := virtualHelmWorld(t, relative, func(c *config.Config) { c.Remote.AllowedPrivateOrigins = []string{external.URL} })
				namespaceHelm(t, w, namespaces, "")
				allowed, body := upstreamChart(t, w, "allowed.tgz", "allowed", "1.0.0")
				external.Set("/releases/allowed.tgz", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/gzip"}}})
				allowed.URLs = []string{external.URL + "/releases/allowed.tgz?token=private"}
				blocked, _ := upstreamChart(t, w, "blocked.tgz", "blocked", "1.0.0")
				blocked.URLs = []string{external.URL + "/blocked/blocked.tgz"}
				w.Repo.ExternalDependenciesEnabled = new(true)
				w.Repo.ExternalDependenciesPatterns = []string{external.URL + "/releases/**"}
				w.Update()
				upstreamHelmIndex(t, w, allowed, blocked)
				index := readVirtualIndex(t, w, "admin")
				if len(index.Entries["allowed"]) != 1 || len(index.Entries["blocked"]) != 0 {
					t.Fatal(index)
				}
				location := index.Entries["allowed"][0].URLs[0]
				if strings.Contains(location, "private") || strings.Contains(location, "token=") {
					t.Fatal("upstream query leaked", location)
				}
				if relative {
					location = "/artifactory/api/helm/virtual/" + location
				} else {
					u, err := url.Parse(location)
					if err != nil {
						t.Fatal(err)
					}
					location = u.RequestURI()
					if !strings.HasPrefix(location, "/artifactory/api/helm/virtual/") {
						t.Fatal(location)
					}
				}
				r := w.Request("GET", location, "admin", "")
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatal(location, r.Code, r.Body.String())
				}
				for _, req := range external.Requests() {
					if req.Header.Get("Authorization") != "" {
						t.Fatal("credentials forwarded externally")
					}
				}
				if external.Count("GET", "/blocked/blocked.tgz") != 0 {
					t.Fatal("blocked chart fetched")
				}
				// The direct remote retains its strict whole-document admission.
				direct := w.Request("GET", "/artifactory/api/helm/remote/index.yaml", "admin", "")
				if direct.Code != 502 {
					t.Fatal("direct remote behavior changed", direct.Code)
				}
			})
		}
	}
}

func TestAccHelmVirtualPublishRefreshesNamespacedIndex(t *testing.T) {
	w := virtualHelmWorld(t, true)
	namespaceHelm(t, w, true, "first")
	virtualChart(t, w, "first", "old.tgz", "old", "1.0.0")
	for range 2 {
		readVirtualIndex(t, w, "admin")
	}
	archive := helmChart("published", "1.0.0")
	putHelmBytes(t, w, "virtual", "published-1.0.0.tgz", archive)
	putHelmBytes(t, w, "virtual", "published-1.0.0.tgz.prov", []byte("published provenance"))
	if _, err := w.Runtime.Store.Items().Get(t.Context(), "first", false, "published-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	index := readVirtualIndex(t, w, "admin")
	if got := index.Entries["published"]; len(got) != 1 || got[0].URLs[0] != "first/published-1.0.0.tgz" {
		t.Fatal(index)
	}
	namespaceHelm(t, w, true, "")
	r := w.Request("PUT", "/artifactory/virtual/other.tgz", "admin", string(archive))
	if r.Code != 405 || r.Header().Get("Allow") != "GET" || !strings.Contains(r.Body.String(), "No local repository was configured") {
		t.Fatal(r.Code, r.Header(), r.Body.String())
	}
}

func TestAccHelmNamedRemoteItemAndTrace(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := virtualHelmWorld(t, true)
			namespaceHelm(t, w, true, "")
			w.Repo.StoreArtifactsLocally = &store
			w.Update()
			chart, body := upstreamChart(t, w, "remote.tgz", "remote", "1.0.0")
			upstreamHelmIndex(t, w, chart)
			for _, root := range []string{"/artifactory/api/helm/virtual/", "/artifactory/virtual/"} {
				r := w.Request("GET", root+"remote/remote.tgz", "admin", "")
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) {
					t.Fatal(root, r.Code, r.Body.String())
				}
			}
			trace := w.Request("GET", "/artifactory/virtual/remote/remote.tgz?trace", "admin", "")
			if trace.Code != 200 || !strings.Contains(trace.Body.String(), "Request succeeded") || trace.Header().Get("Content-Type") != "text/plain" {
				t.Fatal(trace.Code, trace.Body.String())
			}
		})
	}
}
