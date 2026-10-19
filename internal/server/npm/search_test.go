package npm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// search asks npm search's question as user and answers the names found.
func (f *fixture) search(t *testing.T, query, user string) []string {
	t.Helper()
	rec := f.do("GET", registry+"-/v1/search?"+query, user)
	wantStatus(t, rec, http.StatusOK)
	objects, ok := decode(t, rec)["objects"].([]any)
	if !ok {
		t.Fatalf("%s: no objects: %s", query, rec.Body)
	}
	names := []string{}
	for _, o := range objects {
		names = append(names, o.(map[string]any)["package"].(map[string]any)["name"].(string))
	}
	return names
}

// deploySearchable deploys rich and three packages more: widget-kit and
// @acme/widget with keywords, and plain, whose package.json has no
// description, keywords or author.
func (f *fixture) deploySearchable(t *testing.T) {
	t.Helper()
	f.deployRich(t)
	f.deploy(t, "widget-kit", "1.0.0", `{"name":"widget-kit","version":"1.0.0",`+
		`"description":"Builds widgets","keywords":["Widget","ui"],"author":"Bo"}`, nil)
	f.deploy(t, "@acme/widget", "0.1.0", `{"name":"@acme/widget","version":"0.1.0",`+
		`"keywords":"tools, widget"}`, nil)
	f.deploy(t, "plain", "3.0.0", `{"name":"plain","version":"3.0.0"}`, nil)
}

// The body has 7.x's shape (RTFACT-26660): objects alone, each package
// with name, description, maintainers, version, date, keywords and author,
// and no score, scope, links, total or time.
func TestSearchBody(t *testing.T) {
	f := newFixture(t)
	f.deploySearchable(t)
	rec := f.do("GET", registry+"-/v1/search?text=widget&size=20&from=0&quality=0.65", "reader")
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	want := map[string]any{"objects": []any{
		map[string]any{"package": map[string]any{
			"name": "@acme/widget", "description": nil, "maintainers": []any{},
			"version": "0.1.0", "date": "2026-10-01T09:05:00.000Z",
			"keywords": []any{"tools", "widget"}, "author": nil,
		}},
		map[string]any{"package": map[string]any{
			"name": "widget-kit", "description": "Builds widgets", "maintainers": []any{},
			"version": "1.0.0", "date": "2026-10-01T09:04:00.000Z",
			"keywords": []any{"Widget", "ui"}, "author": "Bo",
		}},
	}}
	if diff := cmp.Diff(want, decode(t, rec)); diff != "" {
		t.Errorf("search (-want +got):\n%s", diff)
	}
	// The package's latest version answers: rich's is 1.1.0, whose author
	// is an object, as its package.json has it.
	rec = f.do("GET", registry+"-/v1/search?text=second", "reader")
	got := decode(t, rec)["objects"].([]any)[0].(map[string]any)["package"].(map[string]any)
	if got["version"] != "1.1.0" || !cmp.Equal(got["author"], annAuthor) ||
		got["description"] != "Second" {
		t.Errorf("rich: %v", got)
	}
	// No package.json field, no value: null and empty lists.
	rec = f.do("GET", registry+"-/v1/search?text=plain", "reader")
	got = decode(t, rec)["objects"].([]any)[0].(map[string]any)["package"].(map[string]any)
	if got["description"] != nil || got["author"] != nil ||
		!cmp.Equal(got["keywords"], []any{}) || !cmp.Equal(got["maintainers"], []any{}) {
		t.Errorf("plain: %v", got)
	}
}

// Every term of the text, up to three, must appear in the latest
// version's name, description or keywords, ignoring case.
func TestSearchTerms(t *testing.T) {
	f := newFixture(t)
	f.deploySearchable(t)
	all := []string{"@acme/widget", "plain", "rich", "widget-kit"}
	for _, c := range []struct {
		text string
		want []string
	}{
		{"", all},
		{"widget", []string{"@acme/widget", "widget-kit"}},
		{"WIDGET", []string{"@acme/widget", "widget-kit"}},
		{"acme", []string{"@acme/widget"}},
		{"tools", []string{"@acme/widget"}},
		{"widget ui", []string{"widget-kit"}},
		{"widget+builds", []string{"widget-kit"}},
		// A fourth term is not read.
		{"widget ui builds absent", []string{"widget-kit"}},
		{"widget ui absent builds", []string{}},
		// Only the latest version's package.json counts: rich 1.0.0's
		// description was "First <b>rich</b>".
		{"first", []string{}},
		{"second", []string{"rich"}},
		{"nothing-like-it", []string{}},
	} {
		got := f.search(t, "text="+strings.ReplaceAll(c.text, " ", "%20"), "reader")
		if !slices.Equal(got, c.want) {
			t.Errorf("text %q: %v, want %v", c.text, got, c.want)
		}
	}
}

