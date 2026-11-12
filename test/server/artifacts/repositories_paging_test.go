package artifacts_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// Get Repositories pages its list with limit and offset, as JFrog's
// getrepositoriesbytypeandproject page documents for 7.173 (#1367).

// pagedList is what one list answer says: its status, the keys of its body
// and the three paging headers, "" where a header is absent.
type pagedList struct {
	status               int
	keys                 []string
	total, offset, limit string
}

func listOf(t *testing.T, rec *httptest.ResponseRecorder) pagedList {
	t.Helper()
	got := pagedList{
		status: rec.Code,
		total:  rec.Header().Get("X-Total-Count"),
		offset: rec.Header().Get("X-Result-Offset"),
		limit:  rec.Header().Get("X-Result-Limit"),
	}
	if rec.Code != http.StatusOK {
		return got
	}
	var body []struct{ Key string }
	jsonBody(t, rec, &body)
	got.keys = []string{}
	for _, e := range body {
		got.keys = append(got.keys, e.Key)
	}
	return got
}

func (l pagedList) equal(o pagedList) bool {
	return l.status == o.status && slices.Equal(l.keys, o.keys) &&
		l.total == o.total && l.offset == o.offset && l.limit == o.limit
}

// pagingWorld is the item world, with anonymous access when anonymous is
// set, and three repositories more: thirteen in all, which an
// administrator sees ordered by key.
func pagingWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	for _, c := range []repo.Config{
		{Key: "a-local", Class: repo.Local, Description: "first"},
		{Key: "b-proj-local", Class: repo.Local, ProjectKey: "proj"},
		{Key: "c-proj-npm", Class: repo.Local, PackageType: repo.NPM, ProjectKey: "proj"},
	} {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

var everyRepository = []string{
	"a-local", "artifactory-build-info", "b-proj-local", "c-proj-npm", "libs-local", "loop",
	"other-local", "redirect-local", "server-local", "up", "virt", "virt-nodeploy",
}

// TestAccRepositoriesPageTheList: limit=2&offset=1 answers the second and
// third repositories with the three headers, X-Total-Count the whole count.
// Without limit the list is whole and carries no paging header, as before.
func TestAccRepositoriesPageTheList(t *testing.T) {
	w := pagingWorld(t, false)
	rec := w.do(http.MethodGet, "/artifactory/api/repositories?limit=2&offset=1", "admin", "")
	want := `[{"description":"Build Info repository","key":"artifactory-build-info",` +
		`"packageType":"BuildInfo","type":"LOCAL",` +
		`"url":"https://reg.example/artifactory/artifactory-build-info"},` +
		`{"key":"b-proj-local","packageType":"Generic","type":"LOCAL",` +
		`"url":"https://reg.example/artifactory/b-proj-local"}]` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("limit=2&offset=1: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if got := listOf(t, rec); !got.equal(pagedList{
		status: 200, keys: []string{"artifactory-build-info", "b-proj-local"},
		total: "12", offset: "1", limit: "2",
	}) {
		t.Fatalf("limit=2&offset=1 headers: %+v", got)
	}
	for _, c := range []struct {
		query string
		want  pagedList
	}{
		// No limit: the whole list, as every release before 7.173 gives it.
		{"", pagedList{status: 200, keys: everyRepository}},
		// offset defaults to 0 when limit is sent.
		{"?limit=2", pagedList{
			status: 200, keys: everyRepository[:2], total: "12", offset: "0", limit: "2",
		}},
		{"?limit=2&offset=0", pagedList{
			status: 200, keys: everyRepository[:2], total: "12", offset: "0", limit: "2",
		}},
		// The last page is short, and X-Result-Limit stays the limit asked.
		{"?limit=5&offset=10", pagedList{
			status: 200, keys: everyRepository[10:], total: "12", offset: "10", limit: "5",
		}},
		{"?limit=1000", pagedList{
			status: 200, keys: everyRepository, total: "12", offset: "0", limit: "1000",
		}},
		// An offset past the end is an empty page, not an error.
		{"?limit=2&offset=100", pagedList{
			status: 200, keys: []string{}, total: "12", offset: "100", limit: "2",
		}},
	} {
		rec := w.do(http.MethodGet, "/artifactory/api/repositories"+c.query, "admin", "")
		if got := listOf(t, rec); !got.equal(c.want) {
			t.Errorf("%q: %+v, want %+v", c.query, got, c.want)
		}
	}
	if rec := w.do(
		http.MethodGet, "/artifactory/api/repositories?limit=2&offset=100", "admin", "",
	); rec.Body.String() != "[]\n" {
		t.Errorf("empty page body %q", rec.Body)
	}
}

