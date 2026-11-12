package chef_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// cappedSupermarket is an upstream Supermarket of cookbooks named
// cookbook-0000 upwards, sorted by name, each with the one version 1.0.0.
// It serves at most limit of them a catalog or search page whatever items
// asks, as Supermarket serves at most API_ITEM_LIMIT, 100 when that is not
// set (chef/supermarket, v1_controller.rb, init_params and item_limit;
// supermarket.chef.io answered items=1000 with 100 of its 4,078 cookbooks
// on 2026-11-12). Its page holds start, total and items, and states no
// limit. Its search lists the cookbooks whose name holds q, by name, and
// totals every cookbook, as Supermarket's search totals Cookbook.count
// (cookbooks_controller.rb, search). It records the start and items of
// each catalog page asked for, and the query of each search.
type cappedSupermarket struct {
	*httptest.Server
	names []string
	limit int
	// short serves one cookbook fewer than a catalog page that has more
	// after it should hold.
	short    bool
	mu       sync.Mutex
	asked    map[string]bool
	searched map[string]bool
}

func newCappedSupermarket(t *testing.T, cookbooks, limit int, short bool) *cappedSupermarket {
	t.Helper()
	s := &cappedSupermarket{
		limit: limit, short: short, asked: map[string]bool{}, searched: map[string]bool{},
	}
	for i := range cookbooks {
		s.names = append(s.names, fmt.Sprintf("cookbook-%04d", i))
	}
	r := chi.NewRouter()
	r.Get("/api/v1/cookbooks", s.catalog)
	r.Get("/api/v1/search", s.search)
	r.Get("/api/v1/cookbooks/{name}", s.cookbook)
	r.Get("/api/v1/cookbooks/{name}/versions/{version}", s.version)
	s.Server = httptest.NewServer(r)
	t.Cleanup(s.Close)
	return s
}

func (s *cappedSupermarket) catalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Supermarket's own defaults: start 0, items 10.
	start, items := 0, 10
	if q.Has("start") {
		start, _ = strconv.Atoi(q.Get("start"))
	}
	if q.Has("items") {
		items, _ = strconv.Atoi(q.Get("items"))
	}
	s.mu.Lock()
	s.asked[fmt.Sprintf("start=%d&items=%s", start, q.Get("items"))] = true
	s.mu.Unlock()
	total := len(s.names)
	from := min(start, total)
	end := min(from+min(items, s.limit), total)
	served := s.names[from:end]
	if s.short && end < total {
		served = served[:len(served)-1]
	}
	out := []map[string]string{}
	for _, name := range served {
		out = append(out, map[string]string{
			"cookbook_name":        name,
			"cookbook_maintainer":  "acme",
			"cookbook_description": "the " + name + " cookbook",
			"cookbook":             s.URL + "/api/v1/cookbooks/" + name,
		})
	}
	supermarketJSON(w, map[string]any{"start": start, "total": total, "items": out})
}

func (s *cappedSupermarket) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, items := 0, 10
	if q.Has("start") {
		start, _ = strconv.Atoi(q.Get("start"))
	}
	if q.Has("items") {
		items, _ = strconv.Atoi(q.Get("items"))
	}
	s.mu.Lock()
	s.searched[r.URL.RawQuery] = true
	s.mu.Unlock()
	matched := []string{}
	for _, name := range s.names {
		if strings.Contains(name, q.Get("q")) {
			matched = append(matched, name)
		}
	}
	from := min(start, len(matched))
	out := []map[string]string{}
	for _, name := range matched[from:min(from+min(items, s.limit), len(matched))] {
		out = append(out, map[string]string{
			"cookbook_name":        name,
			"cookbook_maintainer":  "acme",
			"cookbook_description": "the " + name + " cookbook",
			"cookbook":             s.URL + "/api/v1/cookbooks/" + name,
		})
	}
	supermarketJSON(w, map[string]any{"start": start, "total": len(s.names), "items": out})
}

