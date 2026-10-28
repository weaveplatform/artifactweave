package ansible_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const remoteList = "/api/v3/collections/weave/sample/versions/"

func TestAccGalaxyRemoteRefusesMalformedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		status           int
	}{
		{"bad json", remoteList, "{", 502},
		{"oversized metadata", remoteList, strings.Repeat(" ", 2<<20) + `{"data":[]}`, 502},
		{"pagination cycle", remoteList, `{"data":[],"links":{"next":"?limit=100&offset=0"}}`, 502},
		{"bad list", remoteList, `{"data":[{"version":"../bad"}]}`, 502},
		{"missing list", remoteList, `{}`, 502},
		{"duplicate versions", remoteList, `{"data":[{"version":"1.2.3"},{"version":"1.2.3"}]}`, 502},
		{"foreign next", remoteList, `{"data":[],"links":{"next":"http://elsewhere.invalid/collections/"}}`, 502},
		{"unsafe next", remoteList, `{"data":[],"links":{"next":"http://["}}`, 502},
		{"wrong identity", remoteList + "1.2.3/", `{"version":"2.0.0"}`, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			up.Set(
				tc.path,
				fakeupstream.Resource{
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   []byte(tc.body),
				},
			)
			requireStatus(t, f.do("GET", base+remoteList, nil, "", ""), tc.status)
		})
	}
	for _, tc := range []struct {
		name   string
		modify func(map[string]any)
	}{
		{"invalid digest", func(m map[string]any) { m["artifact"] = map[string]string{"sha256": strings.Repeat("x", 64)} }},
		{"credential URL", func(m map[string]any) { m["download_url"] = "https://secret@host/file" }},
		{"relative URL", func(m map[string]any) { m["download_url"] = "/file" }},
		{"non HTTP", func(m map[string]any) { m["download_url"] = "file://host/path" }},
		{"bad dependency", func(m map[string]any) {
			m["metadata"] = map[string]any{"dependencies": map[string]string{"../evil": "*"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			m := map[string]any{
				"version":      "1.2.3",
				"namespace":    map[string]string{"name": "weave"},
				"collection":   map[string]string{"name": "sample"},
				"artifact":     map[string]string{"sha256": strings.Repeat("0", 64)},
				"download_url": up.URL + "/file",
			}
			tc.modify(m)
			data, _ := json.Marshal(m)
			up.Set(
				remoteList+"1.2.3/",
				fakeupstream.Resource{
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   data,
				},
			)
			requireStatus(t, f.do("GET", base+remoteList, nil, "", ""), 502)
		})
	}
	for _, status := range []int{404, 401, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			up.Set(remoteList, fakeupstream.Resource{Status: status})
			w := f.do("GET", base+remoteList, nil, "", "")
			if w.Code == 200 || strings.Contains(w.Body.String(), "1.2.3") {
				t.Fatalf("upstream failure succeeded: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestAccGalaxyRemoteRefreshAndColdOffline(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	requireStatus(t, f.do("GET", base+remoteList, nil, "", ""), 200)
	requireStatus(t, f.do("GET", base+remoteList, nil, "", ""), 200)
	if up.Count("GET", remoteList) != 1 {
		t.Fatal("warm metadata fetched again")
	}
	up.Set(
		remoteList,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"data":[],"links":{"next":null}}`),
		},
	)
	if _, err := f.items.Zap(t.Context(), item.Target{Repo: c, Cached: true}); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", base+remoteList, nil, "", "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "1.2.3") || up.Count("GET", remoteList) != 2 {
		t.Fatalf("zap did not refresh metadata: %s", w.Body)
	}
	c.Offline = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	before := up.Count("", "")
	requireStatus(
		t,
		f.do("GET", base+"/api/v3/collections/weave/absent/versions/", nil, "", ""),
		404,
	)
	if up.Count("", "") != before {
		t.Fatal("offline miss contacted upstream")
	}
}

func TestAccGalaxyRemotePaginationAndVirtualMiss(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	router := chi.NewRouter()
	router.Get(remoteList, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "0" {
			_, _ = w.Write([]byte(`{"data":[],"links":{"next":"?limit=100&offset=100"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"version":"1.2.3"}],"links":{"next":null}}`))
	})
	router.Get(remoteList+"1.2.3/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).
			Encode(map[string]any{"version": "1.2.3", "namespace": map[string]string{"name": "weave"}, "collection": map[string]string{"name": "sample"}, "artifact": map[string]string{"sha256": strings.Repeat("0", 64)}, "download_url": up.URL + "/file"})
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	c.URL = srv.URL
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", base+remoteList, nil, "", "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "1.2.3") {
		t.Fatal("second page missing")
	}
	v := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible, Repositories: []string{c.Key, f.repo.Key}})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	// A genuine upstream miss falls through to a later readable local member.
	c.URL = up.URL
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	up.Set(remoteList, fakeupstream.Resource{Status: 404})
	requireStatus(t, f.publish(f.data, ""), 201)
	requireStatus(t, f.do("GET", "/artifactory/api/ansible/virtual"+remoteList, nil, "", ""), 200)
}

func TestAccGalaxyNoStoreAndInvalidArchiveAreNeverCached(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	c.StoreArtifactsLocally = new(false)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	endpoint := base + "/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz"
	for range 2 {
		w := f.do("GET", endpoint, nil, "", "")
		requireStatus(t, w, 200)
		if !bytes.Equal(w.Body.Bytes(), f.data) {
			t.Fatal("transient archive changed")
		}
	}
	if _, err := f.db.Items().
		Get(t.Context(), c.Key, true, "collections/weave/sample/1.2.3/weave-sample-1.2.3.tar.gz"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("no-store archive published: %v", err)
	}
	if up.Count("GET", "/files/weave-sample-1.2.3.tar.gz") != 2 {
		t.Fatal("no-store archive was reused")
	}
}
