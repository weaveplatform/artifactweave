package aql

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestUnitSpanUnits: $before and $last take the long units aql-search-criteria
// documents and those Artifactory 6.20 also parses (#410).
func TestUnitSpanUnits(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"500mills":  now.Add(-500 * time.Millisecond),
		"500millis": now.Add(-500 * time.Millisecond),
		"2seconds":  now.Add(-2 * time.Second),
		"10mi":      now.Add(-10 * time.Minute),
		"3days":     now.Add(-3 * 24 * time.Hour),
		"2weeks":    now.Add(-14 * 24 * time.Hour),
		"1months":   time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC),
		"2years":    time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC),
	} {
		s, err := span(value{kind: vString, text: in})
		if err != nil || !s.back(now).Equal(want) {
			t.Errorf("span(%s) = %v, %v", in, s.back(now), err)
		}
	}
	q := mustParse(t, `items.find({"created":{"$last":"3days"}})`)
	if c := q.Criteria.(And)[0].(*FieldCriterion); c.Span != (Span{N: 3, Unit: "d"}) {
		t.Fatalf("span %+v", c.Span)
	}
	for _, bad := range []string{"3day", "3D", "3hours", "days"} {
		if _, err := span(value{kind: vString, text: bad}); !errors.Is(err, ErrSyntax) {
			t.Errorf("span(%s): %v", bad, err)
		}
	}
}

// TestUnitCriteriaList: find, delete and update take several criteria objects
// separated by commas, as one implicit $and (#410).
func TestUnitCriteriaList(t *testing.T) {
	q := mustParse(t,
		`items.find({"repo":"my_local"},{"@artifactory.licenses":{"$match":"*LGPL*"}})`)
	and := q.Criteria.(And)
	if len(and) != 3 || and[0].(*FieldCriterion).Field != FieldRepo ||
		and[1].(*PropertyCriterion).Key != "artifactory.licenses" ||
		and[2].(*FieldCriterion).Field != FieldType {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	hit := Hit{
		Item:       types.Item{RepoKey: "my_local", Name: "a"},
		Properties: types.Properties{"artifactory.licenses": {"LGPL-2.1"}},
	}
	if !q.Match(hit, time.Time{}) {
		t.Fatal("both objects hold")
	}
	hit.Item.RepoKey = "other"
	if q.Match(hit, time.Time{}) {
		t.Fatal("the objects are an and, not an or")
	}
	// An object with several members, an empty object and actions.
	q = mustParse(t, `items.find({"repo":"r","name":"n"},{},{"$or":[{"size":1},{"size":2}]})`)
	if and := q.Criteria.(And); len(and) != 4 {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	q = mustParse(t, `items.delete({"repo":"r"},{"name":"n"}).dryRun(true)`)
	if and := q.Criteria.(And); len(and) != 3 || q.Action != DeleteItems {
		t.Fatalf("delete %#v", q.Criteria)
	}
	q = mustParse(t,
		`properties.update({"item.repo":"r"},{"item.name":"n"}).keys("k").newValue("v")`)
	if and := q.Criteria.(And); len(and) != 3 || and[1].(*FieldCriterion).Field != FieldName {
		t.Fatalf("update %#v", q.Criteria)
	}
	for _, src := range []string{
		`items.find(,{"repo":"r"})`, `items.find({"repo":"r"},)`, `items.find({"repo":"r"} {})`,
		`items.find({"repo":"r"},"a")`, `items.find({"repo":"r"},{"nosuch":1})`,
	} {
		if _, err := Parse(src); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%s) = %v", src, err)
		}
	}
	if _, err := Parse(
		`properties.update({},{"key":"a"}).keys("a").newValue("v")`,
	); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("update with a property field: %v", err)
	}
}

