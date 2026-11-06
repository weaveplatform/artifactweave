package aql

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func mustParse(t *testing.T, src string) *Query {
	t.Helper()
	q, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%s): %v", src, err)
	}
	return q
}

func TestUnitParseModifiers(t *testing.T) {
	q := mustParse(t, "  items . find ( { } )\n.include(\"name\", \"size\")\n"+
		`.sort({"$desc":["size","name"]}).offset(3).transitive().limit(7).distinct(false)`)
	if !slices.Equal(q.Include, []string{"name", "size"}) || q.Sort == nil || !q.Sort.Desc ||
		!slices.Equal(q.Sort.Fields, []Field{FieldSize, FieldName}) || q.Offset != 3 ||
		!q.HasOffset || q.Limit != 7 || !q.HasLimit || !q.Transitive || q.Distinct {
		t.Fatalf("query %+v", q)
	}
	// An empty find still searches files only.
	and, ok := q.Criteria.(And)
	if !ok || len(and) != 1 || and[0].(*FieldCriterion).Text != "file" {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// Repeated keys are an implicit $and, kept in order.
	q = mustParse(t, `items.find({"path":{"$ne":"."},"path":{"$match":"a*"},"type":"any"})`)
	and, _ = q.Criteria.(And)
	if len(and) != 3 || and[0].(*FieldCriterion).Op != OpNe ||
		and[1].(*FieldCriterion).Op != OpMatch {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// Several operators on one field are their $and; numbers may be strings.
	q = mustParse(t, `items.find({"size":{"$gt":"1","$lte":2},"type":"file"})`)
	and, _ = q.Criteria.(And)
	sizes, _ := and[0].(And)
	if len(sizes) != 2 || sizes[0].(*FieldCriterion).Number != 1 {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// Escapes in strings decode as JSON.
	q = mustParse(t, `items.find({"name":"a\"bé","type":"file"})`)
	if and, _ := q.Criteria.(And); and[0].(*FieldCriterion).Text != `a"bé` {
		t.Fatalf("criteria %#v", q.Criteria)
	}
}

func TestUnitParseErrors(t *testing.T) {
	deep := `items.find(` + strings.Repeat(`{"$and":[`, maxDepth+2) + `{}` +
		strings.Repeat(`]}`, maxDepth+2) + `)`
	deeper := `items.find(` + strings.Repeat(`[`, maxValueDepth+2) + `)`
	for src, want := range map[string]error{
		"":                                                ErrEmpty,
		" \n\t":                                           ErrEmpty,
		`"items"`:                                         ErrSyntax,
		`releases.find()`:                                 ErrUnsupported,
		`widgets.find()`:                                  ErrSyntax,
		`items.update({})`:                                ErrUnsupported,
		`items.search()`:                                  ErrSyntax,
		`items find()`:                                    ErrSyntax,
		`items.find(`:                                     ErrSyntax,
		`items.find([])`:                                  ErrSyntax,
		`items.find({"name":"a"}`:                         ErrSyntax,
		`items.find() trailing`:                           ErrSyntax,
		`items.find()#`:                                   ErrSyntax,
		`items.find({name:"a"})`:                          ErrSyntax,
		`items.find({"name" "a"})`:                        ErrSyntax,
		`items.find({"name":"a" "b":1})`:                  ErrSyntax,
		`items.find({"name":"unterminated)`:               ErrSyntax,
		`items.find({"name":"bad\x"})`:                    ErrSyntax,
		`items.find({"size":-})`:                          ErrSyntax,
		`items.find({"size":1.})`:                         ErrSyntax,
		`items.find({"size":1e})`:                         ErrSyntax,
		`items.find({"size":1.5})`:                        ErrSyntax,
		`items.find({"size":true})`:                       ErrSyntax,
		`items.find({"size":{}})`:                         ErrSyntax,
		`items.find({"size":{"$match":"1"}})`:             ErrSyntax,
		`items.find({"size":{"$bogus":1}})`:               ErrSyntax,
		`items.find({"name":1})`:                          ErrSyntax,
		`items.find({"name":{"$before":"1d"}})`:           ErrSyntax,
		`items.find({"type":"blob"})`:                     ErrSyntax,
		`items.find({"type":{"$gt":"file"}})`:             ErrSyntax,
		`items.find({"created":"yesterday"})`:             ErrSyntax,
		`items.find({"created":5})`:                       ErrSyntax,
		`items.find({"created":{"$last":5}})`:             ErrSyntax,
		`items.find({"created":{"$last":"3fortnights"}})`: ErrSyntax,
		`items.find({"created":{"$last":"d"}})`:           ErrSyntax,
		`items.find({"nosuch":"a"})`:                      ErrSyntax,
		`items.find({"stat.nosuch":1})`:                   ErrSyntax,
		`items.find({"stat.downloads":{"$gt":null}})`:     ErrSyntax,
		`items.find({"stat.downloads":{"$match":"1"}})`:   ErrSyntax,
		`items.find({"stat.downloaded_by":1})`:            ErrSyntax,
		`items.find({"stat":1})`:                          ErrSyntax,
		`items.find({"archive.entry.name":"a"})`:          ErrUnsupported,
		`items.find({"virtual_repos":"a"})`:               ErrUnsupported,
		`items.find({"property.key":1})`:                  ErrSyntax,
		`items.find({"property.value":{"$last":"1d"}})`:   ErrSyntax,
		`items.find({"$msp":[]})`:                         ErrSyntax,
		`items.find({"$msp":{}})`:                         ErrSyntax,
		`items.find({"$msp":["a"]})`:                      ErrSyntax,
		`items.find({"$msp":[{"name":"a"}]})`:             ErrSyntax,
		`items.find({"$msp":[{"@k":1}]})`:                 ErrSyntax,
		`items.find({"$msp":[{"@":"a"}]})`:                ErrSyntax,
		`items.find({"$rf":[]})`:                          ErrUnsupported,
		`items.find({"$not":[]})`:                         ErrSyntax,
		`items.find({"$and":{}})`:                         ErrSyntax,
		`items.find({"$or":["a"]})`:                       ErrSyntax,
		`items.find({"@":"a"})`:                           ErrSyntax,
		`items.find({"@k":1})`:                            ErrSyntax,
		`items.find({"@k":{"$last":"1d"}})`:               ErrSyntax,
		`items.find({"name":[1,]})`:                       ErrSyntax,
		`items.find({"name":nul})`:                        ErrSyntax,
		deep:                                              ErrSyntax,
		deeper:                                            ErrSyntax,
		`items.find().include(1)`:                         ErrSyntax,
		`items.find().include("stat.nosuch")`:             ErrUnsupported,
		`items.find().sort("name")`:                       ErrSyntax,
		`items.find().sort({"$up":["name"]})`:             ErrSyntax,
		`items.find().sort({"$asc":[]})`:                  ErrSyntax,
		`items.find().sort({"$asc":[1]})`:                 ErrSyntax,
		`items.find().sort({"$asc":["property.key"]})`:    ErrUnsupported,
		`items.find().limit()`:                            ErrSyntax,
		`items.find().limit(-1)`:                          ErrSyntax,
		`items.find().offset(1.5)`:                        ErrSyntax,
		`items.find().transitive(true)`:                   ErrSyntax,
		`items.find().distinct("yes")`:                    ErrSyntax,
		`items.find().limit(1).limit(2)`:                  ErrSyntax,
		`items.find().explode()`:                          ErrSyntax,
		`items.find().(`:                                  ErrSyntax,
		`items.find().limit(1,`:                           ErrSyntax,
		`items.find().limit(1 2)`:                         ErrSyntax,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%.60s) = %v, want %v", src, err, want)
		}
	}
	long := `items.find({"name":"` + strings.Repeat("é", MaxLength) + `"})`
	if _, err := Parse(
		long,
	); !errors.Is(err, ErrTooLong) ||
		err.Error() != "AQL query is too long; please reduce the query length to less than 6000 chars" {
		t.Fatalf("long query: %v", err)
	}
	// The limit counts characters, not bytes.
	exact := `items.find({"name":"` + strings.Repeat("é", MaxLength-24) + `"})`
	if _, err := Parse(exact); err != nil {
		t.Fatalf("query of %d characters: %v", len([]rune(exact)), err)
	}
	if _, err := Parse(`items.find({"name":"a"}) .`); err == nil ||
		!strings.Contains(err.Error(), "offset 26") {
		t.Fatalf("error offset: %v", err)
	}
}

func TestUnitTimesAndSpans(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026":                          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"2026-03":                       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"2026-03-04":                    time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		"2026-03-04T05:06":              time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC),
		"2026-03-04T05:06+01:00":        time.Date(2026, 3, 4, 4, 6, 0, 0, time.UTC),
		"2026-03-04T05:06:07.5":         time.Date(2026, 3, 4, 5, 6, 7, 5e8, time.UTC),
		"2016-10-16T16:52:21.594+03:00": time.Date(2016, 10, 16, 13, 52, 21, 594e6, time.UTC),
	} {
		got, err := instant(value{kind: vString, text: in})
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("instant(%s) = %v, %v", in, got, err)
		}
	}
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"500ms":     now.Add(-500 * time.Millisecond),
		"2s":        now.Add(-2 * time.Second),
		"10minutes": now.Add(-10 * time.Minute),
		"1d":        now.Add(-24 * time.Hour),
		"1w":        now.Add(-7 * 24 * time.Hour),
		"1mo":       time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC), // February has 28 days
		"2y":        time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC),
	} {
		s, err := span(value{kind: vString, text: in})
		if err != nil || !s.back(now).Equal(want) {
			t.Errorf("span(%s) = %v, %v", in, s.back(now), err)
		}
	}
}