// Results are in name order; from skips and size caps them. A from or
// size that is no number, or not positive, takes its default.
func TestSearchPaging(t *testing.T) {
	f := newFixture(t)
	f.deploySearchable(t)
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"size=2", []string{"@acme/widget", "plain"}},
		{"size=2&from=1", []string{"plain", "rich"}},
		{"size=2&from=3", []string{"widget-kit"}},
		{"from=4", []string{}},
		{"size=0&from=-1", []string{"@acme/widget", "plain", "rich", "widget-kit"}},
		{"size=many&from=some", []string{"@acme/widget", "plain", "rich", "widget-kit"}},
		{"size=1000", []string{"@acme/widget", "plain", "rich", "widget-kit"}},
		{"text=widget&from=1", []string{"widget-kit"}},
	} {
		if got := f.search(t, c.query, "reader"); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.query, got, c.want)
		}
	}
}

// A search reads more than one page of the index when the repository has
// more packages than a page holds.
func TestSearchReadsEveryPage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 501
	for i := range n {
		if _, err := f.db.Packages().Ensure(ctx, types.Package{
			RepoKey: "npm-local", Name: fmt.Sprintf("bulk-%03d", i), Type: "npm",
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.deploy(t, "zz-last", "1.0.0", `{"name":"zz-last","version":"1.0.0"}`, nil)
	if got := f.search(t, "text=zz", "reader"); !slices.Equal(got, []string{"zz-last"}) {
		t.Errorf("past the first page: %v", got)
	}
}

// A package is matched and shown as its caller sees it: by its latest
// among the versions the caller may read. A package the caller may read
// no version of is left out, and so is everything for a caller who may
// read nothing.
func TestSearchHidesUnreadable(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "mixed", "1.0.0", `{"name":"mixed","version":"1.0.0","description":"old"}`, nil)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0","description":"new"}`, nil)
	f.deploy(t, "hidden", "1.0.0", `{"name":"hidden","version":"1.0.0"}`, nil)
	if got := f.search(t, "", "reader"); !slices.Equal(got, []string{"hidden", "mixed"}) {
		t.Errorf("reader: %v", got)
	}
	if got := f.search(t, "", "partial"); !slices.Equal(got, []string{"mixed"}) {
		t.Errorf("partial: %v", got)
	}
	if got := f.search(t, "text=new", "partial"); len(got) != 0 {
		t.Errorf("partial matched a version it may not read: %v", got)
	}
	rec := f.do("GET", registry+"-/v1/search?text=old", "partial")
	pkg := decode(t, rec)["objects"].([]any)[0].(map[string]any)["package"].(map[string]any)
	if pkg["version"] != "1.0.0" {
		t.Errorf("partial's mixed: %v", pkg)
	}
	for _, user := range []string{"nobody", "anonymous"} {
		if got := f.search(t, "", user); len(got) != 0 {
			t.Errorf("%s: %v", user, got)
		}
	}
}

// A store that fails, or a version document the index cannot decode, is
// a fault: 500, and the cause goes to the access log.
func TestSearchFaults(t *testing.T) {
	for _, failing := range []string{"list", "find", "versions"} {
		f := newFixture(t, func(st store.Store) store.Store {
			return listFailing{failingStore{Store: st, failing: failing}}
		})
		f.deployRich(t)
		rec := f.do("GET", registry+"-/v1/search?text=rich", "reader")
		wantStatus(t, rec, http.StatusInternalServerError)
		if len(f.faults) != 1 || !errors.Is(f.faults[0], errBroken) {
			t.Errorf("%s: faults %v", failing, f.faults)
		}
	}
	f := newFixture(t)
	f.deploy(t, "broken", "1.0.0", `{"name":"broken","version":"1.0.0"}`, nil)
	ctx := context.Background()
	p, err := f.db.Packages().Find(ctx, "npm-local", "broken")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(ctx, p.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range []string{`not json`, `{"manifest":[1],"dist":{}}`} {
		v.Metadata = []byte(meta)
		if _, err := f.db.Packages().PutVersion(ctx, v); err != nil {
			t.Fatal(err)
		}
		f.faults = nil
		wantStatus(t, f.do("GET", registry+"-/v1/search", "reader"), http.StatusInternalServerError)
		if len(f.faults) != 1 {
			t.Errorf("metadata %s: faults %v", meta, f.faults)
		}
	}
	// Remote repositories are P9's.
	wantStatus(t, f.do("GET", "/artifactory/api/npm/npm-remote/-/v1/search", "reader"),
		http.StatusNotImplemented)
}

// listFailing is failingStore whose package listing fails too when it is
// told to fail "list".
type listFailing struct{ failingStore }

func (s listFailing) Packages() store.PackageStore {
	return listFailingPackages{PackageStore: s.failingStore.Packages(), fail: s.failing == "list"}
}

type listFailingPackages struct {
	store.PackageStore
	fail bool
}

func (p listFailingPackages) List(
	ctx context.Context, key string, pg store.Page,
) ([]types.Package, string, error) {
	if p.fail {
		return nil, "", errBroken
	}
	return p.PackageStore.List(ctx, key, pg) //nolint:wrapcheck // a test's pass-through
}
