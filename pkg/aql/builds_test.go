package aql

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestParseBuildDomains(t *testing.T) {
	for src, want := range map[string]Domain{
		`items.find()`:                  DomainItems,
		`builds.find()`:                 DomainBuilds,
		` build . promotions . find ()`: DomainPromotions,
	} {
		if q := mustParse(t, src); q.Domain != want || q.Domain.String() != want.String() {
			t.Errorf("%s: domain %v", src, q.Domain)
		}
	}
	// No implicit type=file outside items, and a builds query's own
	// criteria stay outside any Joined.
	q := mustParse(t, `builds.find({"name":"app","@os":"linux"})`)
	if and, _ := q.Criteria.(And); len(and) != 2 || q.Joins() {
		t.Fatalf("builds criteria %#v", q.Criteria)
	}
	q = mustParse(t, `items.find({"name":"a","artifact.module.build.name":"b"})`)
	and, _ := q.Criteria.(And)
	if j, ok := and[len(and)-1].(*Joined); !ok || len(j.Criteria) != 1 || !q.Joins() {
		t.Fatalf("items criteria %#v", q.Criteria)
	}
	if mustParse(t, `items.find({"name":"a"})`).Joins() {
		t.Fatal("a query without paths joins")
	}
	if got := mustParse(t, `builds.find().include("number","*")`).Fields(); len(got) != 9 {
		t.Fatalf("builds include * %v", got)
	}
	got := mustParse(t, `build.promotions.find().include("user","status")`).Fields()
	if !slices.Equal(got, []Field{"status", "user"}) {
		t.Fatalf("promotions fields in render order %v", got)
	}
}

func TestParseBuildErrors(t *testing.T) {
	for src, want := range map[string]error{
		`build.find()`:                            ErrSyntax,
		`build.properties.find()`:                 ErrSyntax,
		`build`:                                   ErrSyntax,
		`build.`:                                  ErrSyntax,
		`build.promotions.delete({})`:             ErrUnsupported,
		`builds.update({})`:                       ErrUnsupported,
		`builds.find({"nosuch":"a"})`:             ErrSyntax,
		`builds.find({"size":1})`:                 ErrSyntax,
		`builds.find({"module.nosuch":"a"})`:      ErrSyntax,
		`builds.find({"module.artifact":"a"})`:    ErrSyntax,
		`builds.find({"module.artifact.@k":"a"})`: ErrSyntax,
		`builds.find({"module.artifact.item.stat.downloads":1})`:       ErrUnsupported,
		`builds.find({"module.artifact.item.virtual_repos":"a"})`:      ErrUnsupported,
		`builds.find({"started":"yesterday"})`:                         ErrSyntax,
		`builds.find({"name":{"$before":"1d"}})`:                       ErrSyntax,
		`builds.find({"@":"a"})`:                                       ErrSyntax,
		`builds.find({"$msp":[{"name":"a"}]})`:                         ErrSyntax,
		`builds.find({"$msp":[{"@a":"1"},{"module.@b":"2"}]})`:         ErrSyntax,
		`builds.find({"$msp":[{"module.@b":1}]})`:                      ErrSyntax,
		`builds.find().include("module.@k")`:                           ErrUnsupported,
		`builds.find().include("module.property.*")`:                   ErrUnsupported,
		`builds.find().include("module.artifact.item.stat")`:           ErrUnsupported,
		`builds.find().include("module.nosuch")`:                       ErrUnsupported,
		`builds.find().include("@os")`:                                 ErrUnsupported,
		`builds.find().sort({"$asc":["size"]})`:                        ErrUnsupported,
		`builds.find().include("name").sort({"$asc":["number"]})`:      ErrInvalid,
		`build.promotions.find({"@k":"a"})`:                            ErrSyntax,
		`build.promotions.find({"$msp":[{"@k":"a"}]})`:                 ErrSyntax,
		`build.promotions.find({"build.nosuch":"a"})`:                  ErrSyntax,
		`items.find({"artifact.module.build":"a"})`:                    ErrSyntax,
		`items.find({"artifact.module.build.@":"a"})`:                  ErrSyntax,
		`items.find({"artifact.module.build.stat.downloads":1})`:       ErrSyntax,
		`items.find({"artifact.item.stat.downloads":1})`:               ErrUnsupported,
		`items.find({"artifact.sha256":"a"})`:                          ErrSyntax,
		`items.find({"dependency.module.name":{"$last":"1d"}})`:        ErrSyntax,
		`items.find({"artifact.module.@k":{"$last":"1d"}})`:            ErrSyntax,
		`items.find({"module.name":"a"})`:                              ErrUnsupported,
		`items.find({"$msp":[{"artifact.module.@a":"1"},{"@b":"2"}]})`: ErrSyntax,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%.60s) = %v, want %v", src, err, want)
		}
	}
}