func TestUnitGlobTranslations(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"*", "", true},
		{"*", "a/b", true},
		{"a*c", "abbc", true},
		{"a*c", "abcd", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"?", "é", true},
		{"*.jar", "x.JAR", false},
		{"a**b", "ab", true},
		{"*a*b", "xaxb", true},
		{"", "", true},
		{"", "a", false},
		{"[1]", "[1]", true},
		{"[1]", "1", false},
		{"*x", "abc", false},
	} {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
	if got := sqliteGlob("a[b]*?"); got != "a[[]b]*?" {
		t.Errorf("sqliteGlob = %q", got)
	}
	if got := likePattern(`50%_a\b*?`); got != `50\%\_a\\b%_` {
		t.Errorf("likePattern = %q", got)
	}
}

func TestUnitCompile(t *testing.T) {
	q := mustParse(t, `items.find({"repo":"r-cache","name":{"$match":"a*"},"@k":{"$ne":"v"},`+
		`"@*":"*","created":{"$last":"1d"},"size":{"$gt":1}}).sort({"$desc":["created","name"]}).offset(2)`)
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	lite, err := q.Compile(SQLite, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	wantWhere := "((items.repo_key = ? AND items.cached = ?) AND (items.name GLOB ?) AND " +
		"NOT EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = items.id AND p.key = ? " +
		"AND p.value = ?) AND EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = items.id) " +
		"AND items.created_at > ? AND items.size > ? AND items.type = ?)"
	if lite.Where != wantWhere {
		t.Fatalf("where:\n%s\nwant\n%s", lite.Where, wantWhere)
	}
	if lite.Tail != " ORDER BY items.created_at DESC NULLS LAST, items.name DESC, items.id ASC LIMIT -1 OFFSET ?" {
		t.Fatalf("tail %s", lite.Tail)
	}
	wantArgs := []any{
		"r",
		true,
		"a*",
		"k",
		"v",
		now.Add(-24 * time.Hour).UnixMicro(),
		int64(1),
		"file",
		int64(2),
	}
	if !slices.Equal(lite.Args, wantArgs) {
		t.Fatalf("args %v", lite.Args)
	}
	pg, _ := q.Compile(PostgreSQL, Options{Now: now})
	if !strings.Contains(pg.Where, `(items.name COLLATE "C" LIKE ? ESCAPE '\')`) ||
		pg.Tail != ` ORDER BY items.created_at DESC NULLS LAST, items.name COLLATE "C" DESC, items.id ASC OFFSET ?` {
		t.Fatalf("postgres:\n%s\n%s", pg.Where, pg.Tail)
	}
	// Without criteria the compiler still writes a valid WHERE; a nil
	// criteria tree only comes from hand-built queries.
	if c, _ := (&Query{}).Compile(SQLite, Options{Max: 5}); c.Where != "1=1" ||
		c.Tail != " ORDER BY items.id ASC LIMIT ? OFFSET ?" {
		t.Fatalf("empty query %+v", c)
	}
	if _, err := (&Query{Criteria: Or{badNode{}}}).Compile(
		SQLite,
		Options{},
	); !errors.Is(
		err,
		ErrUnsupported,
	) {
		t.Fatalf("unknown node: %v", err)
	}
}

type badNode struct{}

func (badNode) node() {}

func TestUnitMatchEdges(t *testing.T) {
	it := Hit{Item: types.Item{Name: "a", Size: 3}}
	if (&Query{Criteria: badNode{}}).Match(it, time.Now()) {
		t.Fatal("an unknown node matched")
	}
	if !(&Query{}).Match(it, time.Now()) {
		t.Fatal("no criteria must match everything")
	}
	if compareOp(OpMatch, 0) {
		t.Fatal("compareOp with a non-ordering operator")
	}
	q := mustParse(t, `items.find({"type":{"$ne":"any"}})`)
	if q.Match(it, time.Now()) {
		t.Fatal(`type $ne "any" matched`)
	}
	c, _ := q.Compile(SQLite, Options{})
	if c.Where != "(1=0)" {
		t.Fatalf("where %s", c.Where)
	}
}

func TestUnitFieldsAndRestrictions(t *testing.T) {
	for include, want := range map[string][]Field{
		``:                          defaultFields,
		`.include("property")`:      defaultFields,
		`.include("stat","@k")`:     defaultFields,
		`.include("sha256","repo")`: {FieldRepo, FieldSHA256},
		`.include("*")`:             fieldOrder,
		`.include("virtual_repos","name","path","repo")`: {FieldRepo, FieldPath, FieldName},
	} {
		q := mustParse(t, `items.find()`+include)
		if !slices.Equal(q.Fields(), want) {
			t.Errorf("%s: fields %v", include, q.Fields())
		}
	}
	for include, ok := range map[string]bool{
		``: true, `.include("property")`: true, `.include("*")`: true,
		`.include("repo","path","name")`: true, `.include("repo","name")`: false,
	} {
		err := mustParse(t, `items.find()`+include).CheckRestricted()
		if (err == nil) != ok || (err != nil && !errors.Is(err, ErrRestricted)) {
			t.Errorf("%s: %v", include, err)
		}
	}
	for _, inc := range []string{"property", "property.*", "property.key", "property.value", "@k"} {
		if !mustParse(t, `items.find().include("`+inc+`")`).Properties() {
			t.Errorf("include(%s) does not return properties", inc)
		}
	}
}

func TestUnitResponse(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 678e6, time.FixedZone("X", 3600))
	file := types.Item{
		RepoKey: "r", Cached: true, Path: "a", Name: "f.jar", Size: 5, Depth: 2,
		Actual:  types.Checksums{SHA1: "s1", MD5: "m5", SHA256: "s256"},
		Created: at, CreatedBy: "u", Modified: at,
	}
	folder := types.Item{RepoKey: "r", Path: ".", Name: "a", Folder: true}
	hits := []Hit{
		{Item: file, Properties: types.Properties{"b": {"2", "1"}, "a": {"x"}}},
		{Item: folder},
	}
	// Offset and limit beside a property include are refused by Parse, so
	// they are set by hand to cover rendering the window.
	q := mustParse(t, `items.find().include("*","property")`)
	q.Offset, q.Limit, q.HasLimit = 4, 2, true
	b, err := json.Marshal(q.Response(hits))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"results":[{"repo":"r-cache","path":"a","name":"f.jar","type":"file","size":5,` +
		`"depth":2,"created":"2026-01-02T02:04:05.678Z","created_by":"u",` +
		`"modified":"2026-01-02T02:04:05.678Z","actual_md5":"m5","actual_sha1":"s1",` +
		`"sha256":"s256","properties":[{"key":"a","value":"x"},{"key":"b","value":"2"},` +
		`{"key":"b","value":"1"}]},{"repo":"r","path":".","name":"a","type":"folder","size":0,` +
		`"depth":0}],"range":{"start_pos":4,"end_pos":6,"total":2,"limit":2}}`
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
	r := mustParse(t, `items.find()`).Response(nil)
	r.Notification = "capped"
	b, _ = json.Marshal(r)
	if string(
		b,
	) != `{"results":[],"range":{"start_pos":0,"end_pos":0,"total":0,"notification":"capped"}}` {
		t.Fatalf("empty response %s", b)
	}
	row, _ := json.Marshal(mustParse(t, `items.find().include("name")`).Response(hits[1:]).Rows[0])
	if string(row) != `{"name":"a"}` {
		t.Fatalf("row %s", row)
	}
}

func TestUnitWindow(t *testing.T) {
	q := &Query{Offset: 1}
	if o, l := q.Window(Options{}); o != 1 || l != -1 {
		t.Fatal(o, l)
	}
	if _, l := q.Window(Options{Max: 9}); l != 9 {
		t.Fatal(l)
	}
	q.Limit, q.HasLimit = 0, true
	if _, l := q.Window(Options{Max: 9}); l != 0 {
		t.Fatal(l)
	}
	// The cap trims a query's own limit above it.
	q.Limit = 20
	if _, l := q.Window(Options{Max: 9}); l != 9 {
		t.Fatal(l)
	}
	if (Options{}).now().IsZero() {
		t.Fatal("zero clock")
	}
}

func TestUnitIncludes(t *testing.T) {
	q := mustParse(t, `items.find().include("@b","@a","@b","stat.remote_path","stat.downloads")`)
	if !slices.Equal(q.PropertyKeys(), []string{"b", "a"}) ||
		!slices.Equal(q.Stats(), []StatField{StatDownloads, StatRemotePath}) || q.VirtualRepos() {
		t.Fatalf("keys %v stats %v", q.PropertyKeys(), q.Stats())
	}
	// Each key include filters out items without the key.
	and := q.Criteria.(And)
	if len(and) != 3 ||
		*and[1].(*PropertyCriterion) != (PropertyCriterion{Key: "b", Op: OpEq, Value: "*"}) ||
		and[2].(*PropertyCriterion).Key != "a" {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// The whole property domain renders every key, but the key still filters.
	q = mustParse(t, `items.find().include("@a","property.*")`)
	if q.PropertyKeys() != nil || len(q.Criteria.(And)) != 2 {
		t.Fatalf("keys %v criteria %#v", q.PropertyKeys(), q.Criteria)
	}
	for _, inc := range []string{"stat", "stat.*"} {
		if !slices.Equal(mustParse(t, `items.find().include("`+inc+`")`).Stats(), statOrder) {
			t.Errorf("include(%s) does not return every statistic", inc)
		}
	}
	q = mustParse(t, `items.find().include("name").sort({"$asc":["name"]}).limit(1)`)
	if q.Properties() || q.Stats() != nil {
		t.Fatal("an item-field include returns another domain")
	}
	if !mustParse(t, `items.find().include("repo","path","name","virtual_repos")`).VirtualRepos() {
		t.Fatal("virtual_repos include")
	}
}

func TestUnitPropertyAndStatCriteria(t *testing.T) {
	q := mustParse(t, `items.find({"$msp":[{"@license":{"$match":"*GPL*","$ne":"x"}},`+
		`{"property.value":"v"}],"property.key":"k","stat.downloads":{"$ne":null}})`)
	and := q.Criteria.(And)
	m := and[0].(MSP)
	if len(m) != 3 || m[0].Part != PartPair || m[1].Op != OpNe || m[2].Part != PartValue ||
		m[2].Value != "v" {
		t.Fatalf("msp %#v", m)
	}
	if p := and[1].(*PropertyCriterion); p.Part != PartKey || p.Key != "" || p.Value != "k" {
		t.Fatalf("property.key %#v", p)
	}
	if s := and[2].(*StatCriterion); s.Field != StatDownloads || !s.Null || s.Op != OpNe {
		t.Fatalf("stat %#v", s)
	}
	q = mustParse(
		t,
		`items.find({"stat.downloaded":{"$last":"1d"},"stat.remote_origin":{"$match":"h*"}})`,
	)
	and = q.Criteria.(And)
	if s := and[0].(*StatCriterion); s.Span != (Span{N: 1, Unit: "d"}) {
		t.Fatalf("stat span %#v", s)
	}
	if s := and[1].(*StatCriterion); s.Text != "h*" {
		t.Fatalf("stat text %#v", s)
	}
}

// TestUnitCompileValuePrefix checks the conditions on the indexed value prefix
// PostgreSQL gets beside each positive value comparison; the store contract
// suite checks the rows they find.
func TestUnitCompileValuePrefix(t *testing.T) {
	long := strings.Repeat("é", valuePrefixLen+5)
	cases := []struct {
		criteria, want string
		args           []any
	}{
		{
			`{"@k":"v"}`, ` AND p.key = ? AND left(p.value, 255) = left(?, 255) AND p.value = ?)`,
			[]any{"k", "v", "v"},
		},
		{
			`{"@k":{"$gt":"v"}}`,
			` AND left(p.value, 255) >= left(?, 255) AND p.value COLLATE "C" > ?)`,
			[]any{"k", "v", "v"},
		},
		{
			`{"@k":{"$lte":"v"}}`,
			` AND left(p.value, 255) <= left(?, 255) AND p.value COLLATE "C" <= ?)`,
			[]any{"k", "v", "v"},
		},
		{
			`{"@k":{"$match":"a_b*c?"}}`,
			` AND left(p.value, 255) LIKE ? ESCAPE '\' AND (p.value COLLATE "C" LIKE ? ESCAPE '\'))`,
			[]any{"k", `a\_b%`, `a\_b%c_`},
		},
		// A literal longer than the prefix is cut to it, in characters.
		{
			`{"@k":{"$match":"` + long + `*"}}`, ` LIKE ? ESCAPE '\' AND (p.value`,
			[]any{"k", long[:2*valuePrefixLen] + "%", long + "%"},
		},
		// Without a literal start there is nothing to narrow on, and a
		// negative criterion narrows the row it looks for, not the items.
		{`{"@k":{"$match":"*a"}}`, `p.key = ? AND (p.value COLLATE "C" LIKE`, []any{"k", "%a"}},
		{
			`{"@k":{"$ne":"v"}}`,
			`NOT EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = items.id ` +
				`AND p.key = ? AND left(p.value, 255) = left(?, 255) AND p.value = ?)`,
			[]any{"k", "v", "v"},
		},
		{
			`{"property.value":"v"}`,
			`items.id AND left(p.value, 255) = left(?, 255) AND p.value = ?)`,
			[]any{"v", "v"},
		},
		{`{"property.key":"k"}`, `items.id AND p.key = ?)`, []any{"k"}},
	}
	for _, c := range cases {
		q := mustParse(t, `items.find(`+c.criteria+`)`)
		pg, err := q.Compile(PostgreSQL, Options{})
		if err != nil {
			t.Fatal(err)
		}
		// items.find adds a criterion on the type, bound last.
		if !strings.Contains(pg.Where, c.want) || !slices.Equal(pg.Args, append(c.args, "file")) {
			t.Errorf("%s:\n%s %q\nwant %s %q", c.criteria, pg.Where, pg.Args, c.want, c.args)
		}
		if lite, _ := q.Compile(SQLite, Options{}); strings.Contains(lite.Where, "left(") {
			t.Errorf("%s: SQLite narrows on a prefix: %s", c.criteria, lite.Where)
		}
	}
}

