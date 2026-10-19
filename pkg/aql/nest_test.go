package aql_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/aql/aqltest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// rows renders a response's results compactly.
func rows(t *testing.T, res aql.Response) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return string(doc.Results)
}

// nestItems runs an items query over the shared corpus and nests its
// related includes from the corpus as a Source.
func nestItems(t *testing.T, src string, opt aql.Nesting) (string, error) {
	t.Helper()
	q, err := aql.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	q.Resolve(aqltest.Repositories())
	corpus := aqltest.Corpus()
	// The server reads the hits with every property and statistic, which
	// the relation-path criteria may need.
	hits := aql.SelectItems(q.ForNesting(), aql.Options{Now: aqltest.Now}, corpus)
	opt.Now = aqltest.Now
	rel, err := q.Nest(context.Background(), aqltest.Corpus(), opt, hits)
	return rows(t, q.Response(hits).WithRelated(rel)), err
}

func nestBuilds(t *testing.T, src string, opt aql.Nesting) (string, error) {
	t.Helper()
	q, err := aql.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	corpus := aqltest.Corpus()
	hits := aql.SelectBuilds(q, aql.Options{Now: aqltest.Now}, corpus)
	rel, err := q.NestBuilds(context.Background(), corpus, opt, hits)
	return rows(t, q.BuildResponse(hits).WithRelated(rel)), err
}