// TestUnitItemInfos: item.infos is a documented primary domain, refused as
// unsupported rather than as an unknown domain (#410).
func TestUnitItemInfos(t *testing.T) {
	for src, want := range map[string]error{
		`item.infos.find({"item_id":1})`: ErrUnsupported,
		`item.find()`:                    ErrSyntax,
		`item.nosuch.find()`:             ErrSyntax,
		`item.`:                          ErrSyntax,
		`item`:                           ErrSyntax,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%s) = %v, want %v", src, err, want)
		}
	}
}

// TestUnitRelatedIncludes covers what an include of another domain's fields
// reads (#410).
func TestUnitRelatedIncludes(t *testing.T) {
	q := mustParse(t, `items.find().include("name","artifact.module.build.*",`+
		`"artifact.module.build.*","dependency","artifact.name","artifact.item.repo")`)
	want := []relInclude{
		{path: "artifact.module.build", rel: relBuild, whole: true},
		{path: "dependency", rel: relDependency, whole: true},
		{path: "artifact", rel: relArtifact, field: "name"},
		{path: "artifact.item", rel: relItem, field: "repo"},
	}
	if !q.Relates() || !slices.Equal(q.related, want) ||
		!slices.Equal(q.Fields(), []Field{FieldName}) {
		t.Fatalf("related %+v fields %v", q.related, q.Fields())
	}
	// Including only related fields keeps the item defaults.
	if got := mustParse(t, `items.find().include("artifact.name")`).Fields(); !slices.Equal(
		got, defaultFields) {
		t.Fatalf("fields %v", got)
	}
	tree := includeTree(relItem, q.related)
	if len(tree.children) != 2 || tree.children[0].path != "artifact" ||
		tree.children[1].path != "dependency" || len(tree.children[1].fields) != 5 ||
		!slices.Equal(tree.children[0].fields, []string{"name"}) {
		t.Fatalf("tree %+v", tree.children)
	}
	for src, want := range map[string]error{
		`items.find().include("artifact.module.build.stat.downloads")`:           ErrUnsupported,
		`items.find().include("artifact.property.*")`:                            ErrUnsupported,
		`items.find().include("artifact.@k")`:                                    ErrUnsupported,
		`items.find().include("artifact.@*")`:                                    ErrUnsupported,
		`items.find().include("artifact.@")`:                                     ErrUnsupported,
		`items.find().include("artifact.item.stat.nosuch")`:                      ErrUnsupported,
		`items.find().include("artifact.item.property.nosuch")`:                  ErrUnsupported,
		`items.find().include("artifact.item.virtual_repos")`:                    ErrUnsupported,
		`items.find().include("artifact.nosuch")`:                                ErrUnsupported,
		`items.find().include("module.name")`:                                    ErrUnsupported,
		`items.find().include("artifact.name").sort({"$asc":["artifact.name"]})`: ErrUnsupported,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%s) = %v, want %v", src, err, want)
		}
	}
	// Sort, offset and limit beside a related include apply to the items.
	q = mustParse(t,
		`items.find().include("name","artifact.name").sort({"$asc":["name"]}).limit(1)`)
	if q.Sort == nil || !q.HasLimit {
		t.Fatalf("modifiers %+v", q)
	}
}