func TestUnitCompileSecondaryDomains(t *testing.T) {
	q := mustParse(t, `items.find({"$msp":[{"@l":{"$nmatch":"a*"}},{"@*":{"$ne":"*"}},`+
		`{"property.key":{"$gt":"b"}}],"stat.downloaded_by":{"$match":"a*"},`+
		`"stat.downloads":{"$eq":null},"stat.remote_path":{"$lt":"z"}})`)
	pg, err := q.Compile(PostgreSQL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = items.id AND p.key = ? AND ` +
			`NOT (p.value COLLATE "C" LIKE ? ESCAPE '\') AND 1=0 AND p.key COLLATE "C" > ?)`,
		`((SELECT s.last_downloaded_by FROM item_stats s WHERE s.item_id = items.id) COLLATE "C" LIKE ? ESCAPE '\')`,
		`(SELECT s.download_count FROM item_stats s WHERE s.item_id = items.id) IS NULL`,
		`(SELECT s.remote_path FROM item_stats s WHERE s.item_id = items.id) COLLATE "C" < ?`,
	} {
		if !strings.Contains(pg.Where, want) {
			t.Errorf("postgres where lacks\n%s\nin\n%s", want, pg.Where)
		}
	}
	lite, _ := q.Compile(SQLite, Options{})
	if !strings.Contains(
		lite.Where,
		`(SELECT s.remote_path FROM item_stats s WHERE s.item_id = items.id) < ?`,
	) ||
		!strings.Contains(lite.Where, `NOT (p.value GLOB ?)`) {
		t.Fatalf("sqlite where %s", lite.Where)
	}
}

func TestUnitMatchStatEdges(t *testing.T) {
	st := &types.ItemStats{DownloadCount: 1}
	for src, want := range map[string]bool{
		`{"stat.downloaded":{"$eq":null}}`:          true,
		`{"stat.downloaded":{"$ne":null}}`:          false,
		`{"stat.downloaded":{"$lt":"2030"}}`:        false,
		`{"stat.downloaded_by":{"$ne":null}}`:       true,
		`{"stat.downloaded_by":""}`:                 true,
		`{"stat.remote_downloads":0}`:               true,
		`{"stat.remote_origin":{"$nmatch":"x"}}`:    true,
		`{"stat.remote_downloaded_by":{"$lt":"a"}}`: true,
	} {
		q := mustParse(t, `items.find(`+src+`)`)
		if got := match(q.Criteria.(And)[0], Hit{Stats: st}, time.Now()); got != want {
			t.Errorf("%s: %v", src, got)
		}
	}
}

func TestUnitResponseSecondaryDomains(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 678e6, time.UTC)
	hit := Hit{
		Item:         types.Item{RepoKey: "r", Path: "a", Name: "f"},
		Properties:   types.Properties{"b": {"1"}, "a": {"x"}, "c": {"y"}},
		Stats:        &types.ItemStats{DownloadCount: 2, LastDownloaded: at, RemoteOrigin: "o"},
		VirtualRepos: []string{"v1", "v2"},
	}
	for query, want := range map[string]string{
		`items.find().include("repo","path","name","virtual_repos","@c","@a","stat")`: `{"repo":"r",` +
			`"path":"a","name":"f","virtual_repos":["v1","v2"],"properties":[{"key":"a","value":"x"},` +
			`{"key":"c","value":"y"}],"stats":[{"downloaded":"2026-01-02T03:04:05.678Z","downloads":2,` +
			`"remote_downloads":0,"remote_origin":"o"}]}`,
		`items.find().include("name","stat.downloads","@z")`: `{"name":"f","stats":[{"downloads":2}]}`,
		// "@" and "@*" render every property, as "property.*" does (#506).
		`items.find().include("name","@")`: `{"name":"f","properties":[{"key":"a","value":"x"},` +
			`{"key":"b","value":"1"},{"key":"c","value":"y"}]}`,
		`items.find().include("name","@*","@c")`: `{"name":"f","properties":[{"key":"a","value":"x"},` +
			`{"key":"b","value":"1"},{"key":"c","value":"y"}]}`,
		// A record with none of the included values renders no stats row,
		// rather than an empty one (#506).
		`items.find().include("name","stat.remote_downloaded_by")`: `{"name":"f"}`,
	} {
		b, _ := json.Marshal(mustParse(t, query).Response([]Hit{hit}).Rows[0])
		if string(b) != want {
			t.Errorf("%s:\ngot  %s\nwant %s", query, b, want)
		}
	}
	// No virtual repositories render as an empty list; no statistics record
	// renders no stats.
	bare := Hit{Item: hit.Item}
	q := mustParse(t, `items.find().include("repo","path","name","virtual_repos","stat")`)
	b, _ := json.Marshal(q.Response([]Hit{bare}).Rows[0])
	if string(b) != `{"repo":"r","path":"a","name":"f","virtual_repos":[]}` {
		t.Fatalf("bare row %s", b)
	}
}