// TestNest covers the includes of related domains (#410, research 22
// §1.2) over aqltest's builds: the jar is an artifact of app/1 and app/2,
// the cache's jar of the rerun of app/1, naïve.txt of app/2 and tool/1.
func TestNest(t *testing.T) {
	f32, f40 := strings.Repeat("f", 32), strings.Repeat("f", 40)
	hide2 := aql.Nesting{RunVisible: func(b types.Build) bool { return b.ID != 2 }}
	hideNaive := aql.Nesting{
		ItemVisible: func(it types.Item) bool { return it.Name != "naïve.txt" },
	}
	for _, c := range []struct {
		name, query string
		opt         aql.Nesting
		want        string
	}{
		{
			// Rows with no included field fold into one; runs differ by the
			// values of their included fields.
			"fields of a path", `items.find({"name":"app-1.0.jar"})` +
				`.include("name","artifact.module.build.name","artifact.module.build.number")`,
			aql.Nesting{},
			`[{"name":"app-1.0.jar","artifacts":[{"modules":[{"builds":[` +
				`{"build.name":"app","build.number":"1"},{"build.name":"app","build.number":"2"}]}]}]},` +
				`{"name":"app-1.0.jar","artifacts":[{"modules":[{"builds":[` +
				`{"build.name":"app","build.number":"1"}]}]}]}]`,
		},
		{
			// The criteria and the include share the path's rows.
			"criteria narrow the nested rows",
			`items.find({"name":"app-1.0.jar","artifact.module.build.number":"2"})` +
				`.include("name","artifact.module.build.*")`,
			aql.Nesting{},
			`[{"name":"app-1.0.jar","artifacts":[{"modules":[{"builds":[{` +
				`"build.created":"2026-01-01T00:21:00.000Z","build.created_by":"ci",` +
				`"build.modified":"2026-01-01T00:25:00.000Z","build.modified_by":"admin",` +
				`"build.name":"app","build.number":"2","build.repo":"artifactory-build-info",` +
				`"build.started":"2026-01-01T00:20:00.000Z","build.url":"https://ci/app/2"}]}]}]}]`,
		},
		{
			"a whole domain", `items.find({"name":"app-1.0.pom"}).include("name","artifact")`,
			aql.Nesting{},
			`[{"name":"app-1.0.pom","artifacts":[{"artifact.md5":"` + f32 + `",` +
				`"artifact.name":"app-1.0.pom","artifact.sha1":"` + f40 + `","artifact.type":"pom"}]}]`,
		},
		{
			// readme.md meets the $or without a run and nests nothing;
			// naïve.txt is an artifact of app/2 too, but only tool/1 meets it.
			"an alternative on the item",
			`items.find({"$or":[{"name":"readme.md"},{"artifact.module.build.name":"tool"}]})` +
				`.include("name","artifact.module.name")`,
			aql.Nesting{},
			`[{"name":"readme.md"},` +
				`{"name":"a_b.txt","artifacts":[{"modules":[{"module.name":"tool"}]}]},` +
				`{"name":"axb.txt","artifacts":[{"modules":[{"module.name":"tool"}]}]},` +
				`{"name":"naïve.txt","artifacts":[{"modules":[{"module.name":"tool"}]}]}]`,
		},
		{
			"a dependency and its items", `items.find({"name":"app-1.0.jar","repo":"libs-local"})` +
				`.include("name","dependency.name","dependency.scope","dependency.module.artifact.item.repo")`,
			aql.Nesting{},
			`[{"name":"app-1.0.jar","dependencies":[{"dependency.name":"org.acme:app:1.0","modules":[` +
				`{"artifacts":[{"items":[{"repo":"generic-local"}]}]}]}]}]`,
		},
		{
			"a hidden run", `items.find({"name":"app-1.0.jar","repo":"libs-local"})` +
				`.include("name","artifact.module.build.number")`, hide2,
			`[{"name":"app-1.0.jar","artifacts":[{"modules":[{"builds":[{"build.number":"1"}]}]}]}]`,
		},
		{
			// A path that reaches no row leaves its domain out.
			"no related rows", `items.find({"name":"readme.md"}).include("name","artifact")`,
			aql.Nesting{},
			`[{"name":"readme.md"}]`,
		},
		{
			"the default fields stay", `items.find({"name":"axb.txt"}).include("artifact.sha1")`,
			aql.Nesting{},
			`[{"repo":"generic-local","path":"docs","name":"axb.txt","type":"file","size":8,` +
				`"created":"2026-01-01T00:11:00.000Z","modified":"2026-01-01T00:09:00.000Z",` +
				`"updated":"2026-01-01T00:11:00.000Z",` +
				`"artifacts":[{"artifact.sha1":"` + strings.Repeat("l", 40) + `"}]}]`,
		},
		{
			// An item may reach itself again through its artifact.
			"back to items", `items.find({"name":"app-1.0.jar"}).include("repo","artifact.item.repo")`,
			aql.Nesting{},
			`[{"repo":"libs-local","artifacts":[{"items":[{"repo":"libs-local"}]}]},` +
				`{"repo":"remote-cache","artifacts":[{"items":[{"repo":"remote-cache"}]}]}]`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := nestItems(t, c.query, c.opt)
			if err != nil || got != c.want {
				t.Fatalf("got  %s, %v\nwant %s", got, err, c.want)
			}
		})
	}
	for _, c := range []struct {
		name, query string
		opt         aql.Nesting
		want        string
	}{
		{
			// aql-query-output's example: the build's fields and the item's
			// name, the module and artifact rows only carrying them.
			"items of a run", `builds.find({"name":"tool"}).include("name","module.artifact.item.name")`,
			aql.Nesting{},
			`[{"build.name":"tool","modules":[{"artifacts":[{"items":[` +
				`{"name":"a_b.txt"},{"name":"axb.txt"},{"name":"naïve.txt"}]}]}]}]`,
		},
		{
			"a hidden item", `builds.find({"name":"tool"}).include("name","module.artifact.item.name")`,
			hideNaive,
			`[{"build.name":"tool","modules":[{"artifacts":[{"items":[` +
				`{"name":"a_b.txt"},{"name":"axb.txt"}]}]}]}]`,
		},
		{
			"promotions of runs", `builds.find({"name":"app"}).include("number","promotion.status")`,
			aql.Nesting{},
			`[{"build.number":"1","build.promotions":[{"build.promotion.status":"staged"},` +
				`{"build.promotion.status":"released"}]},{"build.number":"2"},{"build.number":"1"}]`,
		},
		{
			"the run of a promotion",
			`build.promotions.find({"status":"released"}).include("status","build.name","build.number")`,
			aql.Nesting{},
			`[{"build.promotion.status":"released","builds":[{"build.name":"app","build.number":"1"}]}]`,
		},
		{
			"all of a run's fields stay", `builds.find({"name":"tool"}).include("module.name")`,
			aql.Nesting{},
			`[{"build.created":"2026-01-01T00:31:00.000Z","build.created_by":"bob",` +
				`"build.name":"tool","build.number":"1","build.repo":"proj-build-info",` +
				`"build.started":"2026-01-01T00:30:00.000Z","modules":[{"module.name":"tool"}]}]`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := nestBuilds(t, c.query, c.opt)
			if err != nil || got != c.want {
				t.Fatalf("got  %s, %v\nwant %s", got, err, c.want)
			}
		})
	}
}

