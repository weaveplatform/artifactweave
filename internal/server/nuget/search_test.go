package nuget_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/server/nuget"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// searchDoc is a search's body as the client reads it
// (V3SearchResultsConverter): totalHits an integer and data an array.
type searchDoc struct {
	TotalHits int              `json:"totalHits"`
	Data      []map[string]any `json:"data"`
}

func (f *fixture) search(t *testing.T, query, user string) searchDoc {
	t.Helper()
	rec := f.do(http.MethodGet, v3+"/query"+query, user)
	wantStatus(t, rec, http.StatusOK)
	var doc searchDoc
	decodeJSON(t, rec, &doc)
	return doc
}

// ids are the ids a search answered, in its order.
func (d searchDoc) ids() []string {
	out := []string{}
	for _, r := range d.Data {
		out = append(out, r["id"].(string))
	}
	return out
}

// searchFixture holds packages the search tests share: releases,
// pre-releases, a SemVer 2.0.0 version, a tool, and Acme.Mixed, whose 2.x
// partial may not read.
func searchFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", `<tags>json parser</tags>`)
	f.push("Acme.Lib", "1.1.0", `<title>Acme Parsing</title><tags>json parser</tags>`+
		`<summary>Parses</summary><projectUrl>https://acme.example</projectUrl>`+
		`<iconUrl>https://acme.example/i.png</iconUrl><licenseUrl>https://acme.example/l</licenseUrl>`)
	f.push("Acme.Lib", "2.0.0-beta", `<tags>json parser</tags>`)
	f.push("Acme.Lib", "2.0.0-beta.2", `<tags>json parser</tags>`)
	f.push("acme.tool", "1.0.0", `<packageTypes><packageType name="DotnetTool" /></packageTypes>`)
	f.push("Acme.Preview", "0.1.0-alpha", "")
	f.push("Acme.Mixed", "1.0.0", "")
	f.push("Acme.Mixed", "2.0.0", `<description>Second</description>`)
	f.push("Other.Thing", "1.0.0", `<authors>Ann, Bob ,</authors>`)
	return f
}

