package npmhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestAccNPMVirtualSearchInitialUnion(t *testing.T) {
	f := newRemoteFixture(t)
	f.deploy(t, "widget-local", "1.0.0", `{"name":"widget-local","version":"1.0.0","description":"local widget"}`, nil)
	c := *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local", "npm-remote"}})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.setJSON(
		"/-/v1/search",
		map[string]any{
			"objects": []any{
				map[string]any{"package": map[string]string{"name": "widget-remote", "version": "1.0.0"}, "score": map[string]float64{"final": 0.7}, "vendor": "retain"},
			},
			"total": 999,
			"time":  "upstream time",
		},
	)
	result := f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search?text=widget&size=20", "reader")
	wantStatus(t, result, 200)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(result.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc["total"]; present {
		t.Fatal(result.Body)
	}
	if _, present := doc["time"]; present {
		t.Fatal(result.Body)
	}
	if !strings.Contains(result.Body.String(), "widget-local") || !strings.Contains(result.Body.String(), "widget-remote") || !strings.Contains(result.Body.String(), "retain") {
		t.Fatal(result.Body)
	}
}

func TestAccRemoteSearchKeepsSafeNonSemverHit(t *testing.T) {
	f := newRemoteFixture(t)
	f.setJSON(
		"/-/v1/search",
		map[string]any{
			"objects": []any{map[string]any{"package": map[string]string{"name": "widget", "version": "Unity-1.0.0.0"}, "vendor": "retain"}},
			"total":   123,
			"time":    "keep direct envelope",
		},
	)
	result := f.do("GET", remoteRegistry+"-/v1/search?text=widget", "reader")
	wantStatus(t, result, 200)
	if !strings.Contains(result.Body.String(), "Unity-1.0.0.0") || !strings.Contains(result.Body.String(), "keep direct envelope") ||
		strings.Contains(result.Body.String(), `"total"`) {
		t.Fatal(result.Body)
	}
}