// "@" and "@*" include every property and filter nothing, so the criteria
// are a property.* include's; beside an "@<key>" include the key still
// filters, and every key still renders (#506). "@" is how JFrog's "Sorting
// in AQL" example asks for an item's properties.
func TestUnitEveryKeyIncludes(t *testing.T) {
	plain := mustParse(t, `items.find({"repo":"r"}).include("name","property.*")`)
	for _, inc := range []string{"@", "@*"} {
		q := mustParse(t, `items.find({"repo":"r"}).include("name","`+inc+`")`)
		if !q.Properties() || q.PropertyKeys() != nil ||
			!reflect.DeepEqual(q.Criteria, plain.Criteria) {
			t.Errorf(
				"%s: properties %v, keys %v, criteria %v",
				inc,
				q.Properties(),
				q.PropertyKeys(),
				q.Criteria,
			)
		}
		mustParse(t, `items.find({"name":{"$match":"*.jar"}}).include("`+inc+`")`+
			`.sort({"$asc":["repo","name"]})`)
		q = mustParse(t, `items.find().include("`+inc+`","@release")`)
		if q.PropertyKeys() != nil || !slices.Equal(q.keyIncludes(), []string{"release"}) {
			t.Errorf(
				"%s beside @release: keys %v, filters %v",
				inc,
				q.PropertyKeys(),
				q.keyIncludes(),
			)
		}
	}
}

