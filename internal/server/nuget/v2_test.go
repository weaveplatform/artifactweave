package nuget_test

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	legacyV2      = "/artifactory/api/nuget/nuget-local"
	dataNamespace = "http://schemas.microsoft.com/ado/2007/08/dataservices"
)

type parsedProperty struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
	Type    string `xml:"http://schemas.microsoft.com/ado/2007/08/dataservices/metadata type,attr"`
}
type parsedEntry struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom entry"`
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Summary string   `xml:"summary"`
	Authors []string `xml:"author>name"`
	Content struct {
		Src  string `xml:"src,attr"`
		Type string `xml:"type,attr"`
	} `xml:"content"`
	Properties struct {
		Items []parsedProperty `xml:",any"`
	} `xml:"http://schemas.microsoft.com/ado/2007/08/dataservices/metadata properties"`
}
type parsedFeed struct {
	XMLName xml.Name      `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []parsedEntry `xml:"entry"`
	Links   []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func (e parsedEntry) prop(t *testing.T, name string) string {
	t.Helper()
	for _, p := range e.Properties.Items {
		if p.XMLName.Local == name {
			if p.XMLName.Space != dataNamespace {
				t.Fatalf("%s namespace = %s", name, p.XMLName.Space)
			}
			return p.Value
		}
	}
	t.Fatalf("no property %s in %+v", name, e.Properties)
	return ""
}

func readXML(t *testing.T, b []byte, into any) {
	t.Helper()
	if err := xml.Unmarshal(b, into); err != nil {
		t.Fatalf("XML: %v\n%s", err, b)
	}
}

func (f *fixture) feed(path, user string) parsedFeed {
	f.t.Helper()
	rec := f.do(http.MethodGet, path, user)
	wantStatus(f.t, rec, 200)
	var out parsedFeed
	readXML(f.t, rec.Body.Bytes(), &out)
	return out
}

func feedVersions(t *testing.T, f parsedFeed) []string {
	t.Helper()
	var out []string
	for _, e := range f.Entries {
		out = append(out, e.prop(t, "Version"))
	}
	return out
}

func legacyV2URL(endpoint string, q url.Values) string {
	return legacyV2 + "/" + endpoint + "?" + q.Encode()
}

func TestAccV2DocumentsAndExactRestore(t *testing.T) {
	f := newFixture(t)
	body := f.push(
		"Acme.Mixed",
		"01.2.0+build",
		`<title>Title &amp; XML</title><summary>A &lt;summary&gt;</summary><tags>tools build</tags><dependencies><group targetFramework="net8.0"><dependency id="Acme.Dep" version="[1.0,2.0)"/></group><dependency id="Root" version="3.0"/></dependencies><requireLicenseAcceptance>true</requireLicenseAcceptance>`,
	)
	for _, path := range []string{legacyV2, legacyV2 + "/"} {
		rec := f.do(http.MethodGet, path, "reader")
		wantStatus(t, rec, 200)
		var service struct {
			XMLName    xml.Name
			Base       string `xml:"http://www.w3.org/XML/1998/namespace base,attr"`
			Collection struct {
				Href string `xml:"href,attr"`
			} `xml:"workspace>collection"`
		}
		readXML(t, rec.Body.Bytes(), &service)
		if service.XMLName.Space != "http://www.w3.org/2007/app" ||
			service.Collection.Href != "Packages" ||
			service.Base != base+legacyV2+"/" {
			t.Fatalf("service %+v", service)
		}
	}
	rec := f.do(http.MethodGet, legacyV2+"/$metadata", "reader")
	wantStatus(t, rec, 200)
	for _, s := range []string{`Name="V2FeedPackage"`, `Name="Packages" EntityType="NuGet.V2FeedPackage"`, `Name="IsAbsoluteLatestVersion"`, `Name="Search"`, `Name="FindPackagesById"`} {
		if !strings.Contains(rec.Body.String(), s) {
			t.Fatalf("metadata missing %s", s)
		}
	}
	for _, ver := range []string{"1.2", "1.2.0.0", "1.2.0+other", "1.2.0%2Bother"} {
		rec = f.do(
			http.MethodGet,
			legacyV2+"/Packages(Id='ACME.mixed',Version='"+ver+"')",
			"reader",
		)
		wantStatus(t, rec, 200)
		var entry parsedEntry
		readXML(t, rec.Body.Bytes(), &entry)
		if entry.prop(t, "Id") != "Acme.Mixed" || entry.prop(t, "Version") != "1.2.0" ||
			entry.prop(t, "NormalizedVersion") != "1.2.0" {
			t.Fatalf("identity %+v", entry)
		}
		for name, want := range map[string]string{"Title": "Title & XML", "Summary": "A <summary>", "PackageHashAlgorithm": "SHA512", "Listed": "true", "IsPrerelease": "false", "IsLatestVersion": "true", "IsAbsoluteLatestVersion": "true", "RequireLicenseAcceptance": "true", "Authors": "Acme", "PackageSize": strconv.Itoa(len(body))} {
			if got := entry.prop(t, name); got != want {
				t.Fatalf("%s=%s, want %s", name, got, want)
			}
		}
		digest := sha512.Sum512(body)
		if entry.prop(t, "PackageHash") != base64.StdEncoding.EncodeToString(digest[:]) {
			t.Fatal("wrong package digest")
		}
		if !strings.Contains(entry.prop(t, "Dependencies"), "Acme.Dep:[1.0,2.0):net8.0") {
			t.Fatalf("dependencies: %s", entry.prop(t, "Dependencies"))
		}
		downloaded := f.do(http.MethodGet, relative(t, entry.Content.Src), "reader")
		wantStatus(t, downloaded, 200)
		if !bytes.Equal(downloaded.Body.Bytes(), body) {
			t.Fatal("download differs")
		}
		if entry.Content.Src != base+legacyV2+"/Download/Acme.Mixed/1.2.0" ||
			entry.Content.Type != "application/zip" {
			t.Fatalf("content %+v", entry.Content)
		}
		if entry.ID != base+legacyV2+"/Packages(Id='Acme.Mixed',Version='1.2.0')" {
			t.Fatalf("entry id %s", entry.ID)
		}
	}
}

func TestAccV2VisibilityAndFilters(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.0", "1.2", "1.10", "2.0-beta", "2.0-rc.1", "2.0"} {
		f.push("Acme.Mixed", v, `<tags>needle build</tags>`)
	}
	f.push("Zed", "1.0", `<title>Other</title>`)
	cases := []struct {
		endpoint, user string
		q              url.Values
		expected       []string
	}{
		{
			"FindPackagesById()",
			"reader",
			url.Values{"id": {"'acme.MIXED'"}},
			[]string{"1.0.0", "1.2.0", "1.10.0", "2.0.0-beta", "2.0.0"},
		},
		{
			"FindPackagesById()",
			"reader",
			url.Values{"id": {"'Acme.Mixed'"}, "semVerLevel": {"2.0.0"}},
			[]string{"1.0.0", "1.2.0", "1.10.0", "2.0.0-beta", "2.0.0-rc.1", "2.0.0"},
		},
		{
			"FindPackagesById()",
			"partial",
			url.Values{"id": {"'Acme.Mixed'"}, "$filter": {"IsLatestVersion"}},
			[]string{"1.10.0"},
		},
		{"FindPackagesById()", "reader", url.Values{"id": {"'missing'"}}, nil},
		{"FindPackagesById()", "nobody", url.Values{"id": {"'Acme.Mixed'"}}, nil},
		{
			"Search()",
			"reader",
			url.Values{"searchTerm": {"'NEEDLE'"}},
			[]string{"1.0.0", "1.2.0", "1.10.0", "2.0.0"},
		},
		{
			"Search()",
			"reader",
			url.Values{
				"searchTerm":        {"'needle'"},
				"includePrerelease": {"true"},
				"semVerLevel":       {"2.0.0"},
				"$filter":           {"IsAbsoluteLatestVersion"},
			},
			[]string{"2.0.0"},
		},
		{
			"Packages()",
			"reader",
			url.Values{"$filter": {"IsLatestVersion"}, "$orderby": {"Id"}},
			[]string{"2.0.0", "1.0.0"},
		},
		{
			"Packages",
			"reader",
			url.Values{"$filter": {"IsAbsoluteLatestVersion"}, "$skip": {"1"}, "$top": {"1"}},
			[]string{"1.0.0"},
		},
		{"Packages()", "reader", url.Values{"$top": {"0"}}, nil},
		{"Packages()", "reader", url.Values{"$skip": {"999999999"}}, nil},
		{"Search()", "anonymous", url.Values{}, nil},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			feed := f.feed(legacyV2URL(tc.endpoint, tc.q), tc.user)
			if got := feedVersions(t, feed); !slices.Equal(got, tc.expected) {
				t.Fatalf("versions %v, want %v", got, tc.expected)
			}
		})
	}
	feed := f.feed(legacyV2URL("FindPackagesById()", url.Values{"id": {"'Acme.Mixed'"}}), "partial")
	for i, e := range feed.Entries {
		want := strconv.FormatBool(i == len(feed.Entries)-1)
		if e.prop(t, "IsLatestVersion") != want || e.prop(t, "IsAbsoluteLatestVersion") != want {
			t.Fatalf("latest flags %+v", e)
		}
	}
	// A hidden newest version must not supply the search text or latest flags.
	f.push("Acme.Mixed", "9.0", `<title>Secret marker</title>`)
	if len(
		f.feed(legacyV2URL("Search()", url.Values{"searchTerm": {"'secret'"}}), "partial").Entries,
	) != 0 {
		t.Fatal("hidden metadata leaked")
	}
}

func TestAccV2ClientFallbackFilter(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Mixed", "1.0", `<tags>tool</tags>`)
	f.push("Other", "1.0", "")
	// The exact parenthesization and null guards BuildGetPackagesUri emits.
	fallback := "(((Id ne null) and substringof('acme',tolower(Id))) or ((Description ne null) and substringof('acme',tolower(Description)))) or ((Tags ne null) and substringof(' tool ',tolower(Tags)))"
	for _, expression := range []string{fallback, "(" + fallback + ") and IsLatestVersion", "substringof('acme',tolower(Id))", "(IsLatestVersion)", "substringof('can''t',tolower(Description))"} {
		feed := f.feed(legacyV2URL("Packages()", url.Values{"$filter": {expression}}), "reader")
		want := 1
		if expression == "(IsLatestVersion)" {
			want = 2
		}
		if strings.Contains(expression, "can''t") {
			want = 0
		}
		if len(feed.Entries) != want {
			t.Fatalf("%s got %d entries", expression, len(feed.Entries))
		}
	}
}

func TestAccV2PagingAndCounts(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 205; i++ {
		f.push("Many", fmt.Sprintf("1.0.%d", i), "")
	}
	for _, endpoint := range []string{"Packages()", "FindPackagesById()", "Search()"} {
		q := url.Values{
			"id":         {"'Many'"},
			"searchTerm": {"'Many'"},
			"$orderby":   {"Id"},
			"$top":       {"150"},
		}
		path := legacyV2URL(endpoint, q)
		var versions []string
		seen := map[string]bool{}
		for path != "" {
			if seen[path] {
				t.Fatal("repeated next link")
			}
			seen[path] = true
			feed := f.feed(path, "reader")
			if len(feed.Entries) > 100 {
				t.Fatal("unbounded page")
			}
			versions = append(versions, feedVersions(t, feed)...)
			path = ""
			for _, link := range feed.Links {
				if link.Rel == "next" {
					path = relative(t, link.Href)
					if !strings.Contains(path, "%24orderby=Id") {
						t.Fatal("lost query")
					}
				}
			}
		}
		if len(versions) != 150 || versions[0] != "1.0.0" || versions[149] != "1.0.149" {
			t.Fatalf("paging %d: %v", len(versions), versions)
		}
		rec := f.do(http.MethodGet, legacyV2URL(endpoint+"/$count", q), "reader")
		wantStatus(t, rec, 200)
		if rec.Body.String() != "205" || rec.Header().Get("Content-Type") != "text/plain" {
			t.Fatalf("count %s", rec.Body)
		}
	}
	// Omitting $top still supplies next links until the feed is exhausted.
	feed := f.feed(legacyV2+"/Packages", "reader")
	if len(feed.Entries) != 100 || len(feed.Links) != 1 {
		t.Fatalf("default page %+v", feed)
	}
}

func TestAccV2RefusalsAndUnsupportedQueries(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Mixed", "1.0", "")
	for _, path := range []string{"GetUpdates()", "GetUpdates()/$count", "package/Acme.Mixed/1.0", "Packages(Id='Missing',Version='1.0')", "Packages(Id='Acme.Mixed',Version='bad')", "Packages(Id='Acme.Mixed',Version='9.0')", "Packages(Id='bad''id',Version='1.0')", "Download/Acme.Mixed/9.0", "Packages()/nested/$count"} {
		wantStatus(t, f.do(http.MethodGet, legacyV2+"/"+path, "reader"), 404)
	}
	for _, q := range []url.Values{
		{"$select": {"Id"}},
		{"$expand": {"Dependencies"}},
		{"$unknown": {"x"}},
		{"$orderby": {"Version desc"}},
		{"$top": {"-1"}},
		{"$skip": {"x"}},
		{"$top": {"999999999999999999999999999999"}},
		{"$top": {"1", "2"}},
		{"$filter": {"Id eq 'Acme.Mixed'"}},
		{"$filter": {"substringof('x',tolower(Authors))"}},
		{"$filter": {"substringof('unterminated,tolower(Id))"}},
		{"$filter": {"substringof(x,tolower(Id))"}},
		{"$filter": {"substringof('x',Id)"}},
		{"$filter": {"Id ne nulljunk"}},
		{"$filter": {"IsLatestVersion ornot IsLatestVersion"}},
		{"$filter": {"(IsLatestVersion"}},
		{"$filter": {strings.Repeat("(", 33) + "IsLatestVersion" + strings.Repeat(")", 33)}},
		{"$filter": {strings.Repeat("x", 16*1024+1)}},
	} {
		wantStatus(t, f.do(http.MethodGet, legacyV2URL("Packages()", q), "reader"), 400)
	}
	for _, endpoint := range []string{"Search()", "FindPackagesById()"} {
		wantStatus(
			t,
			f.do(
				http.MethodGet,
				legacyV2URL(
					endpoint,
					url.Values{"id": {"'Acme.Mixed'"}, "$filter": {"substringof('x',tolower(Id))"}},
				),
				"reader",
			),
			400,
		)
	}
	for _, id := range []string{"", "unquoted", "'a'b'"} {
		wantStatus(
			t,
			f.do(
				http.MethodGet,
				legacyV2URL("FindPackagesById()", url.Values{"id": {id}}),
				"reader",
			),
			400,
		)
	}
	wantStatus(
		t,
		f.do(
			http.MethodGet,
			legacyV2URL("Search()", url.Values{"searchTerm": {"'broken"}}),
			"reader",
		),
		400,
	)
	for _, user := range []string{"nobody", "anonymous"} {
		wantStatus(
			t,
			f.do(http.MethodGet, legacyV2+"/Packages(Id='Acme.Mixed',Version='1.0')", user),
			404,
		)
	}
	wantStatus(t, f.do(http.MethodGet, legacyV2+"/Download/Acme.Mixed/1.0", "nobody"), 403)
	wantStatus(t, f.do(http.MethodGet, legacyV2+"/Download/Acme.Mixed/1.0", "anonymous"), 401)
	wantStatus(t, f.do(http.MethodGet, legacyV2+"/Packages()", ""), 200)
	wantStatus(
		t,
		f.do(http.MethodGet, "/artifactory/api/nuget/nuget-forced/$metadata", "anonymous"),
		401,
	)
	wantStatus(t, f.do(http.MethodGet, legacyV2+"/Download/a%2Fb/1.0", "reader"), 404)
	wantStatus(t, f.do(http.MethodGet, legacyV2+"/Packages();=invalid", "reader"), 400)
}

func TestAccV2StoreFailures(t *testing.T) {
	for _, failure := range []string{"list", "versions", "metadata", "item", "stats"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(
				t,
				func(s store.Store) store.Store { return failingStore{Store: s, failing: failure} },
			)
			f.push("Acme.Mixed", "1.0", "")
			wantStatus(t, f.do(http.MethodGet, legacyV2+"/Packages()", "reader"), 500)
		})
	}
	for _, failure := range []string{"find", "versions", "metadata", "item", "stats"} {
		t.Run("entry-"+failure, func(t *testing.T) {
			f := newFixture(
				t,
				func(s store.Store) store.Store { return failingStore{Store: s, failing: failure} },
			)
			f.push("Acme.Mixed", "1.0", "")
			wantStatus(
				t,
				f.do(http.MethodGet, legacyV2+"/Packages(Id='Acme.Mixed',Version='1.0')", "reader"),
				500,
			)
		})
	}
}

func TestAccV2DownloadCountsAndImmediateDelete(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Mixed", "1.0", "")
	f.push("Acme.Mixed", "2.0", "")
	for path, n := range map[string]int{"Acme.Mixed.1.0.0.nupkg": 3, "Acme.Mixed.2.0.0.nupkg": 8} {
		it, err := f.db.Items().Get(context.Background(), "nuget-local", false, path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if err := f.db.Items().
				RecordDownload(context.Background(), it.ID, store.Download{By: "reader", At: f.now()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	feed := f.feed(legacyV2+"/Packages()", "partial")
	if feed.Entries[0].prop(t, "DownloadCount") != "3" ||
		feed.Entries[0].prop(t, "VersionDownloadCount") != "3" {
		t.Fatal("hidden download counts leaked")
	}
	wantStatus(t, f.do(http.MethodDelete, legacyV2+"/Acme.Mixed/1.0", "writer"), 204)
	if got := f.feed(
		legacyV2URL("FindPackagesById()", url.Values{"id": {"'Acme.Mixed'"}}),
		"partial",
	); len(
		got.Entries,
	) != 0 {
		t.Fatal("deleted package remains visible")
	}
}