// TestRelatedCases: every shared related-include case nests rows, so the
// store contract suite, which compares each store's rendering with the
// corpus's, compares something.
func TestRelatedCases(t *testing.T) {
	for _, c := range aqltest.RelatedCases() {
		t.Run(c.Name, func(t *testing.T) {
			q, err := c.Parse()
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if q.Domain == aql.DomainItems {
				got, err = nestItems(t, c.Query, aql.Nesting{})
			} else {
				got, err = nestBuilds(t, c.Query, aql.Nesting{})
			}
			if err != nil || !strings.Contains(got, `":[{`) {
				t.Fatalf("nothing nested: %s, %v", got, err)
			}
		})
	}
}

// failingSource fails every load.
type failingSource struct{}

var errLoad = errors.New("load failed")

func (failingSource) RunsWithFile(context.Context, bool, string) ([]types.Build, error) {
	return nil, errLoad
}

func (failingSource) ItemsWithSHA1(context.Context, string) ([]aql.Hit, error) {
	return nil, errLoad
}

func (failingSource) Run(
	context.Context,
	int64,
) (types.Build, error) {
	return types.Build{}, errLoad
}

func (failingSource) RunPromotions(context.Context, int64) ([]types.BuildPromotion, error) {
	return nil, errLoad
}

// TestNestErrors: a load the source fails is the error, and a query
// without related includes nests nothing and loads nothing.
func TestNestErrors(t *testing.T) {
	ctx := context.Background()
	corpus := aqltest.Corpus()
	for _, src := range []string{
		`items.find({"name":"app-1.0.jar"}).include("artifact.name")`,
		`items.find({"name":"app-1.0.jar"}).include("dependency.name")`,
	} {
		q, _ := aql.Parse(src)
		hits := aql.SelectItems(q, aql.Options{}, corpus)
		if _, err := q.Nest(ctx, failingSource{}, aql.Nesting{}, hits); !errors.Is(err, errLoad) {
			t.Errorf("%s: %v", src, err)
		}
	}
	// Items are loaded from files, and runs from promotions.
	for _, src := range []string{
		`builds.find({"name":"tool"}).include("module.artifact.item.name")`,
		`build.promotions.find().include("build.name")`,
		`builds.find({"name":"tool"}).include("promotion.status")`,
	} {
		q, _ := aql.Parse(src)
		hits := aql.SelectBuilds(q, aql.Options{}, corpus)
		_, err := q.NestBuilds(ctx, failingSource{}, aql.Nesting{}, hits)
		if !errors.Is(err, errLoad) {
			t.Errorf("%s: %v", src, err)
		}
	}
	q, _ := aql.Parse(`items.find().include("name")`)
	if q.Relates() {
		t.Fatal("an include of item fields relates")
	}
	if rel, err := q.Nest(ctx, failingSource{}, aql.Nesting{}, []aql.Hit{{}}); err != nil ||
		len(q.Response([]aql.Hit{{}}).WithRelated(rel).Rows) != 1 {
		t.Fatalf("nest without related includes: %v", err)
	}
	q, _ = aql.Parse(`builds.find()`)
	if _, err := q.NestBuilds(ctx, failingSource{}, aql.Nesting{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Run(ctx, 99); err == nil {
		t.Fatal("a run the corpus lacks")
	}
}