// dotnet package search sends the term with skip 0, take 20, prerelease as
// asked and semVerLevel 2.0.0 (RawSearchResourceV3), and lists what comes
// back.
func TestSearchAsDotnetSends(t *testing.T) {
	f := searchFixture(t)
	ctx := context.Background()
	c, _ := f.db.Repos().Get(ctx, "nuget-local")
	it, err := f.db.Items().Get(ctx, c.Key, false, "Acme.Lib.1.0.0.nupkg")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.db.Items().RecordDownload(ctx, it.ID, store.Download{
			By: "reader", At: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	doc := f.search(t, "?q=parser&skip=0&take=20&prerelease=false&semVerLevel=2.0.0", "reader")
	if doc.TotalHits != 1 || len(doc.Data) != 1 {
		t.Fatalf("search %+v", doc)
	}
	hive := base + v3 + "/registration-semver2/"
	want := map[string]any{
		"id": "Acme.Lib", "version": "1.1.0", "description": "The Acme.Lib package",
		"summary": "Parses", "title": "Acme Parsing", "iconUrl": "https://acme.example/i.png",
		"licenseUrl": "https://acme.example/l", "projectUrl": "https://acme.example",
		"tags": []any{"json", "parser"}, "authors": []any{"Acme"},
		"registration": hive + "acme.lib/index.json", "totalDownloads": float64(3),
		"packageTypes": []any{map[string]any{"name": "Dependency"}},
		"versions": []any{
			map[string]any{
				"version": "1.0.0", "downloads": float64(3),
				"@id": hive + "acme.lib/1.0.0.json",
			},
			map[string]any{
				"version": "1.1.0", "downloads": float64(0),
				"@id": hive + "acme.lib/1.1.0.json",
			},
		},
	}
	if diff := cmp.Diff(want, doc.Data[0]); diff != "" {
		t.Fatalf("result (-want +got):\n%s", diff)
	}
	// --prerelease: the newest is a SemVer 2.0.0 pre-release.
	doc = f.search(t, "?q=parser&skip=0&take=20&prerelease=true&semVerLevel=2.0.0", "reader")
	if doc.Data[0]["version"] != "2.0.0-beta.2" || len(doc.Data[0]["versions"].([]any)) != 4 {
		t.Fatalf("prerelease %v", doc.Data[0])
	}
}

// A search shows only listed versions, which is all of them; pre-releases
// only when asked for; SemVer 2.0.0 versions only to a client that reads
// them, with the registration of the hive it reads; and only what the
// caller may read.
func TestSearchFilters(t *testing.T) {
	f := searchFixture(t)
	for _, c := range []struct {
		query, user string
		ids         []string
	}{
		{"", "reader", []string{"Acme.Lib", "Acme.Mixed", "acme.tool", "Other.Thing"}},
		{
			"?prerelease=true", "reader",
			[]string{"Acme.Lib", "Acme.Mixed", "Acme.Preview", "acme.tool", "Other.Thing"},
		},
		{"?q=ACME", "reader", []string{"Acme.Lib", "Acme.Mixed", "acme.tool"}},
		// Every term must match, in the id, title, description or tags.
		{"?q=acme+json", "reader", []string{"Acme.Lib"}},
		{"?q=acme+xml", "reader", []string{}},
		{"?q=parsing", "reader", []string{"Acme.Lib"}},
		{"?q=second", "reader", []string{"Acme.Mixed"}},
		// Matched by the newest version the caller may read.
		{"?q=second", "partial", []string{}},
		{"?q=mixed", "partial", []string{"Acme.Mixed"}},
		{"", "nobody", []string{}},
		{"?packageType=dotnettool", "reader", []string{"acme.tool"}},
		{"?packageType=Dependency&q=acme", "reader", []string{"Acme.Lib", "Acme.Mixed"}},
		{"?packageType=Template", "reader", []string{}},
		{"?prerelease=TRUE&q=preview", "reader", []string{"Acme.Preview"}},
		{"?prerelease=yes&q=preview", "reader", []string{}},
		{
			"?includeDelisted=true&supportedFramework=net8.0&q=thing", "reader",
			[]string{"Other.Thing"},
		},
	} {
		doc := f.search(t, c.query, c.user)
		if diff := cmp.Diff(c.ids, doc.ids()); diff != "" || doc.TotalHits != len(c.ids) {
			t.Errorf("%s as %s: totalHits %d (-want +got):\n%s",
				c.query, c.user, doc.TotalHits, diff)
		}
	}
	// Without semVerLevel, SemVer 2.0.0 versions are hidden and the
	// registration is the hive without them.
	for query, want := range map[string]string{
		"?q=acme.lib&prerelease=true":                   "2.0.0-beta",
		"?q=acme.lib&prerelease=true&semVerLevel=1.0.0": "2.0.0-beta",
		"?q=acme.lib&prerelease=true&semVerLevel=x":     "2.0.0-beta",
		"?q=acme.lib&prerelease=true&semVerLevel=2.0.0": "2.0.0-beta.2",
		"?q=acme.lib&prerelease=true&semVerLevel=3":     "2.0.0-beta.2",
	} {
		doc := f.search(t, query, "reader")
		hive := "/registration/"
		if want == "2.0.0-beta.2" {
			hive = "/registration-semver2/"
		}
		if doc.Data[0]["version"] != want ||
			doc.Data[0]["registration"] != base+v3+hive+"acme.lib/index.json" {
			t.Errorf("%s: %v %v", query, doc.Data[0]["version"], doc.Data[0]["registration"])
		}
	}
	doc := f.search(t, "?q=other", "reader")
	if diff := cmp.Diff([]any{"Ann", "Bob"}, doc.Data[0]["authors"]); diff != "" {
		t.Errorf("authors (-want +got):\n%s", diff)
	}
	if _, ok := doc.Data[0]["title"]; ok {
		t.Errorf("a title the nuspec lacks: %v", doc.Data[0])
	}
}

// skip and take page the results, in id order; totalHits counts them all.
func TestSearchPaging(t *testing.T) {
	f := searchFixture(t)
	for _, c := range []struct {
		query string
		ids   []string
	}{
		{"?skip=1&take=2", []string{"Acme.Mixed", "acme.tool"}},
		{"?skip=3", []string{"Other.Thing"}},
		{"?skip=9", []string{}},
		{"?take=0", []string{}},
		{"?skip=x&take=y", []string{"Acme.Lib", "Acme.Mixed", "acme.tool", "Other.Thing"}},
		{"?skip=-1&take=-1", []string{"Acme.Lib", "Acme.Mixed", "acme.tool", "Other.Thing"}},
		{"?take=100000", []string{"Acme.Lib", "Acme.Mixed", "acme.tool", "Other.Thing"}},
	} {
		doc := f.search(t, c.query, "reader")
		if diff := cmp.Diff(c.ids, doc.ids()); diff != "" || doc.TotalHits != 4 {
			t.Errorf("%s: totalHits %d (-want +got):\n%s", c.query, doc.TotalHits, diff)
		}
	}
	// More than defaultTake, across more than one read of the index.
	t.Cleanup(nuget.SetListPage(7))
	g := newFixture(t)
	for i := range 25 {
		g.push("Pkg"+string(rune('A'+i)), "1.0.0", "")
	}
	doc := g.search(t, "", "reader")
	if doc.TotalHits != 25 || len(doc.Data) != 20 {
		t.Fatalf("totalHits %d, %d results", doc.TotalHits, len(doc.Data))
	}
}

// autocompleteDoc is the autocomplete's body.
type autocompleteDoc struct {
	TotalHits int      `json:"totalHits"`
	Data      []string `json:"data"`
}

// The autocomplete answers ids by prefix, as the client asks
// (AutoCompleteResourceV3), and one id's versions.
func TestAutocomplete(t *testing.T) {
	f := searchFixture(t)
	for _, c := range []struct {
		query, user string
		total       int
		data        []string
	}{
		{
			"?q=acme.&prerelease=false&semVerLevel=2.0.0", "reader", 3,
			[]string{"Acme.Lib", "Acme.Mixed", "acme.tool"},
		},
		{
			"?q=ACME.&prerelease=true&semVerLevel=2.0.0", "reader", 4,
			[]string{"Acme.Lib", "Acme.Mixed", "Acme.Preview", "acme.tool"},
		},
		{"?q=acme.&skip=1&take=1", "reader", 3, []string{"Acme.Mixed"}},
		{"?q=lib", "reader", 0, []string{}},
		{"", "reader", 4, []string{"Acme.Lib", "Acme.Mixed", "acme.tool", "Other.Thing"}},
		{"?q=acme", "nobody", 0, []string{}},
		{"?id=ACME.LIB", "reader", 2, []string{"1.0.0", "1.1.0"}},
		{"?id=acme.lib&prerelease=true", "reader", 3, []string{"1.0.0", "1.1.0", "2.0.0-beta"}},
		{
			"?id=acme.lib&prerelease=true&semVerLevel=2.0.0&skip=1&take=2", "reader", 4,
			[]string{"1.1.0", "2.0.0-beta"},
		},
		{"?id=acme.mixed", "partial", 1, []string{"1.0.0"}},
		{"?id=absent", "reader", 0, []string{}},
	} {
		rec := f.do(http.MethodGet, v3+"/autocomplete"+c.query, c.user)
		wantStatus(t, rec, http.StatusOK)
		var doc autocompleteDoc
		decodeJSON(t, rec, &doc)
		if doc.TotalHits != c.total || !cmp.Equal(c.data, doc.Data) {
			t.Errorf("%s as %s: %+v", c.query, c.user, doc)
		}
	}
}

// A read of the index or the statistics that fails is a fault.
func TestSearchFaults(t *testing.T) {
	for _, failing := range []string{"list", "versions", "metadata", "stats", "item"} {
		f := newFixture(t, func(st store.Store) store.Store {
			return failingStore{Store: st, failing: failing}
		})
		f.push("Acme.Lib", "1.0.0", "")
		for i, path := range []string{v3 + "/query", v3 + "/autocomplete?q=a", v3 + "/autocomplete?id=acme.lib"} {
			want := http.StatusInternalServerError
			if (failing == "stats" || failing == "item") && i > 0 || failing == "list" && i == 2 {
				want = http.StatusOK
			}
			rec := f.do(http.MethodGet, path, "admin")
			wantStatus(t, rec, want)
		}
		if len(f.faults) == 0 {
			t.Errorf("%s: no fault logged", failing)
		}
	}
}

// Hidden versions contribute neither metadata nor download counts. Stable
// versions with SemVer2 dependency bounds require a SemVer2-aware client too.
func TestSearchVisibleCountsAndDependencySemVer(t *testing.T) {
	f := searchFixture(t)
	ctx := context.Background()
	for _, version := range []string{"1.0.0", "2.0.0"} {
		it, err := f.db.Items().Get(ctx, "nuget-local", false, "Acme.Mixed."+version+".nupkg")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.db.Items().
			RecordDownload(ctx, it.ID, store.Download{By: "reader", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	doc := f.search(t, "?q=mixed", "partial")
	if len(doc.Data) != 1 || doc.Data[0]["version"] != "1.0.0" ||
		doc.Data[0]["totalDownloads"] != float64(1) ||
		len(doc.Data[0]["versions"].([]any)) != 1 {
		t.Fatalf("hidden version leaked: %+v", doc)
	}
	f.push("Dependency.SemVer", "1.0.0", "")
	f.push(
		"Dependency.SemVer",
		"2.0.0",
		`<dependencies><dependency id="Other" version="[1.0.0-beta.1,)" /></dependencies>`,
	)
	for query, want := range map[string]string{
		"?q=Dependency.SemVer":                   "1.0.0",
		"?q=Dependency.SemVer&semVerLevel=2.0.0": "2.0.0",
	} {
		got := f.search(t, query, "reader")
		if len(got.Data) != 1 || got.Data[0]["version"] != want {
			t.Fatalf("%s: %+v", query, got)
		}
	}
}
