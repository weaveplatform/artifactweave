package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// compactAQL runs query with ?compact=true and returns the status and the
// body.
func compactAQL(
	t *testing.T,
	h http.Handler,
	as map[string]string,
	query string,
	headers ...string,
) (
	int, string,
) {
	t.Helper()
	all := map[string]string{}
	for k, v := range as {
		all[k] = v
	}
	for i := 0; i+1 < len(headers); i += 2 {
		all[headers[i]] = headers[i+1]
	}
	rec := do(t, h, http.MethodPost, aqlURL+"?compact=true", all, query)
	return rec.Code, rec.Body.String()
}

// TestSearchAQLRelatedIncludes: an include of a related domain's fields
// nests the related rows the criteria allow (#410), and a caller who may
// not read a run or an item does not see it nested.
func TestSearchAQLRelatedIncludes(t *testing.T) {
	h, tokens := buildsAQLWorld(t)
	admin := basic("admin", "pw")
	for query, want := range map[string]string{
		`items.find({"name":"app.jar"})` +
			`.include("name","artifact.module.build.name","artifact.module.build.number")`: `[{"name":"app.jar",` +
			`"artifacts":[{"modules":[{"builds":[{"build.name":"app","build.number":"1"},` +
			`{"build.name":"app","build.number":"2"},{"build.name":"tool","build.number":"1"}]}]}]}]`,
		`items.find({"name":"app.jar"},{"artifact.module.build.name":"tool"})` +
			`.include("name","artifact.module.build.*")`: `[{"name":"app.jar","artifacts":[{"modules":[{` +
			`"builds":[{"build.created":"2026-01-01T01:00:00.000Z","build.created_by":"ci",` +
			`"build.name":"tool","build.number":"1","build.repo":"artifactory-build-info",` +
			`"build.started":"2026-01-01T00:02:00.000Z","build.url":"https://ci/tool"}]}]}]}]`,
		`builds.find({"name":"tool"}).include("name","module.artifact.item.repo")`: `[{` +
			`"build.name":"tool","modules":[{"artifacts":[{"items":[{"repo":"libs-local"}]}]}]}]`,
		`build.promotions.find().include("status","build.number")`: `[{"build.promotion.status":` +
			`"released","builds":[{"build.number":"1"}]}]`,
	} {
		code, body := compactAQL(t, h, admin, query)
		if code != http.StatusOK || !strings.HasPrefix(body, `{"results":`+want+`,`) {
			t.Errorf("%s: %d\n%s\nwant results %s", query, code, body, want)
		}
	}
	// dev reads the runs under app/** only, so tool/1 is not nested.
	code, body := compactAQL(t, h, basic("dev", "pw"),
		`items.find({"name":"app.jar"}).include("repo","path","name","artifact.module.build.name")`)
	if want := `"artifacts":[{"modules":[{"builds":[{"build.name":"app"}]}]}]`; code != http.StatusOK ||
		!strings.Contains(body, want) {
		t.Fatalf("dev's nested runs %d %s", code, body)
	}
	scoped := bearerFor(t, tokens, "build:artifactory-build-info/**:r")
	code, body = compactAQL(t, h, scoped,
		`builds.find({"name":"app"}).include("name","number","repo","promotion.status")`)
	if code != http.StatusOK || strings.Count(body, `"build.promotions":`) != 1 {
		t.Fatalf("scoped promotions %d %s", code, body)
	}
	// A related domain without properties or statistics has none to include.
	code, body = compactAQL(t, h, admin, `items.find().include("artifact.module.build.stat")`)
	if code != http.StatusBadRequest || !strings.Contains(body, "no field, property or statistic") {
		t.Fatalf("statistics of a run %d %s", code, body)
	}
}

