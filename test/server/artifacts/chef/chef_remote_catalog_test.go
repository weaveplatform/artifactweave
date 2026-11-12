package chef_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// countedSupermarket fronts a cappedSupermarket and records the path and
// query of every GET the remote sends it, so a test can count the catalog
// pages, searches and cookbook or version records one catalog request
// reads. The remote's transport sends a HEAD before each GET, which is not
// counted.
type countedSupermarket struct {
	*cappedSupermarket
	front *httptest.Server
	mu    sync.Mutex
	seen  []string
}

func newCountedSupermarket(t *testing.T, cookbooks int) *countedSupermarket {
	t.Helper()
	s := &countedSupermarket{cappedSupermarket: newCappedSupermarket(t, cookbooks, 100, false)}
	s.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.mu.Lock()
			s.seen = append(s.seen, r.URL.RequestURI())
			s.mu.Unlock()
		}
		s.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(s.front.Close)
	return s
}

// take returns the requests recorded since the last take, in order.
func (s *countedSupermarket) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.seen
	s.seen = nil
	return out
}

// records counts the requests for a cookbook or version record.
func records(requests []string) int {
	n := 0
	for _, r := range requests {
		if strings.HasPrefix(r, "/api/v1/cookbooks/") {
			n++
		}
	}
	return n
}

// A remote over a Supermarket of 4,200 cookbooks, more than the 4,096 a
// remote accepted before, answers Knife's list and search from the
// upstream pages they cover, without reading a cookbook or version
// record, through the remote and through a virtual repository over it.
// Knife's items=9999999 is clamped to a page of 1,000; a page past 4,096
// is served; order and user change nothing and are not forwarded (#1305,
// #1306).
func TestAccChefRemoteCatalogPastMaxVersions(t *testing.T) {
	const remote = "/artifactory/api/chef/remote"
	up := newCountedSupermarket(t, 4200)
	w := chefRemote(t, up.cappedSupermarket)
	w.Repo.URL = up.front.URL
	w.Update()
	v := *repo.Defaults(&repo.Config{
		Key: "cvirtual", Class: repo.Virtual, PackageType: repo.Chef,
		Repositories: []string{w.Repo.Key},
	})
	if err := w.Runtime.Store.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	firstThousand := []string{}
	for start := 0; start < 1000; start += 100 {
		firstThousand = append(firstThousand,
			"/api/v1/cookbooks?items=100&start="+strconv.Itoa(start))
	}
	for _, tc := range []struct {
		route, body string
		asked       []string
	}{
		{
			"/api/v1/cookbooks?items=9999999&start=0",
			catalogAnswer(t, remote, up.names, "", 0, 1000), firstThousand,
		},
		// The pages the last answer read are cached, so a request that
		// forwarded order or user would ask for pages of its own.
		{
			"/api/v1/cookbooks?items=9999999&start=0&order=most_downloaded&user=someone",
			catalogAnswer(t, remote, up.names, "", 0, 1000), nil,
		},
		{
			"/api/v1/cookbooks?start=4150&items=1000",
			catalogAnswer(t, remote, up.names, "", 4150, 1000),
			[]string{"/api/v1/cookbooks?items=100&start=4100"},
		},
		{
			"/api/v1/cookbooks?start=5000",
			catalogAnswer(t, remote, up.names, "", 5000, 1000),
			[]string{"/api/v1/cookbooks?items=100&start=5000"},
		},
		{
			"/api/v1/search?q=cookbook-41&items=9999999&start=0",
			pageAnswer(t, remote, up.names, "cookbook-41", 0, 1000, 4200),
			[]string{"/api/v1/search?items=100&q=cookbook-41&start=0"},
		},
	} {
		up.take()
		r := w.Request(http.MethodGet, remote+tc.route, "reader", "")
		if r.Code != http.StatusOK || r.Body.String() != tc.body {
			t.Fatalf(
				"%s: got %d %.300s\nwant 200 %.300s",
				tc.route,
				r.Code,
				r.Body.String(),
				tc.body,
			)
		}
		if got := up.take(); !slices.Equal(got, tc.asked) {
			t.Fatalf("%s asked its upstream %v, want %v", tc.route, got, tc.asked)
		}
	}
	// The virtual repository answers the same pages under its own links,
	// from the remote's cached upstream pages.
	const virtual = "/artifactory/api/chef/cvirtual"
	for route, body := range map[string]string{
		"/api/v1/cookbooks?items=9999999&start=0": catalogAnswer(t, virtual, up.names, "", 0, 1000),
		"/api/v1/cookbooks?start=4150&items=1000": catalogAnswer(t, virtual, up.names, "", 4150, 1000),
	} {
		r := w.Request(http.MethodGet, virtual+route, "admin", "")
		if r.Code != http.StatusOK || r.Body.String() != body {
			t.Fatalf("%s: got %d %.300s", route, r.Code, r.Body.String())
		}
	}
	if got := up.take(); records(got) != 0 {
		t.Fatalf("the virtual repository read cookbook records: %v", got)
	}
}