// TestUnitParseOutputRules covers the refusals of research 22 §1.2's output
// rules and the longer secondary-domain errors.
func TestUnitParseOutputRules(t *testing.T) {
	for src, want := range map[string]error{
		`items.find().include("name").sort({"$asc":["size"]})`:                    ErrInvalid,
		`items.find().include("name","property").sort({"$asc":["size"]})`:         ErrInvalid,
		`items.find().include("property").sort({"$asc":["sha256"]}).offset(1)`:    ErrInvalid,
		`items.find().include("stat").sort({"$asc":["stat.downloads"]}).limit(1)`: ErrUnsupported,
		`items.find().include("virtual_repos")`:                                   ErrInvalid,
		`items.find().include("repo","name","virtual_repos")`:                     ErrInvalid,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%s) = %v, want %v", src, err, want)
		}
	}
}

// TestUnitParseModifiersBesideSubDomainIncludes: sort, offset and limit beside
// a property or statistic include are accepted, as Artifactory accepts them
// (aql-query-output), and keep their values for the item query. The first
// query is the one jf rt s --offset 1 sends (jfrog-client-go
// includePropertiesInAqlForSpec keeps "property" without a sort or limit).
func TestUnitParseModifiersBesideSubDomainIncludes(t *testing.T) {
	type want struct {
		sort                  bool
		offset, limit         int
		hasOffset, hasLimit   bool
		properties, statistic bool
	}
	for src, w := range map[string]want{
		`items.find({"$or":[{"$and":[{"repo":"libs-local","path":{"$match":"*"},"name":{"$match":"*"}}]}]})` +
			`.include("name","repo","path","actual_md5","actual_sha1","sha256","size","type","modified",` +
			`"created","property").offset(1)`: {offset: 1, hasOffset: true, properties: true},
		`items.find().include("property").limit(1)`:      {limit: 1, hasLimit: true, properties: true},
		`items.find().include("property.key").offset(0)`: {hasOffset: true, properties: true},
		`items.find().include("@k").offset(1)`:           {offset: 1, hasOffset: true, properties: true},
		`items.find().include("stat").sort({"$asc":["name"]})`: {
			sort: true, statistic: true,
		},
		`items.find().include("stat.downloads").limit(1)`: {limit: 1, hasLimit: true, statistic: true},
		`items.find().include("name","property","stat").sort({"$desc":["name"]}).offset(2).limit(3)`: {
			sort: true, offset: 2, hasOffset: true, limit: 3, hasLimit: true,
			properties: true, statistic: true,
		},
	} {
		q := mustParse(t, src)
		got := want{
			sort: q.Sort != nil, offset: q.Offset, limit: q.Limit,
			hasOffset: q.HasOffset, hasLimit: q.HasLimit,
			properties: q.Properties(), statistic: len(q.Stats()) > 0,
		}
		if got != w {
			t.Errorf("Parse(%s) = %+v, want %+v", src, got, w)
		}
	}
}