// TestSearchAQLRelatedProperties: an include of a related row's properties
// or statistics, or of a run's own properties, nests them (#494), and a
// caller sees only those of the runs and items it may read, as it sees
// only those rows.
func TestSearchAQLRelatedProperties(t *testing.T) {
	ctx := context.Background()
	h, tokens := buildsAQLWorld(t, func(c *Config) {
		db := c.Store
		jar, err := db.Items().Get(ctx, "libs-local", false, "app.jar")
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		if err := db.Items().SetProperties(ctx, jar.ID, types.Properties{"team": {"core"}},
			at); err != nil {
			t.Fatal(err)
		}
		if err := db.Items().
			RecordDownload(ctx, jar.ID, store.Download{By: "dev", At: at}); err != nil {
			t.Fatal(err)
		}
		// dev reads the runs under app/** only: app/3's properties, not
		// tool/2's.
		file := types.BuildFile{Name: "app.jar", Checksums: types.Checksums{SHA1: jar.Actual.SHA1}}
		for _, name := range []string{"app", "tool"} {
			if _, err := db.Builds().Put(ctx, types.Build{
				Name: name, Number: "9", Started: at, Created: at, CreatedBy: "ci",
				Properties: types.Properties{"os": {name + "-os"}},
				Modules: []types.BuildModule{{
					ID: name, Properties: types.Properties{"java": {"17"}},
					Artifacts: []types.BuildFile{file},
				}},
			}); err != nil {
				t.Fatal(err)
			}
		}
	})
	runProps := func(values ...string) string {
		var rows []string
		for _, v := range values {
			rows = append(rows, `{"build.property.key":"os","build.property.value":"`+v+`"}`)
		}
		return `"build.properties":[` + strings.Join(rows, ",") + `]`
	}
	// The modules fold into one row; the whole property domain keeps each
	// module's java apart, so the admin sees one per run.
	items := `items.find({"name":"app.jar"}).include("repo","path","name","artifact.module.build.@os",` +
		`"artifact.module.property.*")`
	java := `{"module.property.key":"java","module.property.value":"17"}`
	for _, c := range []struct {
		name string
		as   map[string]string
		want string
	}{
		{"admin", basic("admin", "pw"), `"artifacts":[{"modules":[{"builds":[{` +
			runProps("app-os", "tool-os") + `}],"module.properties":[` + java + `,` + java + `]}]}]`},
		{"dev", basic("dev", "pw"), `"artifacts":[{"modules":[{"builds":[{` +
			runProps("app-os") + `}],"module.properties":[` + java + `]}]}]`},
	} {
		code, body := compactAQL(t, h, c.as, items)
		if code != http.StatusOK || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s\nwant %s", c.name, code, body, c.want)
		}
	}
	// A build-scoped token reaches no artifact, so the run's items, and
	// their properties and statistics, are not nested for it.
	builds := `builds.find({"name":"app","number":"9"}).include("name","number","repo","@os",` +
		`"module.artifact.item.@team","module.artifact.item.stat.downloads")`
	head := `{"results":[{"build.name":"app","build.number":"9","build.repo":"artifactory-build-info",` +
		runProps(
			"app-os",
		)
	for _, c := range []struct {
		name string
		as   map[string]string
		want string
	}{
		{"admin", basic("admin", "pw"), head + `,"modules":[{"artifacts":[{"items":[{` +
			`"properties":[{"key":"team","value":"core"}],"stats":[{"downloads":1}]}]}]}]}]`},
		{"scoped", bearerFor(t, tokens, "build:artifactory-build-info/**:r"), head + `}]`},
	} {
		code, body := compactAQL(t, h, c.as, builds)
		if code != http.StatusOK || !strings.HasPrefix(body, c.want+`,"range":`) {
			t.Errorf("%s: %d %s\nwant %s", c.name, code, body, c.want)
		}
	}
}