func virtualSearchFixture(t *testing.T, period int) *remoteFixture {
	t.Helper()
	f := newRemoteFixture(t)
	c := *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local", "npm-remote"}, VirtualRetrievalCachePeriodSecs: new(period)})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	remote, err := f.db.Repos().Get(t.Context(), "npm-remote")
	if err != nil {
		t.Fatal(err)
	}
	remote.RetrievalCachePeriodSecs, remote.MissedRetrievalCachePeriodSecs = new(3600), new(3600)
	if err := f.db.Repos().Update(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	f.opts.Cache = virtual.NewCache(virtual.CacheOptions{Now: f.clock.Now})
	f.mount(t, base)
	return f
}

func searchObjects(names ...string) map[string]any {
	objects := []any{}
	for _, name := range names {
		objects = append(objects, map[string]any{"package": map[string]string{"name": name, "version": "1.0.0"}, "vendor": name})
	}
	return map[string]any{"objects": objects, "time": "remote time", "total": 1000}
}

func searchNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	wantStatus(t, rec, 200)
	var doc struct {
		Objects []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, object := range doc.Objects {
		out = append(out, object.Package.Name)
	}
	return out
}

func TestAccNPMVirtualSearchCachesScopedRawPagesAndReadsLocalsLive(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	f.setJSON("/-/v1/search", searchObjects("mixed", "private"))
	path := "/artifactory/api/npm/npm-virtual/-/v1/search?text=MiXeD%20other%20third%20ignored"
	if got := searchNames(t, f.do("GET", path, "partial")); !reflect.DeepEqual(got, []string{"mixed"}) {
		t.Fatal(got)
	}
	before := f.up.Count("", "/-/v1/search")
	f.setJSON("/-/v1/search", searchObjects("changed"))
	if got := searchNames(t, f.do("GET", path, "partial")); !reflect.DeepEqual(got, []string{"mixed"}) || f.up.Count("", "/-/v1/search") != before {
		t.Fatal("scoped cache miss", got, f.up.Requests())
	}
	// The broad caller's visibility has its own cache identity, even though the
	// retained page is raw and every descendant is checked again on delivery.
	if got := searchNames(t, f.do("GET", path, "reader")); !reflect.DeepEqual(got, []string{"changed"}) || f.up.Count("", "/-/v1/search") <= before {
		t.Fatal("visibility shared", got)
	}
	f.deploy(t, "mixed-local", "1.0.0", `{"name":"mixed-local","version":"1.0.0","description":"mixed other third"}`, nil)
	localPath := "/artifactory/api/npm/npm-virtual/-/v1/search?text=mixed"
	if got := searchNames(t, f.do("GET", localPath, "reader")); !slices.Contains(got, "mixed-local") {
		t.Fatal("local index stale", got)
	}
	f.clock.Advance(601 * time.Second)
	if got := searchNames(t, f.do("GET", path, "partial")); len(got) != 0 {
		t.Fatal("expired scoped page survived", got)
	}
}

func TestAccNPMVirtualSearchPeriodForcesActualPositiveAndNegativeRefresh(t *testing.T) {
	for _, period := range []int{0, -1} {
		for _, status := range []int{200, 404} {
			t.Run(fmt.Sprintf("period-%d-status-%d", period, status), func(t *testing.T) {
				f := virtualSearchFixture(t, period)
				if status == 200 {
					f.setJSON("/-/v1/search", searchObjects("mixed"))
				}
				path := "/artifactory/api/npm/npm-virtual/-/v1/search"
				searchNames(t, f.do("GET", path, "partial"))
				before := f.up.Count("", "/-/v1/search")
				searchNames(t, f.do("GET", path, "partial"))
				if before == 0 || f.up.Count("", "/-/v1/search") <= before {
					t.Fatal("raw source age defeated virtual refresh", f.up.Requests())
				}
			})
		}
	}
}

func TestAccNPMVirtualSearchFiltersAndDuplicatesBeforeMultiWindowPaging(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	f.deploy(t, "widget-common", "1.0.0", `{"name":"widget-common","version":"1.0.0","description":"first owner"}`, nil)
	c, _ := f.db.Repos().Get(t.Context(), "npm-virtual")
	c.ExcludesPattern = new(".npm/hidden*/**")
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for i := range 100 {
		names = append(names, fmt.Sprintf("hidden%03d", i), "widget-common")
	}
	names = append(names, "widget-one", "widget-two", "widget-three")
	var calls atomic.Int32
	router := chi.NewRouter()
	router.Get("/-/v1/search", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if size < 1 || size > 500 {
			t.Errorf("unbounded size %d", size)
			w.WriteHeader(500)
			return
		}
		end := min(len(names), from+size)
		if from > len(names) {
			from = len(names)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchObjects(names[from:end]...))
	})
	up := httptest.NewServer(router)
	defer up.Close()
	remote, _ := f.db.Repos().Get(t.Context(), "npm-remote")
	remote.URL = up.URL
	if err := f.db.Repos().Update(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/api/npm/npm-virtual/-/v1/search?size=3"
	got := searchNames(t, f.do("GET", path, "reader"))
	if !reflect.DeepEqual(got, []string{"widget-common", "widget-one", "widget-two"}) || calls.Load() < 3 {
		t.Fatal("short filtered page", got, calls.Load())
	}
	got = searchNames(t, f.do("GET", path+"&from=1", "reader"))
	if !reflect.DeepEqual(got, []string{"widget-one", "widget-two", "widget-three"}) {
		t.Fatal("merged offset", got)
	}
	got = searchNames(t, f.do("GET", path+"&from="+strconv.Itoa(math.MaxInt), "reader"))
	if len(got) != 0 {
		t.Fatal("overflow", got)
	}
}

func TestAccNPMVirtualSearchExhaustedRemoteIsPassedOver(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	f.deploy(t, "widget-local", "1.0.0", `{"name":"widget-local","version":"1.0.0"}`, nil)
	var calls atomic.Int32
	router := chi.NewRouter()
	router.Get("/-/v1/search", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		names := make([]string, size)
		for i := range names {
			names[i] = "widget-remote"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchObjects(names...))
	})
	up := httptest.NewServer(router)
	defer up.Close()
	remote, _ := f.db.Repos().Get(t.Context(), "npm-remote")
	remote.URL = up.URL
	if err := f.db.Repos().Update(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	got := searchNames(t, f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search?size=3", "reader"))
	if !reflect.DeepEqual(got, []string{"widget-local"}) || calls.Load() != 32 {
		t.Fatal("exhausted source retained partial result or exceeded bound", got, calls.Load())
	}
}

func TestAccNPMVirtualSearchLocalFiltersBeforeCandidatePaging(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	for _, entry := range []struct{ name, version, description string }{
		{"a-query", "1.0.0", "widget"},
		{"a-query", "2.0.0", "unrelated"},
		{"b-hidden", "1.0.0", "widget"},
		{"c-visible", "1.0.0", "widget"},
	} {
		manifest, _ := json.Marshal(map[string]string{"name": entry.name, "version": entry.version, "description": entry.description})
		f.deploy(t, entry.name, entry.version, string(manifest), nil)
	}
	c, _ := f.db.Repos().Get(t.Context(), "npm-virtual")
	c.Repositories = []string{"npm-local"}
	c.ExcludesPattern = new(".npm/b-hidden/**")
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := searchNames(t, f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search?text=widget&size=1", "reader")); !reflect.DeepEqual(got, []string{"c-visible"}) {
		t.Fatal("candidate filters shortened the page", got)
	}
}

func TestAccNPMVirtualSearchFailureAndPolicyControls(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := virtualSearchFixture(t, 600)
			f.deploy(t, "mixed", "1.0.0", `{"name":"mixed","version":"1.0.0"}`, nil)
			f.up.Set("/-/v1/search", fakeupstream.Resource{Status: status})
			if got := searchNames(t, f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search", "reader")); !reflect.DeepEqual(got, []string{"mixed"}) {
				t.Fatal(got)
			}
		})
	}
	for _, policy := range []string{"offline", "blackout", "pattern", "unreadable"} {
		t.Run(policy, func(t *testing.T) {
			f := virtualSearchFixture(t, 600)
			f.setJSON("/-/v1/search", searchObjects("private"))
			c, _ := f.db.Repos().Get(t.Context(), "npm-remote")
			user := "reader"
			switch policy {
			case "offline":
				c.Offline = new(true)
			case "blackout":
				c.BlackedOut = new(true)
			case "pattern":
				c.ExcludesPattern = new(".npm/private/**")
			case "unreadable":
				user = "nobody"
			}
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if got := searchNames(t, f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search", user)); len(got) != 0 {
				t.Fatal(got)
			}
			if policy != "pattern" && f.up.Count("", "/-/v1/search") != 0 {
				t.Fatal("refused member fetched", f.up.Requests())
			}
		})
	}
}