// searchesAsked returns the query of every search the remote forwarded,
// sorted.
func (s *cappedSupermarket) searchesAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for query := range s.searched {
		out = append(out, query)
	}
	slices.Sort(out)
	return out
}

func (s *cappedSupermarket) cookbook(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !slices.Contains(s.names, name) {
		http.NotFound(w, r)
		return
	}
	version := s.URL + "/api/v1/cookbooks/" + name + "/versions/1_0_0"
	supermarketJSON(w, map[string]any{
		"name": name, "maintainer": "acme", "description": "the " + name + " cookbook",
		"latest_version": version, "versions": []string{version},
	})
}

func (s *cappedSupermarket) version(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !slices.Contains(s.names, name) || chi.URLParam(r, "version") != "1_0_0" {
		http.NotFound(w, r)
		return
	}
	cookbook := s.URL + "/api/v1/cookbooks/" + name
	supermarketJSON(w, map[string]any{
		"version": "1.0.0", "license": "Apache-2.0", "tarball_file_size": 100,
		"file": cookbook + "/versions/1.0.0/download", "cookbook": cookbook,
		"dependencies": map[string]string{}, "platforms": map[string]string{},
	})
}

// pagesAsked returns the start and items of every catalog page the remote
// asked for, sorted.
func (s *cappedSupermarket) pagesAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for page := range s.asked {
		out = append(out, page)
	}
	slices.Sort(out)
	return out
}

func supermarketJSON(w http.ResponseWriter, doc any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(doc)
}

// catalogAnswer returns the exact body the catalog at prefix answers for
// names: those whose name holds q, from start, at most items of them, with
// links under prefix.
func catalogAnswer(t *testing.T, prefix string, names []string, q string, start, items int) string {
	t.Helper()
	return pageAnswer(t, prefix, names, q, start, items, -1)
}