// TestUnitRelatedPropertyIncludes covers what an include of a related row's
// properties or statistics reads (#494), and the filter an "@<key>"
// include adds on the row its path reaches.
func TestUnitRelatedPropertyIncludes(t *testing.T) {
	q := mustParse(t, `items.find().include("name","artifact.module.build.@os",`+
		`"artifact.module.property.key","artifact.module.property","artifact.item.stat.downloads",`+
		`"artifact.item.stat","dependency.item.@build.name","dependency.item.property.value")`)
	want := []relInclude{
		{path: "artifact.module.build", rel: relBuild, part: partProperties, key: "os"},
		{path: "artifact.module", rel: relModule, part: partProperties},
		{path: "artifact.module", rel: relModule, part: partProperties, whole: true},
		{path: "artifact.item", rel: relItem, part: partStats, field: "downloads"},
		{path: "artifact.item", rel: relItem, part: partStats, whole: true},
		{path: "dependency.item", rel: relItem, part: partProperties, key: "build.name"},
		{path: "dependency.item", rel: relItem, part: partProperties},
	}
	if !slices.Equal(q.related, want) || !slices.Equal(q.Fields(), []Field{FieldName}) ||
		q.Properties() || len(q.Stats()) > 0 {
		t.Fatalf("related %+v fields %v", q.related, q.Fields())
	}
	// Each "@<key>" include filters on its path, inside the Joined that the
	// include shares the path's rows with.
	root := q.Criteria.(And)
	joined, ok := root[len(root)-1].(*Joined)
	if !ok || len(joined.Criteria) != 2 {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	for i, path := range []string{"artifact.module.build", "dependency.item"} {
		p, ok := joined.Criteria[i].(*PathProperty)
		if !ok || p.Path != path || p.Criterion.Key == "" || p.Criterion.Value != "*" {
			t.Fatalf("filter %d %#v", i, joined.Criteria[i])
		}
	}
	tree := includeTree(relItem, q.related)
	byPath := map[string]*nestNode{}
	var walk func(n *nestNode)
	walk = func(n *nestNode) {
		byPath[n.path] = n
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(tree)
	build, module := byPath["artifact.module.build"], byPath["artifact.module"]
	item, dep := byPath["artifact.item"], byPath["dependency.item"]
	switch {
	case !build.props || !slices.Equal(build.keys, []string{"os"}) || build.propsWhole:
		t.Fatalf("build node %+v", build)
	case !module.props || module.keys != nil || !module.propsWhole:
		t.Fatalf("module node %+v", module)
	case item.props || !slices.Equal(item.stats, statOrder) || !item.statsWhole:
		t.Fatalf("item node %+v", item)
	case !dep.props || dep.keys != nil || dep.propsWhole || len(dep.stats) != 0:
		t.Fatalf("dependency item node %+v", dep)
	}
	if n := includeTree(relItem, mustParse(t,
		`items.find().include("artifact.item.stat.remote_path","artifact.item.stat.downloads")`,
	).related).children[0].children[0]; !slices.Equal(n.stats,
		[]StatField{StatDownloads, StatRemotePath}) || n.statsWhole {
		t.Fatalf("named statistics %+v", n)
	}
	// A run's own properties nest as a related run's do, and its "@<key>"
	// filters the runs themselves.
	q = mustParse(t, `builds.find().include("name","@os","property.*")`)
	if !slices.Equal(q.related, []relInclude{
		{rel: relBuild, part: partProperties, key: "os"},
		{rel: relBuild, part: partProperties, whole: true},
	}) || !slices.Equal(q.Fields(), []Field{"name"}) {
		t.Fatalf("builds related %+v fields %v", q.related, q.Fields())
	}
	if p, ok := q.Criteria.(And)[0].(*PathProperty); !ok || p.Path != "" ||
		p.Criterion.Key != "os" {
		t.Fatalf("builds criteria %#v", q.Criteria)
	}
	if mustParse(t, `builds.find().include("*")`).Relates() {
		t.Fatal(`builds include("*") relates`)
	}
}

// TestUnitNestBudget: an include whose combinations of related rows outgrow
// the budget is refused rather than worked through.
func TestUnitNestBudget(t *testing.T) {
	saved := maxNestCombinations
	maxNestCombinations = 3
	t.Cleanup(func() { maxNestCombinations = saved })
	sha := strings.Repeat("a", 40)
	files := make([]types.BuildFile, 4)
	for i := range files {
		files[i] = types.BuildFile{Name: strconv.Itoa(i), Checksums: types.Checksums{SHA1: sha}}
	}
	c := &Corpus{
		Items: []Hit{{Item: types.Item{
			ID: 1, RepoKey: "r", Name: "f", Actual: types.Checksums{SHA1: sha},
		}}},
		Builds: []types.Build{{
			ID: 1, Name: "b", Number: "1",
			Modules: []types.BuildModule{{ID: "m", Artifacts: files}},
		}},
	}
	q := mustParse(t, `items.find().include("artifact.name")`)
	if _, err := q.Nest(context.Background(), c, Nesting{}, c.Items); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nest over the budget: %v", err)
	}
	maxNestCombinations = 4
	rel, err := q.Nest(context.Background(), c, Nesting{}, c.Items)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(q.Response(c.Items).WithRelated(rel).Rows[0])
	if !strings.Contains(string(b), `"artifacts":[{"artifact.name":"0"},{"artifact.name":"1"},`+
		`{"artifact.name":"2"},{"artifact.name":"3"}]`) {
		t.Fatalf("row %s", b)
	}
}

// TestUnitRoots covers the rows of repository roots without a record (#445):
// what Resolve lists, which a query matches, and how WithRoots merges them
// into a store's rows.
func TestUnitRoots(t *testing.T) {
	repos := Repositories{
		"local": {Class: RepoLocal}, "remote": {Class: RepoRemote},
		"virtual": {Class: RepoVirtual, Members: []string{"local"}},
	}
	q := mustParse(t, `items.find({"type":"folder"}).sort({"$asc":["name"]}).offset(1).limit(2)`)
	q.Resolve(repos)
	if want := []RepoRef{{Key: "local"}, {Key: "remote", Cached: true}}; !slices.Equal(
		q.Roots(), want) {
		t.Fatalf("roots %v", q.Roots())
	}
	// A query built in code lists none.
	built := &Query{Criteria: And{}}
	built.Resolve(repos)
	if built.Roots() != nil {
		t.Fatalf("built roots %v", built.Roots())
	}
	roots := q.MissingRoots(Options{}, func(r RepoRef) bool { return r.Key == "remote" })
	if len(roots) != 1 || roots[0].Item.RepoKey != "local" || !roots[0].Item.IsRoot() ||
		!roots[0].Item.Folder || roots[0].Item.ID != 0 {
		t.Fatalf("missing roots %+v", roots)
	}
	stored := []Hit{
		{Item: types.Item{ID: 5, RepoKey: "local", Path: ".", Name: "a", Folder: true}},
		{Item: types.Item{ID: 6, RepoKey: "local", Path: ".", Name: "b", Folder: true}},
	}
	var asked *Query
	got, err := q.WithRoots(Options{}, roots, func(w *Query, _ Options) ([]Hit, error) {
		asked = w
		return stored, nil
	})
	if err != nil || len(got) != 2 || got[0].Item.Name != "a" || got[1].Item.Name != "b" {
		t.Fatalf("merged %+v, %v", got, err)
	}
	// The store is asked for the rows up to the end of the window.
	if asked.HasOffset || !asked.HasLimit || asked.Limit != 3 {
		t.Fatalf("store query %+v", asked)
	}
	// Without roots the store answers alone; a failure is the error.
	if _, err := q.WithRoots(Options{}, nil, func(w *Query, _ Options) ([]Hit, error) {
		if w != q {
			t.Fatal("query changed")
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	errBoom := errors.New("boom")
	if _, err := q.WithRoots(Options{}, roots, func(*Query, Options) ([]Hit, error) {
		return nil, errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatalf("store error %v", err)
	}
	// No limit, or one too large to add the offset to, asks for every row.
	for _, l := range []string{"", ".limit(9223372036854775807)"} {
		q := mustParse(t, `items.find({"type":"folder"}).offset(1)`+l)
		q.Resolve(repos)
		if _, err := q.WithRoots(Options{}, roots, func(w *Query, _ Options) ([]Hit, error) {
			if w.HasLimit {
				t.Errorf("limit %d", w.Limit)
			}
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Roots without a record tie on ID and order by key, the cache last.
	a, b := types.RootItem("x", false), types.RootItem("x", true)
	if q.Compare(a, b) >= 0 || q.Compare(b, a) <= 0 || q.Compare(a, a) != 0 {
		t.Fatal("root order")
	}
}
