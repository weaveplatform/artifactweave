package storetest

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testLongPropertyValues stores property values at and past the limits and
// finds them by equality, wildcard and range. Values longer than the limit
// are cut to it. The values are random text (LongText), so PostgreSQL can
// neither fit one in a B-tree entry nor compress it to fit, as it would a
// repeated character.
func testLongPropertyValues(t *testing.T, st store.Store) {
	ctx := context.Background()
	s := st.Items()
	base := LongText(types.PropertyValueMax - 1)
	// v1 and v2 share their first 3999 characters, far past any indexed
	// prefix. v3 leaves base early with "!", which sorts below every
	// character LongText uses, so v3 < v1 < v2.
	v1 := base + "a"
	v2 := base + "b"
	v3 := string([]rune(base)[:300]) + "!"
	props := get[types.Properties](t)
	set := map[string]types.Properties{
		"r/a.txt": {"notes": {v1}},
		"r/b.txt": {"notes": {v3}},
		// Past the limit: c's value becomes v1, and d's two values become
		// one.
		"r/c.txt": {"notes": {v1 + "past the limit"}},
		"r/d.txt": {"notes": {v2, v2 + "x"}},
	}
	want := map[string]types.Properties{
		"r/a.txt": {"notes": {v1}},
		"r/b.txt": {"notes": {v3}},
		"r/c.txt": {"notes": {v1}},
		"r/d.txt": {"notes": {v2}},
	}
	ids := map[string]int64{}
	for n, path := range []string{"r/a.txt", "r/b.txt", "r/c.txt", "r/d.txt"} {
		it := get[types.Item](t)(s.Put(ctx, fileItem("r", strings.TrimPrefix(path, "r/"), n+1)))
		ids[path] = it.ID
		must(t, s.SetProperties(ctx, it.ID, set[path]))
		same(t, "SetProperties "+path, want[path], props(s.Properties(ctx, it.ID)))
	}
	// A patch cuts too; a surrogate pair that would straddle the limit goes
	// whole.
	must(t, s.PatchProperties(ctx, ids["r/b.txt"], types.Properties{"pair": {base + "😀"}}, nil))
	same(t, "PatchProperties", types.Properties{"notes": {v3}, "pair": {base}},
		props(s.Properties(ctx, ids["r/b.txt"])))

	quote := func(v string) string {
		b, err := json.Marshal(v)
		must(t, err)
		return string(b)
	}
	find := func(criteria string) []string {
		t.Helper()
		q, err := aql.Parse(`items.find(` + criteria + `)`)
		must(t, err)
		hits, err := s.Search(ctx, q, aql.Options{})
		must(t, err)
		out := make([]string, len(hits))
		for n, h := range hits {
			out[n] = h.Item.RepoKey + "/" + h.Item.FullPath()
		}
		return out
	}
	for _, c := range []struct {
		name, criteria string
		want           []string
	}{
		{"eq", `{"@notes":` + quote(v1) + `}`, []string{"r/a.txt", "r/c.txt"}},
		{"eq past the limit", `{"@notes":` + quote(v1+"past the limit") + `}`, nil},
		{"ne", `{"@notes":{"$ne":` + quote(v1) + `}}`, []string{"r/b.txt", "r/d.txt"}},
		{"match", `{"@notes":{"$match":` + quote(string([]rune(base)[:10])+"*b") + `}}`, []string{"r/d.txt"}},
		{
			"match a long literal", `{"@notes":{"$match":` + quote(v2[:len(v2)-1]+"?") + `}}`,
			[]string{"r/a.txt", "r/c.txt", "r/d.txt"},
		},
		{"nmatch", `{"@notes":{"$nmatch":"*b"}}`, []string{"r/a.txt", "r/b.txt", "r/c.txt"}},
		{"gt", `{"@notes":{"$gt":` + quote(v1) + `}}`, []string{"r/d.txt"}},
		{"gte", `{"@notes":{"$gte":` + quote(v1) + `}}`, []string{"r/a.txt", "r/c.txt", "r/d.txt"}},
		{"lt", `{"@notes":{"$lt":` + quote(v2) + `}}`, []string{"r/a.txt", "r/b.txt", "r/c.txt"}},
		{"lte", `{"@notes":{"$lte":` + quote(v3) + `}}`, []string{"r/b.txt"}},
		{
			"msp", `{"$msp":[{"property.key":"notes","property.value":` + quote(v2) + `}]}`,
			[]string{"r/d.txt"},
		},
	} {
		if got := find(c.criteria); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// Build and module properties are cut to types.BuildPropertyValueMax.
	long := LongText(types.BuildPropertyValueMax)
	short := string([]rune(long)[:types.BuildPropertyValueMax-1])
	b := fxBuild("", "long", "1", 0)
	b.Properties = types.Properties{"notes": {long + "past", long}}
	b.Modules[0].Properties = types.Properties{"notes": {short + "😀"}}
	run := get[types.Build](t)(st.Builds().Put(ctx, b))
	got := get[types.Build](t)(st.Builds().ByID(ctx, run.ID))
	same(t, "build properties", types.Properties{"notes": {long}}, got.Properties)
	same(t, "module properties", types.Properties{"notes": {short}}, got.Modules[0].Properties)
	for _, criteria := range []string{
		`{"@notes":` + quote(long) + `}`,
		`{"module.@notes":{"$match":` + quote(string([]rune(short)[:5])+"*") + `}}`,
	} {
		q, err := aql.Parse(`builds.find(` + criteria + `)`)
		must(t, err)
		hits, err := st.Builds().Search(ctx, q, aql.Options{})
		must(t, err)
		if len(hits) != 1 || hits[0].Build.ID != run.ID {
			t.Errorf("builds.find(%.40s…): %d hits", criteria, len(hits))
		}
	}
}

// LongText returns n characters of random text, the same on every call. Half
// the characters are two or three bytes of UTF-8 and each is one UTF-16 code
// unit; all sort above "!" and none is an AQL wildcard.
func LongText(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789" +
		"абвгдежзийклмнопрстуфхцчш" + "中文字日本語漢字表示€"
	runes := []rune(alphabet)
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test data, not a secret
	var b strings.Builder
	for range n {
		b.WriteRune(runes[rng.IntN(len(runes))])
	}
	return b.String()
}
