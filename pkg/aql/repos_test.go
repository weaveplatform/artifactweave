package aql

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitResolve(t *testing.T) {
	repos := Repositories{
		"libs":    {Class: RepoLocal},
		"x-cache": {Class: RepoLocal},
		"rem":     {Class: RepoRemote},
		"virt":    {Class: RepoVirtual, Members: []string{"libs", "rem", "libs"}},
		"empty":   {Class: RepoVirtual},
	}
	resolved := func(src string) *Query {
		t.Helper()
		q := mustParse(t, src)
		q.Resolve(repos)
		return q
	}
	repoSet := func(q *Query) RepoSet {
		t.Helper()
		s, ok := q.Criteria.(And)[0].(RepoSet)
		if !ok {
			t.Fatalf("criteria %#v", q.Criteria)
		}
		return s
	}
	// A virtual key's members are listed once, in key order.
	q := resolved(`items.find({"repo":"virt"})`)
	if got := repoSet(
		q,
	).Refs; !slices.Equal(
		got,
		[]RepoRef{{"libs", false}, {"rem", false}, {"rem", true}},
	) {
		t.Fatalf("virtual %v", got)
	}
	// The implied virtual_repos is added to the default fields, not in place
	// of them, which an explicit include of virtual_repos alone would do.
	if !q.VirtualRepos() || !slices.Equal(q.Fields(), defaultFields) {
		t.Fatalf("implied virtual_repos %v %v", q.VirtualRepos(), q.Fields())
	}
	if q := resolved(
		`items.find({"repo":{"$ne":"virt"}})`,
	); q.VirtualRepos() ||
		!repoSet(q).Negate {
		t.Fatal("$ne of a virtual")
	}
	// A configured local's key is its own, even with the cache suffix; an
	// unconfigured one still names a cache.
	if got := repoSet(
		resolved(`items.find({"repo":"x-cache"})`),
	).Refs; !slices.Equal(
		got,
		[]RepoRef{{"x-cache", false}},
	) {
		t.Fatalf("local x-cache %v", got)
	}
	if got := repoSet(
		resolved(`items.find({"repo":"y-cache"})`),
	).Refs; !slices.Equal(
		got,
		[]RepoRef{{"y", true}},
	) {
		t.Fatalf("unconfigured y-cache %v", got)
	}
	// Ordering operators, patterns no configured key matches and other fields
	// keep their criterion.
	for _, src := range []string{
		`items.find({"repo":{"$gt":"rem"}})`,
		`items.find({"repo":{"$match":"zz*"}})`,
		`items.find({"name":"rem"})`,
	} {
		if _, ok := resolved(src).Criteria.(And)[0].(*FieldCriterion); !ok {
			t.Errorf("%s was rewritten", src)
		}
	}
	// Resolution reaches into $or and leaves a hand-built query without
	// criteria alone.
	q = resolved(`items.find({"$or":[{"repo":"rem","name":"a"}]})`)
	if _, ok := q.Criteria.(And)[0].(Or)[0].(RepoSet); !ok {
		t.Fatalf("$or %#v", q.Criteria)
	}
	(&Query{}).Resolve(repos)
	// Refs sort by key, then the stored repository before its cache, so the
	// compiled SQL is stable.
	if got := uniqueRefs(
		[]RepoRef{{"b", true}, {"b", false}, {"a", true}, {"b", true}},
	); !slices.Equal(
		got,
		[]RepoRef{{"a", true}, {"b", false}, {"b", true}},
	) {
		t.Fatalf("uniqueRefs %v", got)
	}

	// A virtual repository without members matches nothing, and its $ne
	// everything.
	it := Hit{Item: types.Item{RepoKey: "libs", Name: "a"}}
	for src, want := range map[string]bool{
		`items.find({"repo":"empty"})`:            false,
		`items.find({"repo":{"$ne":"empty"}})`:    true,
		`items.find({"repo":{"$match":"emp*"}})`:  false,
		`items.find({"repo":{"$nmatch":"emp*"}})`: true,
		`items.find({"repo":{"$match":"li*"}})`:   true,
		`items.find({"repo":{"$nmatch":"li*"}})`:  false,
		`items.find({"repo":{"$match":"vi*"}})`:   true,
		`items.find({"repo":{"$nmatch":"vi*"}})`:  false,
	} {
		if got := len(Select(resolved(src), Options{}, []Hit{it})) == 1; got != want {
			t.Errorf("%s: %v", src, got)
		}
	}
}

func TestUnitCompileRepoSet(t *testing.T) {
	for _, tc := range []struct {
		set   RepoSet
		where string
		args  []any
	}{
		{RepoSet{}, "1=0", nil},
		{RepoSet{Negate: true}, "1=1", nil},
		{
			RepoSet{Refs: []RepoRef{{"a", true}}},
			"((items.cached = ? AND items.repo_key IN (?)))",
			[]any{true, "a"},
		},
		{
			RepoSet{Refs: []RepoRef{{"a", false}, {"b", true}, {"c", false}}, Negate: true},
			"NOT ((items.cached = ? AND items.repo_key IN (?, ?)) OR " +
				"(items.cached = ? AND items.repo_key IN (?)))",
			[]any{false, "a", "c", true, "b"},
		},
	} {
		c, err := (&Query{Criteria: tc.set}).Compile(PostgreSQL, Options{})
		if err != nil || c.Where != tc.where || !slices.Equal(c.Args, tc.args) {
			t.Errorf("%+v: %q %v %v", tc.set, c.Where, c.Args, err)
		}
	}
}

func TestUnitExpand(t *testing.T) {
	repos := Repositories{
		"libs": {Class: RepoLocal},
		"rem":  {Class: RepoRemote},
		"virt": {Class: RepoVirtual, Members: []string{"rem", "libs", "virt"}},
	}
	for key, want := range map[string][]RepoRef{
		"virt":      {{"libs", false}, {"rem", false}, {"rem", true}},
		"rem-cache": {{"rem", true}},
		"nope":      {{"nope", false}},
	} {
		if got := repos.Expand(key); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", key, got, want)
		}
	}
}
