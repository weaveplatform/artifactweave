package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestAccPuppetRemoteCacheRefreshOfflineAndIntegrity(t *testing.T) {
	w := newRemoteWorld(t)
	data, err := os.ReadFile("../../test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../../test/acceptance/fixtures/puppet/widget/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	var fetches atomic.Int64
	var status atomic.Int64
	var corrupt atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if status.Load() != 0 {
			out.WriteHeader(int(status.Load()))
			return
		}
		switch r.URL.Path {
		case "/v3/releases":
			if r.URL.Query().Get("module") != "acme-widget" {
				http.NotFound(out, r)
				return
			}
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).
				Encode(map[string]any{"results": []any{map[string]any{"version": "1.0.0", "file_uri": "/v3/files/acme-widget-1.0.0.tar.gz", "file_sha256": hex.EncodeToString(digest[:]), "file_size": len(data), "metadata": json.RawMessage(manifest)}}, "pagination": map[string]any{"offset": 0, "total": 1, "next": nil}})
		case "/v3/files/acme-widget-1.0.0.tar.gz":
			http.Redirect(out, r, "/blob", http.StatusFound)
		case "/blob":
			if corrupt.Load() {
				_, _ = out.Write([]byte(strings.Repeat("x", len(data))))
			} else {
				_, _ = out.Write(data)
			}
		default:
			http.NotFound(out, r)
		}
	}))
	defer up.Close()
	w.c.PackageType = repo.Puppet
	w.c.URL = up.URL
	w.update()
	metadata := "/artifactory/api/puppet/remote/v3/releases?module=acme-widget"
	archive := "/artifactory/api/puppet/remote/v3/files/acme-widget-1.0.0.tar.gz"
	remoteStatus(t, w.request("GET", metadata, "denied", ""), 403)
	if fetches.Load() != 0 {
		t.Fatal("denied metadata reached upstream")
	}
	remoteStatus(t, w.request("GET", archive, "denied", ""), 403)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	corrupt.Store(true)
	remoteStatus(t, w.request("GET", archive, "reader", ""), 502)
	corrupt.Store(false)
	r := w.request("GET", archive, "reader", "")
	remoteStatus(t, r, 200)
	if r.Body.String() != string(data) {
		t.Fatal("archive differs")
	}
	before := fetches.Load()
	remoteStatus(t, w.request("GET", archive, "reader", ""), 200)
	if fetches.Load() != before {
		t.Fatal("cache hit refetched")
	}
	w.c.Offline = new(true)
	w.update()
	status.Store(503)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	remoteStatus(t, w.request("GET", archive, "reader", ""), 200)
	if fetches.Load() != before {
		t.Fatal("offline contacted upstream")
	}
	w.c.Offline = new(false)
	w.c.RetrievalCachePeriodSecs = new(1)
	w.c.AssumedOfflinePeriodSecs = new(0)
	w.update()
	service := item.Service{Store: w.rt.store, Blobs: w.rt.blobs}
	if _, err := service.Zap(
		t.Context(),
		item.Target{Repo: w.c, Cached: true, Path: "v3/releases"},
	); err != nil {
		t.Fatal(err)
	}
	// P9 retains a complete validated response during a transient upstream outage.
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
	if fetches.Load() <= before {
		t.Fatal("zap failed to refresh metadata")
	}
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/api/puppet/remote/v3/releases?module=acme-missing",
			"reader",
			"",
		),
		503,
	)
	status.Store(0)
	remoteStatus(t, w.request("GET", metadata, "reader", ""), 200)
}

func TestAccPuppetRegisteredRoutesAndReindex(t *testing.T) {
	w := newRemoteWorld(t)
	data, err := os.ReadFile("../../test/acceptance/fixtures/puppet/acme-widget-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "puppet", Class: repo.Local, PackageType: repo.Puppet})
	if err := w.rt.store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/puppet/acme-widget/acme-widget-1.0.0.tar.gz"
	remoteStatus(t, w.request("PUT", path, "denied", string(data)), 403)
	remoteStatus(t, w.request("PUT", path, "admin", "bad"), 400)
	remoteStatus(t, w.request("PUT", path, "admin", string(data)), 201)
	for _, route := range []string{"/v3/modules?query=acme-widget", "/v3/modules/acme-widget", "/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.0.0"} {
		remoteStatus(t, w.request("GET", "/artifactory/api/puppet/puppet"+route, "admin", ""), 200)
	}
	remoteStatus(
		t,
		w.request("POST", "/artifactory/api/puppet/puppet/reindex?bad=1", "admin", ""),
		400,
	)
	remoteStatus(t, w.request("POST", "/artifactory/api/puppet/puppet/reindex", "denied", ""), 403)
	remoteStatus(t, w.request("POST", "/artifactory/api/puppet/puppet/reindex", "admin", ""), 200)
	remoteStatus(t, w.request("DELETE", path, "admin", ""), 204)
	remoteStatus(
		t,
		w.request("GET", "/artifactory/api/puppet/puppet/v3/modules/acme-widget", "admin", ""),
		404,
	)
}