func TestCheckRestrictedBuilds(t *testing.T) {
	for src, ok := range map[string]bool{
		`builds.find()`: true,
		`builds.find().include("name","number","repo")`:     true,
		`builds.find().include("name","number")`:            false,
		`build.promotions.find().include("status")`:         true,
		`items.find().include("repo","path","name")`:        true,
		`items.find().include("repo","path")`:               false,
		`builds.find().include("name","number","url")`:      false,
		`builds.find().include("repo","name","number","*")`: true,
	} {
		if err := mustParse(t, src).CheckRestricted(); (err == nil) != ok ||
			(err != nil && !errors.Is(err, ErrRestricted)) {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestBuildResponse(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 678_901_000, time.UTC)
	run := types.Build{
		ID: 1, ProjectKey: "proj", Name: "app", Number: "7", Started: at, Created: at,
		CreatedBy: "ci",
	}
	q := mustParse(t, `builds.find().include("name","number","modified","started","repo")`)
	b, err := json.Marshal(q.BuildResponse([]BuildHit{{Build: run}}))
	if err != nil {
		t.Fatal(err)
	}
	// Fields render alphabetically; the missing modification time is left
	// out.
	want := `{"results":[{"build.name":"app","build.number":"7","build.repo":"proj-build-info",` +
		`"build.started":"2026-01-02T03:04:05.678Z"}],"range":{"start_pos":0,"end_pos":1,"total":1}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	q = mustParse(t, `build.promotions.find().offset(1).limit(5)`)
	promo := &types.BuildPromotion{ID: 2, BuildID: 1, Status: "staged", Comment: "rc"}
	b, err = json.Marshal(q.BuildResponse([]BuildHit{{Build: run, Promotion: promo}}))
	if err != nil {
		t.Fatal(err)
	}
	want = `{"results":[{"build.promotion.comment":"rc","build.promotion.status":"staged"}],` +
		`"range":{"start_pos":1,"end_pos":2,"total":1,"limit":5}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestCompileJoins(t *testing.T) {
	compile := func(src string, d Dialect) Compiled {
		t.Helper()
		c, err := mustParse(t, src).Compile(d, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// The JFrog CLI's shape needs no null row, so its joins are inner.
	c := compile(`items.find({"$or":[{"$and":[{"artifact.module.build.name":"a",`+
		`"artifact.module.build.number":"1"}]}]})`, SQLite)
	const inner = "EXISTS (SELECT 1 FROM build_artifacts j1 JOIN build_modules j2"
	if !strings.Contains(c.Where, inner) || strings.Contains(c.Where, "LEFT JOIN") ||
		!slices.Equal(c.Args, []any{"file", "a", "1"}) {
		t.Fatalf("inner joins %s %v", c.Where, c.Args)
	}
	// An alternative without the path needs the null row.
	c = compile(`items.find({"$or":[{"name":"a"},{"dependency.name":"b"}]})`, SQLite)
	const (
		outer   = "(SELECT 1 AS one) j0 LEFT JOIN build_dependencies j1"
		guarded = "(j1.build_id IS NOT NULL AND j1.dependency_id = ?)"
	)
	if !strings.Contains(c.Where, outer) || !strings.Contains(c.Where, guarded) {
		t.Fatalf("outer joins %s", c.Where)
	}
	c = compile(`builds.find({"module.dependency.scope":{"$match":"c*"}})`+
		`.sort({"$desc":["repo","started"]})`, PostgreSQL)
	const (
		scopes = "json_array_elements_text(j2.scopes::json)"
		repo   = `ORDER BY (CASE WHEN builds.project_key = ''`
		tail   = `END) COLLATE "C" DESC, builds.started_at DESC NULLS LAST, builds.id ASC`
	)
	if !strings.Contains(c.Where, scopes) || !strings.Contains(c.Tail, repo) ||
		!strings.HasSuffix(c.Tail, tail) {
		t.Fatalf("postgres builds %s %s", c.Where, c.Tail)
	}
	c = compile(`build.promotions.find({"build.module.@k":{"$ne":"v"}})`, SQLite)
	const module = "NOT EXISTS (SELECT 1 FROM build_properties p WHERE " +
		"p.build_id = j2.build_id AND p.module_position = j2.position AND p.key = ? AND p.value = ?)"
	if !strings.Contains(c.Where, module) ||
		!strings.HasSuffix(c.Tail, "ORDER BY build_promotions.id ASC") {
		t.Fatalf("promotions %s %s", c.Where, c.Tail)
	}
	// A hand-built Joined without paths compiles its criteria alone, and
	// an error inside one surfaces.
	empty := &Query{Criteria: &Joined{Criteria: And{}}}
	if c, err := empty.Compile(SQLite, Options{}); err != nil || c.Where != "1=1" {
		t.Fatalf("empty joined %q %v", c.Where, err)
	}
	bad := &Query{Criteria: &Joined{Criteria: And{
		&PathCriterion{Path: "artifact", Field: "name", rel: relArtifact}, badNode{},
	}}}
	if _, err := bad.Compile(SQLite, Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown node inside a join: %v", err)
	}
}

func TestJoinedEdges(t *testing.T) {
	// A Joined nested in an $or by hand evaluates on its own and restores
	// the outer combination.
	it := types.Item{ID: 1, Name: "a", Actual: types.Checksums{SHA1: "s"}}
	c := &Corpus{Items: []Hit{{Item: it}}}
	c.Builds = []types.Build{{ID: 1, Name: "b", Modules: []types.BuildModule{{
		Artifacts: []types.BuildFile{{Name: "f", Checksums: types.Checksums{SHA1: "s"}}},
	}}}}
	inner := &Joined{Criteria: And{&PathCriterion{
		Path: "artifact.module.build", Field: "name", Op: OpEq, Text: "b", rel: relBuild,
		kind: kindText,
	}}}
	q := &Query{Criteria: Or{&FieldCriterion{Field: FieldName, Op: OpEq, Text: "z"}, inner}}
	if got := SelectItems(q, Options{}, c); len(got) != 1 {
		t.Fatalf("nested joined %v", got)
	}
	// Match sees no builds: the path reaches the null row.
	if q.Match(c.Items[0], time.Now()) {
		t.Fatal("Match joined a build")
	}
	// A promotion of a run the corpus lacks is skipped, as the SQL join
	// skips it, and reaches no build either.
	c.Promotions = []types.BuildPromotion{{ID: 1, BuildID: 9, Status: "s"}}
	promotions := mustParse(t, `build.promotions.find()`)
	if got := SelectBuilds(promotions, Options{}, c); len(got) != 0 {
		t.Fatalf("orphan promotion %v", got)
	}
	orphan := &row{rel: relPromotion, promo: &c.Promotions[0]}
	if got := c.related(orphan, relBuild); got != nil {
		t.Fatalf("orphan promotion's build %v", got)
	}
	// An items query gets nothing from SelectBuilds.
	if got := SelectBuilds(mustParse(t, `items.find()`), Options{}, c); got != nil {
		t.Fatalf("items through SelectBuilds %v", got)
	}
	if !falseWithout(Or{}, "artifact") || falseWithout(And{}, "artifact") {
		t.Fatal("falseWithout on empty nodes")
	}
}