func TestAccNPMVirtualSearchWarmSourceFaultStaysServerError(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	f.setJSON("/-/v1/search", searchObjects("mixed"))
	path := "/artifactory/api/npm/npm-virtual/-/v1/search"
	searchNames(t, f.do("GET", path, "reader"))
	resolve := f.svc.Resolve
	f.svc.Resolve = func(context.Context, repo.Config) (artmodel.SourceIdentity, error) {
		return artmodel.SourceIdentity{}, fmt.Errorf("read source store: %w", format.ErrStorageFault)
	}
	wantStatus(t, f.do("GET", path, "reader"), 500)
	f.svc.Resolve = resolve
}

func TestAccNPMVirtualSearchLocalStoreFaultStaysServerError(t *testing.T) {
	f := newFixture(t, func(st pkgroute.Store) pkgroute.Store { return failingStore{Store: st, failing: "search"} })
	c := *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local"}})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("GET", "/artifactory/api/npm/npm-virtual/-/v1/search", "reader"), 500)
}

func TestAccNPMVirtualSearchKeepsDeferredPathAndMethodRefusals(t *testing.T) {
	f := virtualSearchFixture(t, 600)
	for _, tc := range []struct{ method, path string }{
		{"POST", "-/v1/search"},
		{"PUT", "-/v1/search"},
		{"GET", "-/v1/search/"},
		{"GET", "-/v1/search;property=value"},
		{"GET", "-/npm/v1/keys"},
	} {
		wantStatus(t, f.do(tc.method, "/artifactory/api/npm/npm-virtual/"+tc.path, "reader"), 501)
	}
	if f.up.Count("", "/-/v1/search") != 0 {
		t.Fatal("deferred request fetched")
	}
}