// TestAccRepositoriesPageRefusesBadParameters: the 400s JFrog documents,
// with its messages, and the paging headers absent. A caller that must
// authenticate first is still told so.
func TestAccRepositoriesPageRefusesBadParameters(t *testing.T) {
	w := pagingWorld(t, false)
	const (
		needsLimit  = "offset requires limit"
		limitRange  = "limit must be between 1 and 1000"
		offsetRange = "offset must be greater than or equal to 0"
	)
	for _, c := range []struct{ query, message string }{
		{"?offset=1", needsLimit},
		{"?offset=0", needsLimit},
		{"?offset=-1", needsLimit},
		{"?limit=0", limitRange},
		{"?limit=1001", limitRange},
		{"?limit=-1", limitRange},
		{"?limit=1001&offset=1", limitRange},
		{"?limit=0&offset=-1", limitRange},
		{"?limit=1&offset=-1", offsetRange},
		{"?type=local&limit=0", limitRange},
		// A value that is not a number fails the binding of the integer
		// parameter, as any other integer query parameter here does.
		{"?limit=abc", `Invalid format for parameter limit: error binding string parameter: ` +
			`strconv.ParseInt: parsing \"abc\": invalid syntax`},
		{"?limit=", `Invalid format for parameter limit: error binding string parameter: ` +
			`strconv.ParseInt: parsing \"\": invalid syntax`},
		{"?limit=1&offset=x", `Invalid format for parameter offset: error binding string parameter: ` +
			`strconv.ParseInt: parsing \"x\": invalid syntax`},
	} {
		for _, as := range []string{"admin", "user"} {
			rec := w.do(http.MethodGet, "/artifactory/api/repositories"+c.query, as, "")
			want := `{"errors":[{"message":"` + c.message + `","status":400}]}` + "\n"
			if rec.Code != http.StatusBadRequest || rec.Body.String() != want ||
				rec.Header().Get("X-Total-Count") != "" {
				t.Errorf("%s %q: %d %s", as, c.query, rec.Code, rec.Body)
			}
		}
	}
	rec := w.do(http.MethodGet, "/artifactory/api/repositories?offset=1", "", "")
	if rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "Authentication is required") {
		t.Fatalf("anonymous without anonymous access: %d %s", rec.Code, rec.Body)
	}
}

// TestAccRepositoriesPageCountsWhatTheCallerSees: JFrog's page says the
// server "still enumerates the full authorized filtered set, then returns a
// slice", so X-Total-Count counts the repositories the caller may see, and
// the filters narrow the list before it is paged.
func TestAccRepositoriesPageCountsWhatTheCallerSees(t *testing.T) {
	w := pagingWorld(t, true)
	for _, c := range []struct {
		as, query string
		want      pagedList
	}{
		{"reader", "?limit=2&offset=1", pagedList{
			status: 200, keys: everyRepository[1:3], total: "12", offset: "1", limit: "2",
		}},
		// deployer may read libs-local alone.
		{"deployer", "?limit=1", pagedList{
			status: 200, keys: []string{"libs-local"}, total: "1", offset: "0", limit: "1",
		}},
		{"deployer", "?limit=2&offset=1", pagedList{
			status: 200, keys: []string{}, total: "1", offset: "1", limit: "2",
		}},
		{"deployer", "", pagedList{status: 200, keys: []string{"libs-local"}}},
		{"user", "?limit=5", pagedList{
			status: 200, keys: []string{}, total: "0", offset: "0", limit: "5",
		}},
		// Anonymous reads the remote through its own permission.
		{"", "?limit=2", pagedList{
			status: 200, keys: []string{"up"}, total: "1", offset: "0", limit: "2",
		}},
		{"", "", pagedList{status: 200, keys: []string{"up"}}},
		{"admin", "?type=local&limit=2&offset=1", pagedList{
			status: 200, keys: []string{"artifactory-build-info", "b-proj-local"},
			total: "8", offset: "1", limit: "2",
		}},
		{"admin", "?packageType=npm&limit=5", pagedList{
			status: 200, keys: []string{"c-proj-npm"}, total: "1", offset: "0", limit: "5",
		}},
		{"admin", "?project=proj&limit=1&offset=1", pagedList{
			status: 200, keys: []string{"c-proj-npm"}, total: "2", offset: "1", limit: "1",
		}},
		{"admin", "?project=proj", pagedList{
			status: 200, keys: []string{"b-proj-local", "c-proj-npm"},
		}},
		// No distribution repository is served, so its page is empty too.
		{"admin", "?type=distribution&limit=5", pagedList{
			status: 200, keys: []string{}, total: "0", offset: "0", limit: "5",
		}},
		{"admin", "?type=distribution", pagedList{status: 200, keys: []string{}}},
	} {
		rec := w.do(http.MethodGet, "/artifactory/api/repositories"+c.query, c.as, "")
		if got := listOf(t, rec); !got.equal(c.want) {
			t.Errorf("%q %q: %+v, want %+v", c.as, c.query, got, c.want)
		}
	}
	// The vendor list media type pages the same and carries the headers.
	rec := w.do(http.MethodGet, "/artifactory/api/repositories?limit=2&offset=1", "admin", "",
		"Accept", mediaDetails)
	if got := listOf(t, rec); rec.Header().Get("Content-Type") != mediaDetails ||
		!got.equal(pagedList{
			status: 200, keys: everyRepository[1:3], total: "12", offset: "1", limit: "2",
		}) {
		t.Fatalf("vendor list: %s %+v", rec.Header().Get("Content-Type"), got)
	}
}
