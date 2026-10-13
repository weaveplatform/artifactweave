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

func mustParse(t *testing.T, src string) *Query {
	t.Helper()
	q, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%s): %v", src, err)
	}
	return q
}

func TestParseModifiers(t *testing.T) {
	q := mustParse(t, "  items . find ( { } )\n.include(\"name\", \"property\")\n"+
		`.sort({"$desc":["size","name"]}).offset(3).transitive().limit(7).distinct(false)`)
	if !slices.Equal(q.Include, []string{"name", "property"}) || q.Sort == nil || !q.Sort.Desc ||
		!slices.Equal(q.Sort.Fields, []Field{FieldSize, FieldName}) || q.Offset != 3 ||
		q.Limit != 7 || !q.HasLimit || !q.Transitive || q.Distinct {
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

func TestParseErrors(t *testing.T) {
	deep := `items.find(` + strings.Repeat(`{"$and":[`, maxDepth+2) + `{}` +
		strings.Repeat(`]}`, maxDepth+2) + `)`
	deeper := `items.find(` + strings.Repeat(`[`, maxValueDepth+2) + `)`
	for src, want := range map[string]error{
		"":                                                ErrSyntax,
		`"items"`:                                         ErrSyntax,
		`builds.find()`:                                   ErrUnsupported,
		`widgets.find()`:                                  ErrSyntax,
		`items.delete({})`:                                ErrUnsupported,
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
		`items.find({"stat.downloads":1})`:                ErrUnsupported,
		`items.find({"$msp":[]})`:                         ErrUnsupported,
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
		`items.find().include("stat")`:                    ErrUnsupported,
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
		err.Error() != "AQL query is too long" {
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

func TestTimesAndSpans(t *testing.T) {
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

func TestGlobTranslations(t *testing.T) {
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

func TestCompile(t *testing.T) {
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
	if c, _ := (&Query{}).Compile(SQLite, Options{Limit: 5}); c.Where != "1=1" ||
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

func TestMatchEdges(t *testing.T) {
	it := types.Item{Name: "a", Size: 3}
	if (&Query{Criteria: badNode{}}).Match(it, nil, time.Now()) {
		t.Fatal("an unknown node matched")
	}
	if !(&Query{}).Match(it, nil, time.Now()) {
		t.Fatal("no criteria must match everything")
	}
	if compareOp(OpMatch, 0) {
		t.Fatal("compareOp with a non-ordering operator")
	}
	q := mustParse(t, `items.find({"type":{"$ne":"any"}})`)
	if q.Match(it, nil, time.Now()) {
		t.Fatal(`type $ne "any" matched`)
	}
	c, _ := q.Compile(SQLite, Options{})
	if c.Where != "1=0" {
		t.Fatalf("where %s", c.Where)
	}
}

func TestFieldsAndRestrictions(t *testing.T) {
	for include, want := range map[string][]Field{
		``:                          defaultFields,
		`.include("property")`:      defaultFields,
		`.include("sha256","repo")`: {FieldRepo, FieldSHA256},
		`.include("*")`:             fieldOrder,
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
	for _, inc := range []string{"property", "property.*", "property.key", "property.value"} {
		if !mustParse(t, `items.find().include("`+inc+`")`).Properties() {
			t.Errorf("include(%s) does not return properties", inc)
		}
	}
}

func TestResponse(t *testing.T) {
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
	q := mustParse(t, `items.find().include("*","property").offset(4).limit(2)`)
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

func TestWindow(t *testing.T) {
	q := &Query{Offset: 1}
	if o, l := q.Window(Options{}); o != 1 || l != -1 {
		t.Fatal(o, l)
	}
	if _, l := q.Window(Options{Limit: 9}); l != 9 {
		t.Fatal(l)
	}
	q.Limit, q.HasLimit = 0, true
	if _, l := q.Window(Options{Limit: 9}); l != 0 {
		t.Fatal(l)
	}
	if (Options{}).now().IsZero() {
		t.Fatal("zero clock")
	}
}