// pageAnswer is catalogAnswer whose total is total, unless total is -1,
// when it is the number of names that hold q. A remote's search answers
// the total its upstream's search gave, which Supermarket's counts every
// cookbook (#1306).
func pageAnswer(
	t *testing.T,
	prefix string,
	names []string,
	q string,
	start, items, total int,
) string {
	t.Helper()
	matched := []map[string]any{}
	for _, name := range names {
		if strings.Contains(name, q) {
			matched = append(matched, map[string]any{
				"cookbook_name":        name,
				"cookbook_description": "the " + name + " cookbook",
				"cookbook_maintainer":  "acme",
				"cookbook":             "http://example.com" + prefix + "/api/v1/cookbooks/" + name,
			})
		}
	}
	from := min(start, len(matched))
	if total == -1 {
		total = len(matched)
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(map[string]any{
		"total": total, "start": start,
		"items": matched[from:min(from+items, len(matched))],
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// wantCatalog fails unless the catalog under prefix answers 200 with every
// cookbook s holds, read whole, a page at a time and through a search
// whose matches lie on more than one upstream catalog page. The search is
// forwarded to the upstream's search and answers what its one page holds,
// with the upstream's total, which counts every cookbook (#1306).
func (s *cappedSupermarket) wantCatalog(t *testing.T, w *harness.RemoteWorld, prefix, user string) {
	t.Helper()
	for route, body := range map[string]string{
		"/api/v1/cookbooks":                   catalogAnswer(t, prefix, s.names, "", 0, 1000),
		"/api/v1/cookbooks?start=0&items=100": catalogAnswer(t, prefix, s.names, "", 0, 100),
		"/api/v1/cookbooks?start=1&items=1":   catalogAnswer(t, prefix, s.names, "", 1, 1),
		"/api/v1/cookbooks?start=100&items=100": catalogAnswer(
			t, prefix, s.names, "", 100, 100,
		),
		"/api/v1/search?q=5": pageAnswer(t, prefix, s.names, "5", 0, 1000, len(s.names)),
	} {
		r := w.Request(http.MethodGet, prefix+route, user, "")
		if r.Code != http.StatusOK || r.Body.String() != body {
			t.Fatalf("%s: got %d %s\nwant 200 %s", route, r.Code, r.Body.String(), body)
		}
	}
}

// chefRemote returns a world whose remote is a Chef remote over up.
func chefRemote(t *testing.T, up *cappedSupermarket) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Chef
	w.Repo.URL = up.URL
	w.Update()
	return w
}

// A Chef remote asks its upstream for 100 cookbooks a catalog page and
// starts the next page after the cookbooks a page held, so the catalog of
// a Supermarket with more cookbooks than it serves a page lists every one
// of them, through the remote and through a virtual repository over it
// (#1298). Its search forwards the query to the upstream's search, once
// (#1306).
func TestAccChefRemoteReadsEverySupermarketPage(t *testing.T) {
	const remote = "/artifactory/api/chef/remote"
	up := newCappedSupermarket(t, 150, 100, false)
	w := chefRemote(t, up)
	up.wantCatalog(t, w, remote, "reader")
	if got := up.pagesAsked(); !slices.Equal(got,
		[]string{"start=0&items=100", "start=100&items=100"}) {
		t.Fatalf("pages asked: %v", got)
	}
	if got := up.searchesAsked(); !slices.Equal(got, []string{"items=100&q=5&start=0"}) {
		t.Fatalf("searches asked: %v", got)
	}
	v := *repo.Defaults(&repo.Config{
		Key: "cvirtual", Class: repo.Virtual, PackageType: repo.Chef,
		Repositories: []string{w.Repo.Key},
	})
	if err := w.Runtime.Store.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	up.wantCatalog(t, w, "/artifactory/api/chef/cvirtual", "admin")
}

// A Supermarket whose catalog fits on one page, and a page short of 100
// with more after it, answer as they did before #1298: every catalog
// answer of the one, and 502 on each of the other's that reads the short
// page, through the remote and through a virtual repository over it. A
// remote reads only the pages a catalog page covers, and forwards a
// search, so the page after the short one and the search answer 200
// (#1306).
func TestAccChefRemotePagingKeepsItsOtherAnswers(t *testing.T) {
	const remote = "/artifactory/api/chef/remote"
	for _, cookbooks := range []int{2, 100} {
		t.Run(fmt.Sprint(cookbooks, " cookbooks"), func(t *testing.T) {
			up := newCappedSupermarket(t, cookbooks, 100, false)
			up.wantCatalog(t, chefRemote(t, up), remote, "reader")
		})
	}
	t.Run("short with more after it", func(t *testing.T) {
		up := newCappedSupermarket(t, 150, 100, true)
		w := chefRemote(t, up)
		v := *repo.Defaults(&repo.Config{
			Key: "cvirtual", Class: repo.Virtual, PackageType: repo.Chef,
			Repositories: []string{w.Repo.Key},
		})
		if err := w.Runtime.Store.Repos().Create(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		// The remote's source refuses the page while it validates it, and
		// answers its own 502 rather than parseCatalog's message.
		const refused = `{"errors":[{"message":"Bad Gateway","status":502}]}` + "\n"
		for _, route := range []string{
			remote + "/api/v1/cookbooks", "/artifactory/api/chef/cvirtual/api/v1/cookbooks",
		} {
			r := w.Request(http.MethodGet, route, "admin", "")
			if r.Code != http.StatusBadGateway || r.Body.String() != refused {
				t.Fatalf("%s: got %d %s", route, r.Code, r.Body.String())
			}
		}
		for route, body := range map[string]string{
			"/api/v1/cookbooks?start=100&items=100": catalogAnswer(
				t, remote, up.names, "", 100, 100,
			),
			"/api/v1/search?q=5": pageAnswer(t, remote, up.names, "5", 0, 1000, 150),
		} {
			r := w.Request(http.MethodGet, remote+route, "admin", "")
			if r.Code != http.StatusOK || r.Body.String() != body {
				t.Fatalf("%s: got %d %s\nwant 200 %s", route, r.Code, r.Body.String(), body)
			}
		}
	})
}
