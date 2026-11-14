package artifacts_test

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// Get Repositories names its next window in a Link header while more
// repositories follow (#1408, decision 0043): the client's path and query,
// every parameter kept, only offset moved on by limit. JFrog sends no
// Link, so it is an added header and nothing else of the answer changes.

// nextLink matches a Link header's one next link, <target>; rel="next".
var nextLink = regexp.MustCompile(`^<([^>]+)>; rel="next"$`)

// linkTarget is the target of rec's next link, "" without one.
func linkTarget(t *testing.T, rec interface{ Header() http.Header }) string {
	t.Helper()
	v := rec.Header().Get("Link")
	if v == "" {
		return ""
	}
	m := nextLink.FindStringSubmatch(v)
	if m == nil {
		t.Fatalf("Link %q is not one next link", v)
	}
	return m[1]
}

// TestAccRepositoriesLinkNextWindow: the Link of each window of the paging
// world, absent on the last window, past the end and without limit.
func TestAccRepositoriesLinkNextWindow(t *testing.T) {
	w := pagingWorld(t, false)
	const list = "/artifactory/api/repositories"
	for _, c := range []struct{ query, want string }{
		{"?limit=2", list + "?limit=2&offset=2"},
		{"?limit=2&offset=1", list + "?limit=2&offset=3"},
		// The client's order and every parameter stay as sent.
		{"?offset=3&type=local&limit=2", list + "?offset=5&type=local&limit=2"},
		{"?packageType=GENERIC&limit=1", list + "?packageType=GENERIC&limit=1&offset=1"},
		{"?limit=5&offset=5", list + "?limit=5&offset=10"},
		// The last window, a window past the end and a whole list have none.
		{"?limit=5&offset=10", ""},
		{"?limit=12", ""},
		{"?limit=1000", ""},
		{"?limit=2&offset=100", ""},
		{"", ""},
		{"?type=local", ""},
		{"?type=distribution&limit=1", ""},
		// The provider's trailing slash names the list without it.
		{"/?limit=4", list + "?limit=4&offset=4"},
	} {
		rec := w.do(http.MethodGet, list+c.query, "admin", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: %d %s", c.query, rec.Code, rec.Body)
		}
		if got := linkTarget(t, rec); got != c.want {
			t.Errorf("%q: Link target %q, want %q", c.query, got, c.want)
		}
	}
	// The vendor media type carries it too; a refused request has none.
	rec := w.do(http.MethodGet, list+"?limit=2", "admin", "", "Accept", mediaDetails)
	if got := linkTarget(t, rec); got != list+"?limit=2&offset=2" {
		t.Errorf("vendor list Link target %q", got)
	}
	if rec := w.do(http.MethodGet, list+"?limit=0", "admin", ""); rec.Code != http.StatusBadRequest ||
		rec.Header().Get("Link") != "" {
		t.Errorf("refused page: %d Link %q", rec.Code, rec.Header().Get("Link"))
	}
}

// walkWorld is the paging world with 120 repositories more, of every class,
// and a permission letting user read every remote and two named locals: a
// partial reader whose visible list is not one contiguous run of keys.
func walkWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := pagingWorld(t, true)
	ctx := context.Background()
	classes := []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated}
	for i := range 120 {
		c := repo.Config{Key: fmt.Sprintf("w-%03d", i), Class: classes[i%len(classes)]}
		if c.Class == repo.Remote {
			c.URL = "https://up.example"
		}
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	read := accesstest.Target{Include: []string{"**"}}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "partial",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				api.TargetAnyRemote: read, "w-004": read, "w-100": read, "a-local": read,
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestAccRepositoriesWalkVisitsEachOnce: following the Link from the first
// window visits every repository the caller sees exactly once, in the
// order of the whole list, with X-Total-Count on every window equal to
// that list's length, for an administrator, a partial reader and
// anonymous, with and without filters.
func TestAccRepositoriesWalkVisitsEachOnce(t *testing.T) {
	w := walkWorld(t)
	const list = "/artifactory/api/repositories"
	for _, c := range []struct {
		as, filter string
		limit      int
		min        int
	}{
		{"admin", "", 7, 132},
		{"admin", "type=remote&", 5, 30},
		{"admin", "packageType=generic&", 1000, 100},
		{"user", "", 4, 33},
		{"user", "type=local&", 1, 2},
		{"", "", 3, 31},
	} {
		whole := listOf(t, w.do(http.MethodGet, list+"?"+c.filter, c.as, ""))
		if whole.status != http.StatusOK || len(whole.keys) < c.min {
			t.Fatalf("%q %q whole list: %+v", c.as, c.filter, whole)
		}
		var visited []string
		next := fmt.Sprintf("%s?%slimit=%d", list, c.filter, c.limit)
		for pages := 0; next != ""; pages++ {
			if pages > len(whole.keys) {
				t.Fatalf("%q %q: the walk does not end", c.as, c.filter)
			}
			rec := w.do(http.MethodGet, next, c.as, "")
			got := listOf(t, rec)
			if got.status != http.StatusOK || got.total != fmt.Sprint(len(whole.keys)) ||
				len(got.keys) > c.limit {
				t.Fatalf("%q %s: %+v", c.as, next, got)
			}
			visited = append(visited, got.keys...)
			next = linkTarget(t, rec)
		}
		if !slices.Equal(visited, whole.keys) {
			t.Errorf("%q %q: walk visited %v, want %v", c.as, c.filter, visited, whole.keys)
		}
	}
}
