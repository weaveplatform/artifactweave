package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// testLongPropertyValues stores property values at and past the limits and
// finds them by equality, wildcard and range. Values longer than the limit
// are cut to it. The values are random text (LongText), so PostgreSQL can
// neither fit one in a B-tree entry nor compress it to fit, as it would a
// repeated character.
func testLongPropertyValues(t *testing.T, st port.Store) {
	ctx := context.Background()
	s := st.Items()
	base := LongText(model.PropertyValueMax - 1)
	// v1 and v2 share every character but their last, far past any indexed
	// prefix. v3 leaves base early with "!", which sorts below every
	// character LongText uses, so v3 < v1 < v2.
	v1 := base + "a"
	v2 := base + "b"
	v3 := string([]rune(base)[:300]) + "!"
	props := storekit.Get[model.Properties](t)
	set := map[string]model.Properties{
		"r/a.txt": {"notes": {v1}},
		"r/b.txt": {"notes": {v3}},
		// Past the limit: c's value becomes v1, and d's two values become
		// one.
		"r/c.txt": {"notes": {v1 + "past the limit"}},
		"r/d.txt": {"notes": {v2, v2 + "x"}},
	}
	want := map[string]model.Properties{
		"r/a.txt": {"notes": {v1}},
		"r/b.txt": {"notes": {v3}},
		"r/c.txt": {"notes": {v1}},
		"r/d.txt": {"notes": {v2}},
	}
	ids := map[string]int64{}
	for n, path := range []string{"r/a.txt", "r/b.txt", "r/c.txt", "r/d.txt"} {
		it := storekit.Get[model.Item](
			t,
		)(
			s.Put(ctx, fileItem("r", strings.TrimPrefix(path, "r/"), n+1)),
		)
		ids[path] = it.ID
		storekit.Must(t, s.SetProperties(ctx, it.ID, set[path], storekit.At(1)))
		storekit.Same(t, "SetProperties "+path, want[path], props(s.Properties(ctx, it.ID)))
	}
	// A patch cuts too; a surrogate pair that would straddle the limit goes
	// whole.
	storekit.Must(
		t,
		s.PatchProperties(
			ctx,
			ids["r/b.txt"],
			model.Properties{"pair": {base + "😀"}},
			nil,
			storekit.At(2),
		),
	)
	storekit.Same(t, "PatchProperties", model.Properties{"notes": {v3}, "pair": {base}},
		props(s.Properties(ctx, ids["r/b.txt"])))

	quote := func(v string) string {
		b, err := json.Marshal(v)
		storekit.Must(t, err)
		return string(b)
	}
	find := func(criteria string) []string {
		t.Helper()
		q, err := aql.Parse(`items.find(` + criteria + `)`)
		storekit.Must(t, err)
		hits, err := s.Search(ctx, q, aql.Options{})
		storekit.Must(t, err)
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

	// Build and module properties are cut to model.BuildPropertyValueMax.
	long := LongText(model.BuildPropertyValueMax)
	short := string([]rune(long)[:model.BuildPropertyValueMax-1])
	b := fxBuild("", "long", "1", 0)
	b.Properties = model.Properties{"notes": {long + "past", long}}
	b.Modules[0].Properties = model.Properties{"notes": {short + "😀"}}
	run := storekit.Get[model.Build](t)(st.Builds().Put(ctx, b))
	got := storekit.Get[model.Build](t)(st.Builds().ByID(ctx, run.ID))
	storekit.Same(t, "build properties", model.Properties{"notes": {long}}, got.Properties)
	storekit.Same(
		t,
		"module properties",
		model.Properties{"notes": {short}},
		got.Modules[0].Properties,
	)
	for _, criteria := range []string{
		`{"@notes":` + quote(long) + `}`,
		`{"module.@notes":{"$match":` + quote(string([]rune(short)[:5])+"*") + `}}`,
	} {
		q, err := aql.Parse(`builds.find(` + criteria + `)`)
		storekit.Must(t, err)
		hits, err := st.Builds().Search(ctx, q, aql.Options{})
		storekit.Must(t, err)
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

// testLongPropertyKeys stores keys of model.PropertyKeyMax characters and
// refuses longer ones with ErrInvalid, writing nothing. The characters are
// four bytes of UTF-8 each, and the value beside them is as long as a value
// may be, which is the most a PostgreSQL index entry over a key and a
// value's prefix has to hold.
func testLongPropertyKeys(t *testing.T, st port.Store) {
	ctx := context.Background()
	s := st.Items()
	key := wideText(model.PropertyKeyMax)
	long := wideText(model.PropertyKeyMax + 1)
	value := wideText(model.PropertyValueMax / 2)
	props := storekit.Get[model.Properties](t)
	tooLong := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, kernel.ErrInvalid) || !errors.Is(err, model.ErrPropertyKeyTooLong) {
			t.Fatalf("%s: err = %v, want ErrInvalid and ErrPropertyKeyTooLong", what, err)
		}
	}

	it := storekit.Get[model.Item](t)(s.Put(ctx, fileItem("r", "keys.txt", 1)))
	want := model.Properties{key: {value}}
	storekit.Must(t, s.SetProperties(ctx, it.ID, want, storekit.At(1)))
	storekit.Same(t, "a key at the limit", want, props(s.Properties(ctx, it.ID)))
	tooLong(
		"SetProperties",
		s.SetProperties(ctx, it.ID, model.Properties{"ok": {"1"}, long: {"1"}}, storekit.At(2)),
	)
	tooLong(
		"PatchProperties",
		s.PatchProperties(ctx, it.ID, model.Properties{long: {"1"}}, []string{key}, storekit.At(2)),
	)
	storekit.Same(t, "after the refused writes", want, props(s.Properties(ctx, it.ID)))

	bs := st.Builds()
	b := fxBuild("", "keys", "1", 0)
	b.Properties = model.Properties{long: {"v"}}
	_, err := bs.Put(ctx, b)
	tooLong("Put with a build property", err)
	b.Properties = model.Properties{key: {"v"}}
	b.Modules[0].Properties = model.Properties{long: {"v"}}
	_, err = bs.Put(ctx, b)
	tooLong("Put with a module property", err)
	_, err = bs.Latest(ctx, "", "keys")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	moduleValue := wideText(model.BuildPropertyValueMax / 2)
	b.Modules[0].Properties = model.Properties{key: {moduleValue}}
	run := storekit.Get[model.Build](t)(bs.Put(ctx, b))
	got := storekit.Get[model.Build](t)(bs.ByID(ctx, run.ID))
	storekit.Same(t, "build properties", model.Properties{key: {"v"}}, got.Properties)
	storekit.Same(
		t,
		"module properties",
		model.Properties{key: {moduleValue}},
		got.Modules[0].Properties,
	)
	changed := got
	changed.Properties = model.Properties{long: {"v"}}
	tooLong("Replace", bs.Replace(ctx, changed))
	storekit.Same(t, "after the refused Replace", model.Properties{key: {"v"}},
		storekit.Get[model.Build](t)(bs.ByID(ctx, run.ID)).Properties)
}

// wideText returns n random characters of four bytes of UTF-8 each (emoji,
// so two UTF-16 code units each), the same on every call.
func wideText(n int) string {
	rng := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // test data, not a secret
	var b strings.Builder
	for range n {
		//nolint:gosec // G115: 0x1F600 plus under 80 is far inside rune's range.
		b.WriteRune(rune(0x1F600 + rng.IntN(80)))
	}
	return b.String()
}