// TestSearchAQLEveryKey: include("@") and include("@*") return every
// property, as property.* does, of an item and of a related row, and an
// item's own statistic its record lacks renders no empty stats row (#506).
func TestSearchAQLEveryKey(t *testing.T) {
	ctx := context.Background()
	h, _ := buildsAQLWorld(t, func(c *Config) {
		jar, err := c.Store.Items().Get(ctx, "libs-local", false, "app.jar")
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		if err := c.Store.Items().SetProperties(ctx, jar.ID,
			types.Properties{"team": {"core"}, "tier": {"1"}}, at); err != nil {
			t.Fatal(err)
		}
		if err := c.Store.Items().
			RecordDownload(ctx, jar.ID, store.Download{By: "dev", At: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Store.Builds().Put(ctx, types.Build{
			Name: "app", Number: "9", Started: at, Created: at, CreatedBy: "ci",
			Properties: types.Properties{"os": {"linux"}},
			Modules: []types.BuildModule{{ID: "app", Artifacts: []types.BuildFile{
				{Name: "app.jar", Checksums: types.Checksums{SHA1: jar.Actual.SHA1}},
			}}},
		}); err != nil {
			t.Fatal(err)
		}
	})
	props := `"properties":[{"key":"team","value":"core"},{"key":"tier","value":"1"}]`
	for query, want := range map[string]string{
		`items.find({"name":"app.jar"}).include("name","@")`:  `[{"name":"app.jar",` + props + `}]`,
		`items.find({"name":"app.jar"}).include("name","@*")`: `[{"name":"app.jar",` + props + `}]`,
		// The "Sorting in AQL" example's form.
		`items.find({"name":{"$match":"*.jar"}}).include("@").sort({"$asc":["repo","name"]})`: `[{` +
			`"repo":"libs-local"`,
		`items.find({"name":"app.jar"}).include("name","stat.remote_downloaded_by")`: `[{"name":"app.jar"}]`,
		`items.find({"name":"app.jar"}).include("name","artifact.module.build.@*")`: `[{"name":"app.jar",` +
			`"artifacts":[{"modules":[{"builds":[{"build.properties":[` +
			`{"build.property.key":"os","build.property.value":"linux"}]}]}]}]}]`,
	} {
		code, body := compactAQL(t, h, basic("admin", "pw"), query)
		if code != http.StatusOK || !strings.HasPrefix(body, `{"results":`+want) {
			t.Errorf("%s: %d\n%s\nwant results %s", query, code, body, want)
		}
	}
}

// TestSearchAQLTimeout: a query that outlasts the configured timeout is
// cancelled and answered with 408 (#411), the streamed timeout applying to
// a stream: true request.
func TestSearchAQLTimeout(t *testing.T) {
	h, _ := buildsAQLWorld(t, func(c *Config) {
		c.AQL.Timeout = time.Nanosecond
		c.Store = deadlineStore{c.Store}
	})
	admin := basic("admin", "pw")
	for _, query := range []string{`items.find()`, `builds.find()`} {
		code, body := compactAQL(t, h, admin, query)
		if code != http.StatusRequestTimeout ||
			!strings.Contains(body, "exceeded its timeout of 0.000000001 seconds") {
			t.Errorf("%s: %d %s", query, code, body)
		}
		// A streamed query has its own timeout, here none.
		if code, body := compactAQL(t, h, admin, query, "stream", "true"); code != http.StatusOK {
			t.Errorf("%s streamed: %d %s", query, code, body)
		}
	}
	h, _ = buildsAQLWorld(t, func(c *Config) {
		c.AQL.StreamTimeout = time.Nanosecond
		c.Store = deadlineStore{c.Store}
	})
	if code, _ := compactAQL(t, h, admin, `items.find()`); code != http.StatusOK {
		t.Fatalf("unstreamed query: %d", code)
	}
	if code, _ := compactAQL(t, h, admin, `items.find()`, "stream", "true"); code !=
		http.StatusRequestTimeout {
		t.Fatalf("streamed query: %d", code)
	}
	// The nesting of related rows fails the same ways: on the timeout, and
	// on an include that reaches too many related rows.
	s := artifactoryService{}
	ctx := context.Background()
	var api *apierror.Error
	late := context.DeadlineExceeded
	fail := s.aqlFailure(ctx, late, time.Second, "nest", late)
	if !errors.As(fail, &api) || api.Status != http.StatusRequestTimeout ||
		api.Message != "The AQL query exceeded its timeout of 1 seconds and was cancelled" {
		t.Fatalf("nest timeout: %v", fail)
	}
	fail = s.aqlFailure(ctx, nil, 0, "nest", fmt.Errorf("%w: too many", aql.ErrInvalid))
	if !errors.As(fail, &api) || api.Status != http.StatusBadRequest {
		t.Fatalf("an include over the budget: %v", fail)
	}
	// A client that went away is no timeout.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	fail = s.aqlFailure(gone, context.DeadlineExceeded, time.Second, "nest", context.Canceled)
	if errors.As(fail, &api) && api.Status == http.StatusRequestTimeout {
		t.Fatalf("a cancelled request: %v", fail)
	}
}

// TestSearchAQLRoots: every repository's root is an item row, a folder
// with path and name "." (#445); items.delete passes it over.
func TestSearchAQLRoots(t *testing.T) {
	h, _ := buildsAQLWorld(t)
	const root = `{"repo":"libs-local","path":".","name":".","type":"folder","size":0}`
	for _, as := range []map[string]string{basic("admin", "pw"), basic("dev", "pw")} {
		code, body := compactAQL(t, h, as,
			`items.find({"type":"folder","path":"."}).include("repo","path","name","type","size")`)
		if code != http.StatusOK || !strings.Contains(body, root) {
			t.Fatalf("roots %d %s", code, body)
		}
	}
	code, body := compactAQL(t, h, basic("admin", "pw"),
		`items.delete({"repo":"libs-local","type":"any"}).dryRun("false")`)
	if code != http.StatusOK || strings.Contains(body, `"name":"."`) ||
		!strings.Contains(body, `"name":"app.jar"`) {
		t.Fatalf("delete %d %s", code, body)
	}
}

// TestSearchAQLGrammar covers 7.x's forms over HTTP (#410): several
// criteria objects, the long relative-time units, item.infos and the
// length limit's message.
func TestSearchAQLGrammar(t *testing.T) {
	h, _ := buildsAQLWorld(t)
	admin := basic("admin", "pw")
	code, body := compactAQL(t, h, admin,
		`items.find({"repo":"libs-local"},{"created":{"$last":"3days"}}).include("name")`)
	if code != http.StatusOK || !strings.HasPrefix(body, `{"results":[{"name":"app.jar"}]`) {
		t.Fatalf("criteria list %d %s", code, body)
	}
	code, body = compactAQL(t, h, admin, `item.infos.find({"item_id":1})`)
	if code != http.StatusBadRequest || !strings.Contains(body, "item.infos domain") {
		t.Fatalf("item.infos %d %s", code, body)
	}
	code, body = compactAQL(
		t,
		h,
		admin,
		`items.find({"name":"`+strings.Repeat("a", aql.MaxLength)+`"})`,
	)
	if want := "AQL query is too long; please reduce the query length to less than 6000 chars"; code !=
		http.StatusBadRequest ||
		!strings.Contains(body, `"message":"`+want+`"`) {
		t.Fatalf("too long %d %s", code, body)
	}
}

// deadlineStore makes an AQL search that carries a deadline wait for it, so
// the timeout tests do not race the search. Go cancels a context at once only
// when its deadline has already passed as it is made; Windows' clock is too
// coarse for a one-nanosecond deadline to have passed, and a fast in-memory
// search then finishes before the timer fires.
type deadlineStore struct{ store.Store }

func (s deadlineStore) Items() store.ItemStore   { return deadlineItems{s.Store.Items()} }
func (s deadlineStore) Builds() store.BuildStore { return deadlineBuilds{s.Store.Builds()} }

type deadlineItems struct{ store.ItemStore }

func (d deadlineItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if _, ok := ctx.Deadline(); ok {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.ItemStore.Search(ctx, q, o) //nolint:wrapcheck // the store's own error
}

type deadlineBuilds struct{ store.BuildStore }

func (d deadlineBuilds) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.BuildHit, error) {
	if _, ok := ctx.Deadline(); ok {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.BuildStore.Search(ctx, q, o) //nolint:wrapcheck // the store's own error
}