func TestUnitParseActions(t *testing.T) {
	// JFrog's documented examples (aql-syntax, aql-examples).
	q := mustParse(t, `items.delete({"name" : {"$match" : "*.tmp"}}).dryRun("true")`)
	if q.Action != DeleteItems || !q.DryRun || q.Action.String() != "items.delete" {
		t.Fatalf("query %+v", q)
	}
	// delete selects files unless the criteria name a type, as find does.
	and, _ := q.Criteria.(And)
	if len(and) != 2 || and[1].(*FieldCriterion).Text != "file" {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	q = mustParse(
		t,
		`items.delete({"name":{"$match":"*.tmp"},"repo":"temp-local"}).dryRun("false")`,
	)
	if q.DryRun || !slices.Equal(q.Fields(), defaultFields) || q.Properties() {
		t.Fatalf("query %+v", q)
	}
	q = mustParse(
		t,
		`properties.update({"item.repo":"libs-release-local","item.name":{"$match":"*.jar"}})`+
			`.keys("status").newValue("approved").dryRun("false")`,
	)
	if q.Action != UpdateProperties || q.DryRun || !slices.Equal(q.Keys, []string{"status"}) ||
		q.NewValue != "approved" || q.Action.String() != "properties.update" {
		t.Fatalf("query %+v", q)
	}
	// item.<field> criteria become item fields; there is no implicit
	// type=file, and an item must have one of the keys.
	and, _ = q.Criteria.(And)
	if len(and) != 3 || and[0].(*FieldCriterion).Field != FieldRepo ||
		and[1].(*FieldCriterion).Field != FieldName {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	if has, _ := and[2].(Or); len(has) != 1 || has[0].(*PropertyCriterion).Key != "status" {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// Rows carry the keys being changed and nothing else.
	if !q.Properties() || !slices.Equal(q.PropertyKeys(), []string{"status"}) {
		t.Fatalf("properties %v %v", q.Properties(), q.PropertyKeys())
	}
	// No .dryRun() is a dry run; booleans and any case are accepted, keys
	// are deduplicated, and $and and $or reach item fields inside them.
	for src, dry := range map[string]bool{
		`items.delete()`:                             true,
		`items.delete().dryRun(false)`:               false,
		`items.delete().dryRun(true)`:                true,
		`items.delete().dryRun("FALSE")`:             false,
		`items.delete({"type":"folder"})`:            true,
		`properties.update().keys("a").newValue("")`: true,
	} {
		if q := mustParse(t, src); q.DryRun != dry {
			t.Errorf("%s: dry run %v", src, q.DryRun)
		}
	}
	q = mustParse(
		t,
		`properties.update({"$or":[{"item.name":"a"},{"$and":[{"item.type":"folder"}]}]})`+
			`.newValue("v").keys("b","a","b")`,
	)
	if !slices.Equal(q.Keys, []string{"b", "a"}) {
		t.Fatalf("keys %v", q.Keys)
	}
	and, _ = q.Criteria.(And)
	or, _ := and[0].(Or)
	if len(or) != 2 || or[0].(*FieldCriterion).Field != FieldName ||
		or[1].(And)[0].(*FieldCriterion).Field != FieldType {
		t.Fatalf("criteria %#v", q.Criteria)
	}
	// find is no action and runs for real.
	q = mustParse(t, `items.find()`)
	if q.Action != Find || q.DryRun || q.Action.String() != "items.find" {
		t.Fatalf("find %+v", q)
	}
}

func TestUnitParseActionErrors(t *testing.T) {
	for src, want := range map[string]error{
		`properties.find()`:                                                    ErrUnsupported,
		`properties.search()`:                                                  ErrSyntax,
		`items.delete([])`:                                                     ErrSyntax,
		`items.delete().limit(1)`:                                              ErrInvalid,
		`items.delete().sort({"$asc":["name"]})`:                               ErrInvalid,
		`items.delete().include("name")`:                                       ErrInvalid,
		`items.delete().keys("a")`:                                             ErrInvalid,
		`items.find().dryRun("true")`:                                          ErrInvalid,
		`items.delete().dryRun()`:                                              ErrSyntax,
		`items.delete().dryRun("yes")`:                                         ErrSyntax,
		`items.delete().dryRun(1)`:                                             ErrSyntax,
		`items.delete().dryRun("true").dryRun("false")`:                        ErrSyntax,
		`items.delete().explode()`:                                             ErrSyntax,
		`properties.update().keys("a")`:                                        ErrInvalid,
		`properties.update().newValue("v")`:                                    ErrInvalid,
		`properties.update().keys().newValue("v")`:                             ErrSyntax,
		`properties.update().keys("").newValue("v")`:                           ErrSyntax,
		`properties.update().keys(1).newValue("v")`:                            ErrSyntax,
		`properties.update().keys("a").newValue(1)`:                            ErrSyntax,
		`properties.update().keys("a").newValue("v","w")`:                      ErrSyntax,
		`properties.update().keys("a").newValue("v").limit(1)`:                 ErrInvalid,
		`properties.update({"key":"a"}).keys("a").newValue("v")`:               ErrUnsupported,
		`properties.update({"value":"a"}).keys("a").newValue("v")`:             ErrUnsupported,
		`properties.update({"@a":"b"}).keys("a").newValue("v")`:                ErrUnsupported,
		`properties.update({"$msp":[]}).keys("a").newValue("v")`:               ErrUnsupported,
		`properties.update({"item.nosuch":"a"}).keys("a").newValue("v")`:       ErrUnsupported,
		`properties.update({"item.stat.downloads":1}).keys("a").newValue("v")`: ErrUnsupported,
		`properties.update({"name":"a"}).keys("a").newValue("v")`:              ErrSyntax,
		`properties.update({"$or":[{"key":"a"}]}).keys("a").newValue("v")`:     ErrUnsupported,
		`properties.update({"$or":["a"]}).keys("a").newValue("v")`:             ErrSyntax,
		`properties.update({"$not":[]}).keys("a").newValue("v")`:               ErrSyntax,
		`properties.update({"item.size":"big"}).keys("a").newValue("v")`:       ErrSyntax,
		`properties.update().keys("` + strings.Repeat("k", types.PropertyKeyMax+1) +
			`").newValue("v")`: ErrInvalid,
	} {
		if _, err := Parse(src); !errors.Is(err, want) {
			t.Errorf("Parse(%s) = %v, want %v", src, err, want)
		}
	}
}
