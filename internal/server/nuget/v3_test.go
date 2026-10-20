package nuget_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// serviceIndexDoc is the service index as the client reads it.
type serviceIndexDoc struct {
	Version   string `json:"version"`
	Resources []struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	} `json:"resources"`
}

// resource is the @id of the first resource of type t, as the client takes
// the first it finds (ServiceIndexResourceV3.GetServiceEntryUris).
func (d serviceIndexDoc) resource(t *testing.T, typ string) string {
	t.Helper()
	for _, r := range d.Resources {
		if r.Type == typ {
			return r.ID
		}
	}
	t.Fatalf("no %s in the service index", typ)
	return ""
}

// The service index answers at the three paths the clients use, with
// every resource decision 0038 lists, built from the request's base URL.
func TestServiceIndex(t *testing.T) {
	f := newFixture(t)
	want := map[string]string{
		"PackageBaseAddress/3.0.0":             base + v3 + "/flatcontainer/",
		"RegistrationsBaseUrl":                 base + v3 + "/registration/",
		"RegistrationsBaseUrl/3.0.0-beta":      base + v3 + "/registration/",
		"RegistrationsBaseUrl/3.0.0-rc":        base + v3 + "/registration/",
		"RegistrationsBaseUrl/3.4.0":           base + v3 + "/registration/",
		"RegistrationsBaseUrl/3.6.0":           base + v3 + "/registration-semver2/",
		"SearchQueryService":                   base + v3 + "/query",
		"SearchQueryService/3.0.0-beta":        base + v3 + "/query",
		"SearchQueryService/3.0.0-rc":          base + v3 + "/query",
		"SearchQueryService/3.5.0":             base + v3 + "/query",
		"SearchAutocompleteService":            base + v3 + "/autocomplete",
		"SearchAutocompleteService/3.0.0-beta": base + v3 + "/autocomplete",
		"SearchAutocompleteService/3.0.0-rc":   base + v3 + "/autocomplete",
		"SearchAutocompleteService/3.5.0":      base + v3 + "/autocomplete",
		"PackagePublish/2.0.0":                 base + "/artifactory/api/nuget/nuget-local",
		"LegacyGallery/2.0.0":                  base + "/artifactory/api/nuget/nuget-local",
		"SymbolPackagePublish/4.9.0":           base + v3 + "/symbols",
	}
	for _, path := range []string{v3 + "/index.json", v3, v3 + "/"} {
		for _, user := range []string{"anonymous", "nobody", "admin"} {
			rec := f.do(http.MethodGet, path, user)
			wantStatus(t, rec, http.StatusOK)
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("%s: Content-Type %q", path, ct)
			}
			var doc serviceIndexDoc
			decodeJSON(t, rec, &doc)
			got := map[string]string{}
			for _, r := range doc.Resources {
				if _, dup := got[r.Type]; dup {
					t.Fatalf("%s listed twice", r.Type)
				}
				got[r.Type] = r.ID
			}
			if doc.Version != "3.0.0" {
				t.Fatalf("version %q", doc.Version)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("%s as %s (-want +got):\n%s", path, user, diff)
			}
		}
	}
	rec := f.do(http.MethodHead, v3+"/index.json", "admin")
	wantStatus(t, rec, http.StatusOK)
	for path, status := range map[string]int{
		// Neither another type, nor a missing repository, nor the classes
		// P9 and P10 serve.
		"/artifactory/api/nuget/v3/libs-local/index.json":   http.StatusNotFound,
		"/artifactory/api/nuget/v3/missing/index.json":      http.StatusNotFound,
		"/artifactory/api/nuget/v3/nuget-remote/index.json": http.StatusNotImplemented,
		// Not served: the v2 queries under the v3 prefix and the
		// symbol-server reads (decision 0038).
		v3 + "/FindPackagesById()?id='x'":        http.StatusNotFound,
		v3 + "/app.pdb/0123456789abcdef/app.pdb": http.StatusNotFound,
	} {
		wantStatus(t, f.do(http.MethodGet, path, "admin"), status)
	}
	wantStatus(t, f.do(http.MethodPost, v3+"/index.json", "admin"), http.StatusMethodNotAllowed)
	// The generated reindex operation owns POST at the v2 root. Claiming
	// that root must preserve its 405 response to unsupported methods.
	wantStatus(t, f.do(http.MethodGet, "/artifactory/api/nuget/nuget-local/reindex", "admin"),
		http.StatusMethodNotAllowed)
}

// The flat container lists the versions a caller may read, normalised and
// lower-cased, ascending, and serves each version's package and nuspec,
// matching the id and version ignoring case.
func TestFlatContainer(t *testing.T) {
	f := newFixture(t)
	beta := f.push("Acme.Lib", "1.0.0-Beta", "")
	f.push("Acme.Lib", "1.0", "")
	f.push("Acme.Lib", "2.0.0.0", "")
	f.push("Acme.Mixed", "1.0.0", "")
	f.push("Acme.Mixed", "2.0.0", "")
	// A version with its symbols alone has nothing to serve yet.
	f.deploy("Acme.Lib.3.0.0.snupkg", snupkg("Acme.Lib", "3.0.0"))
	flat := v3 + "/flatcontainer/"

	for _, c := range []struct {
		path, user string
		want       []string
	}{
		{flat + "acme.lib/index.json", "reader", []string{"1.0.0-beta", "1.0.0", "2.0.0"}},
		{flat + "ACME.LIB/index.json", "reader", []string{"1.0.0-beta", "1.0.0", "2.0.0"}},
		{flat + "acme.mixed/index.json", "partial", []string{"1.0.0"}},
	} {
		rec := f.do(http.MethodGet, c.path, c.user)
		wantStatus(t, rec, http.StatusOK)
		var got struct {
			Versions []string `json:"versions"`
		}
		decodeJSON(t, rec, &got)
		if diff := cmp.Diff(c.want, got.Versions); diff != "" {
			t.Fatalf("%s as %s (-want +got):\n%s", c.path, c.user, diff)
		}
	}
	// None the caller may read reads as no package at all, to anonymous
	// too, which force authentication exists to change (decision 0038).
	for path, user := range map[string]string{
		flat + "acme.lib/index.json":     "nobody",
		flat + "acme.lib/index.json?x=1": "anonymous",
		flat + "acme.mixed/index.json":   "nobody",
		flat + "acme.none/index.json":    "admin",
		flat + "acme.lib":                "admin",
		flat + "acme.lib/index.xml":      "admin",
		flat + "acme.lib/1.0.0/x/y":      "admin",
	} {
		wantStatus(t, f.do(http.MethodGet, path, user), http.StatusNotFound)
	}

	// The package, in the case the list gave it or any other.
	for _, path := range []string{
		flat + "acme.lib/1.0.0-beta/acme.lib.1.0.0-beta.nupkg",
		flat + "acme.lib/1.0.0-Beta/acme.lib.1.0.0-Beta.nupkg",
		flat + "Acme.Lib/1.0.0-BETA/ACME.LIB.1.0.0-BETA.NUPKG",
	} {
		rec := f.do(http.MethodGet, path, "reader")
		wantStatus(t, rec, http.StatusOK)
		if !bytes.Equal(rec.Body.Bytes(), beta) ||
			rec.Header().Get("X-Served") != "Acme.Lib.1.0.0-Beta.nupkg" {
			t.Fatalf("%s served %q", path, rec.Header().Get("X-Served"))
		}
	}
	rec := f.do(http.MethodHead, flat+"acme.lib/1.0.0/acme.lib.1.0.0.nupkg", "reader")
	wantStatus(t, rec, http.StatusOK)
	// A version that normalises to a stored one is that version.
	rec = f.do(http.MethodGet, flat+"acme.lib/1.0/acme.lib.1.0.nupkg", "reader")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("X-Served") != "Acme.Lib.1.0.0.nupkg" {
		t.Fatalf("1.0 served %q", rec.Header().Get("X-Served"))
	}

	// The nuspec, as the package holds it.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec = f.do(method, flat+"acme.lib/2.0.0/acme.lib.nuspec", "reader")
		wantStatus(t, rec, http.StatusOK)
		if rec.Header().Get("Content-Type") != "application/xml" {
			t.Fatalf("nuspec Content-Type %q", rec.Header().Get("Content-Type"))
		}
	}
	if !strings.Contains(rec.Body.String(), "<version>2.0.0.0</version>") {
		t.Fatalf("nuspec %s", rec.Body)
	}

	// A download is refused as the item routes refuse one: 401 tells the
	// client to send credentials.
	for user, status := range map[string]int{
		"anonymous": http.StatusUnauthorized,
		"nobody":    http.StatusForbidden,
		"partial":   http.StatusForbidden,
	} {
		for _, path := range []string{
			flat + "acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg",
			flat + "acme.mixed/2.0.0/acme.mixed.nuspec",
		} {
			rec := f.do(http.MethodGet, path, user)
			wantStatus(t, rec, status)
		}
	}
	for _, path := range []string{
		flat + "acme.lib/9.0.0/acme.lib.9.0.0.nupkg",
		flat + "acme.lib/3.0.0/acme.lib.3.0.0.nupkg",
		flat + "acme.none/1.0.0/acme.none.1.0.0.nupkg",
		flat + "acme.lib/not-a-version/acme.lib.not-a-version.nupkg",
		flat + "acme.lib/1.0.0/acme.lib.2.0.0.nupkg",
		flat + "acme.lib/1.0.0/acme.lib.1.0.0.snupkg",
		flat + "acme.lib/1.0.0/other.nuspec",
	} {
		wantStatus(t, f.do(http.MethodGet, path, "admin"), http.StatusNotFound)
	}
	// A path the kit cannot parse.
	wantStatus(t, f.do(http.MethodGet, flat+"acme.lib;=x/index.json", "admin"),
		http.StatusBadRequest)
	wantStatus(t, f.do(http.MethodPut, flat+"acme.lib/index.json", "admin"),
		http.StatusMethodNotAllowed)
}

// A restore through the flat container makes NuGet.Protocol's requests
// (HttpFileSystemBasedFindPackageByIdResource): the service index, the id's
// version list under PackageBaseAddress, then the package at the URL it
// builds from the version as listed.
func TestRestoreThroughFlatContainer(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	body := f.push("Acme.Lib", "1.1.0-rc.1", "")
	rec := f.do(http.MethodGet, v3+"/index.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	var index serviceIndexDoc
	decodeJSON(t, rec, &index)
	flat := index.resource(t, "PackageBaseAddress/3.0.0")
	const id = "Acme.Lib"
	lower := strings.ToLower(id)
	rec = f.do(http.MethodGet, relative(t, flat+lower+"/index.json"), "reader",
		"Accept-Encoding", "gzip, deflate")
	wantStatus(t, rec, http.StatusOK)
	var list struct {
		Versions []string `json:"versions"`
	}
	decodeJSON(t, rec, &list)
	if !slices.Contains(list.Versions, "1.1.0-rc.1") {
		t.Fatalf("versions %v", list.Versions)
	}
	ver := "1.1.0-rc.1"
	rec = f.do(http.MethodGet, relative(t, flat+lower+"/"+ver+"/"+lower+"."+ver+".nupkg"), "reader")
	wantStatus(t, rec, http.StatusOK)
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("restore downloaded other content")
	}
	readNuspec(t, rec.Body.Bytes(), id, ver)
}

// readNuspec checks that pkg is a package whose nuspec names id at version,
// as the client checks what it downloaded.
func readNuspec(t *testing.T, pkg []byte, id, version string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := zr.Open(id + ".nuspec")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(f)
	if !strings.Contains(string(raw), "<version>"+version+"</version>") {
		t.Fatalf("nuspec %s", raw)
	}
}

// registrationIndexDoc and the rest are the registration as the client
// reads it (RegistrationUtility, Model/RegistrationPage.cs).
type registrationIndexDoc struct {
	ID    string        `json:"@id"`
	Count int           `json:"count"`
	Items []pageLinkDoc `json:"items"`
}

type pageLinkDoc struct {
	ID    string            `json:"@id"`
	Count int               `json:"count"`
	Lower string            `json:"lower"`
	Upper string            `json:"upper"`
	Items []json.RawMessage `json:"items"`
}

type pageDoc struct {
	ID     string    `json:"@id"`
	Count  int       `json:"count"`
	Lower  string    `json:"lower"`
	Upper  string    `json:"upper"`
	Parent string    `json:"parent"`
	Items  []leafDoc `json:"items"`
}

type leafDoc struct {
	ID             string         `json:"@id"`
	PackageContent string         `json:"packageContent"`
	Registration   string         `json:"registration"`
	CatalogEntry   map[string]any `json:"catalogEntry"`
}

// A restore through the registration makes NuGet.Protocol's requests when
// a source has no PackageBaseAddress (RegistrationUtility.LoadRanges): the
// index, each page it links rather than inlines, then the package at the
// leaf's packageContent.
func TestRestoreThroughRegistration(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	body := f.push("Acme.Lib", "2.0.0-beta.1", "")
	rec := f.do(http.MethodGet, v3+"/index.json", "reader")
	var index serviceIndexDoc
	decodeJSON(t, rec, &index)
	hive := index.resource(t, "RegistrationsBaseUrl/3.6.0")
	rec = f.do(http.MethodGet, relative(t, hive+"acme.lib/index.json"), "reader")
	wantStatus(t, rec, http.StatusOK)
	var reg registrationIndexDoc
	decodeJSON(t, rec, &reg)
	if reg.Count != 1 || len(reg.Items) != 1 || reg.Items[0].Items != nil {
		t.Fatalf("index %s", rec.Body)
	}
	var content string
	for _, link := range reg.Items {
		rec = f.do(http.MethodGet, relative(t, link.ID), "reader")
		wantStatus(t, rec, http.StatusOK)
		var page pageDoc
		decodeJSON(t, rec, &page)
		for _, l := range page.Items {
			if l.CatalogEntry["version"] == "2.0.0-beta.1" {
				content = l.PackageContent
			}
		}
	}
	if content != hive+"Download/Acme.Lib/2.0.0-beta.1" {
		t.Fatalf("packageContent %q", content)
	}
	rec = f.do(http.MethodGet, relative(t, content), "reader")
	wantStatus(t, rec, http.StatusOK)
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("restore downloaded other content")
	}
}

// The index links pages of up to 128 versions and never inlines them; a
// page answers for any bounds that hold a visible version.
func TestRegistrationPaging(t *testing.T) {
	f := newFixture(t)
	for i := range 129 {
		f.push("Acme.Many", fmt.Sprintf("1.0.%d", i), "")
	}
	hive := v3 + "/registration-semver2/"
	rec := f.do(http.MethodGet, hive+"acme.many/index.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	var reg registrationIndexDoc
	decodeJSON(t, rec, &reg)
	want := registrationIndexDoc{
		ID: base + hive + "acme.many/index.json", Count: 2, Items: []pageLinkDoc{
			{
				ID: base + hive + "acme.many/page/1.0.0/1.0.127.json", Count: 128,
				Lower: "1.0.0", Upper: "1.0.127",
			},
			{
				ID: base + hive + "acme.many/page/1.0.128/1.0.128.json", Count: 1,
				Lower: "1.0.128", Upper: "1.0.128",
			},
		},
	}
	if diff := cmp.Diff(want, reg); diff != "" {
		t.Fatalf("index (-want +got):\n%s", diff)
	}
	for _, c := range []struct {
		lower, upper string
		count        int
	}{
		{"1.0.0", "1.0.127", 128},
		{"1.0.128", "1.0.128", 1},
		// Bounds no page listed, in NuGet order, not string order.
		{"1.0.9", "1.0.10", 2},
		{"0.1", "1.0.1", 2},
		{"1.0.127", "9.0.0", 2},
	} {
		rec := f.do(http.MethodGet,
			hive+"acme.many/page/"+c.lower+"/"+c.upper+".json", "reader")
		wantStatus(t, rec, http.StatusOK)
		var page pageDoc
		decodeJSON(t, rec, &page)
		if page.Count != c.count || len(page.Items) != c.count ||
			page.Parent != base+hive+"acme.many/index.json" {
			t.Fatalf("%s-%s: count %d, %d items, parent %s",
				c.lower, c.upper, page.Count, len(page.Items), page.Parent)
		}
	}
	for _, path := range []string{
		hive + "acme.many/page/2.0.0/3.0.0.json",
		hive + "acme.many/page/x/1.0.0.json",
		hive + "acme.many/page/1.0.0/y.json",
		hive + "acme.many/page/1.0.0/1.0.1",
		hive + "acme.none/page/1.0.0/1.0.1.json",
		hive + "acme.many/page/1.0.0.json",
	} {
		wantStatus(t, f.do(http.MethodGet, path, "reader"), http.StatusNotFound)
	}
}

// RegistrationsBaseUrl leaves out SemVer 2.0.0 versions and 3.6.0's hive
// shows them, as Microsoft defines the two.
func TestRegistrationHives(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	f.push("Acme.Lib", "1.1.0-rc.1", "")
	f.push("Acme.Lib", "1.2.0+git.abc", "")
	f.push(
		"Acme.Lib",
		"1.3.0",
		`<dependencies><dependency id="Acme.Dep" version="[1.0.0-a.1,)" /></dependencies>`,
	)
	f.push("Acme.Two", "2.0.0-rc.1", "")
	for hive, want := range map[string][]string{
		"registration":         {"1.0.0"},
		"registration-semver2": {"1.0.0", "1.1.0-rc.1", "1.2.0", "1.3.0"},
	} {
		rec := f.do(http.MethodGet, v3+"/"+hive+"/acme.lib/index.json", "reader")
		wantStatus(t, rec, http.StatusOK)
		var reg registrationIndexDoc
		decodeJSON(t, rec, &reg)
		rec = f.do(http.MethodGet, relative(t, reg.Items[0].ID), "reader")
		var page pageDoc
		decodeJSON(t, rec, &page)
		var got []string
		for _, l := range page.Items {
			got = append(got, l.CatalogEntry["version"].(string))
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("%s (-want +got):\n%s", hive, diff)
		}
		for _, v := range []string{"1.1.0-rc.1", "1.2.0"} {
			rec := f.do(http.MethodGet, v3+"/"+hive+"/acme.lib/"+v+".json", "reader")
			if want := slices.Contains(want, v); want != (rec.Code == http.StatusOK) {
				t.Fatalf("%s %s: %d", hive, v, rec.Code)
			}
		}
	}
	// A package with only SemVer 2.0.0 versions is no package to the
	// other hive.
	wantStatus(t, f.do(http.MethodGet, v3+"/registration/acme.two/index.json", "reader"),
		http.StatusNotFound)
	wantStatus(t,
		f.do(http.MethodGet, v3+"/registration/acme.two/page/2.0.0-rc.1/2.0.0-rc.1.json", "reader"),
		http.StatusNotFound)
	wantStatus(t, f.do(http.MethodGet, v3+"/registration-semver2/acme.two/index.json", "reader"),
		http.StatusOK)
}

// A leaf in a page carries 7.x's catalog entry, with @id, the nuspec's
// other fields, and published; a leaf document has 7.x's fields and
// published.
func TestRegistrationEntries(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", `<title>Acme Library</title><summary>Short</summary>`+
		`<tags> one  two </tags><projectUrl>https://acme.example</projectUrl>`+
		`<iconUrl>https://acme.example/i.png</iconUrl><licenseUrl>https://acme.example/l</licenseUrl>`+
		`<license type="expression">MIT</license><language>en-GB</language>`+
		`<requireLicenseAcceptance>true</requireLicenseAcceptance>`+
		`<dependencies><group targetFramework="net8.0"><dependency id="Acme.Dep" version="1.2.0" />`+
		`</group><group targetFramework="netstandard2.0" /></dependencies>`)
	hive := base + v3 + "/registration/"
	rec := f.do(http.MethodGet, v3+"/registration/acme.lib/page/1.0.0/1.0.0.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rec, &page)
	entry := page.Items[0]["catalogEntry"].(map[string]any)
	published, _ := entry["published"].(string)
	if !strings.HasPrefix(published, "2026-10-01T09:") || !strings.HasSuffix(published, "Z") {
		t.Fatalf("published %q", published)
	}
	delete(entry, "published")
	want := map[string]any{
		"@id": hive + "acme.lib/1.0.0.json", "id": "Acme.Lib", "version": "1.0.0",
		"title": "Acme Library", "authors": "Acme", "summary": "Short",
		"description": "The Acme.Lib package", "tags": []any{"one", "two"},
		"iconUrl": "https://acme.example/i.png", "licenseUrl": "https://acme.example/l",
		"licenseExpression": "MIT", "projectUrl": "https://acme.example", "language": "en-GB",
		"requireLicenseAcceptance": true, "listed": true,
		"packageContent": hive + "Download/Acme.Lib/1.0.0",
		"dependencyGroups": []any{
			map[string]any{"targetFramework": "net8.0", "dependencies": []any{map[string]any{
				"id": "Acme.Dep", "range": "1.2.0", "registration": hive + "acme.dep/index.json",
			}}},
			map[string]any{"targetFramework": "netstandard2.0"},
		},
	}
	if diff := cmp.Diff(want, entry); diff != "" {
		t.Fatalf("catalogEntry (-want +got):\n%s", diff)
	}
	if page.Items[0]["@id"] != hive+"acme.lib/1.0.0.json" ||
		page.Items[0]["registration"] != hive+"acme.lib/index.json" ||
		page.Items[0]["packageContent"] != hive+"Download/Acme.Lib/1.0.0" {
		t.Fatalf("leaf %v", page.Items[0])
	}

	rec = f.do(http.MethodGet, v3+"/registration/ACME.LIB/1.0.json", "reader")
	wantStatus(t, rec, http.StatusOK)
	var doc map[string]any
	decodeJSON(t, rec, &doc)
	if doc["published"] != published {
		t.Fatalf("leaf published %v", doc["published"])
	}
	delete(doc, "published")
	if diff := cmp.Diff(map[string]any{
		"@id": hive + "acme.lib/1.0.0.json", "listed": true,
		"packageContent": hive + "Download/Acme.Lib/1.0.0",
		"registration":   hive + "acme.lib/index.json",
	}, doc); diff != "" {
		t.Fatalf("leaf (-want +got):\n%s", diff)
	}
	for _, path := range []string{
		v3 + "/registration/acme.lib/2.0.0.json",
		v3 + "/registration/acme.lib/nope.json",
		v3 + "/registration/acme.lib/1.0.0",
		v3 + "/registration/acme.lib",
		v3 + "/registration/Download/Acme.Lib/9.0.0",
		v3 + "/registration/Download/Acme.Lib/x",
	} {
		wantStatus(t, f.do(http.MethodGet, path, "reader"), http.StatusNotFound)
	}
	// Download refuses as the item routes do.
	wantStatus(t, f.do(http.MethodGet, v3+"/registration/Download/Acme.Lib/1.0.0", "nobody"),
		http.StatusForbidden)
	wantStatus(t, f.do(http.MethodHead, v3+"/registration/Download/acme.lib/1.0.0", "reader"),
		http.StatusOK)
	// A path the kit cannot parse.
	wantStatus(t, f.do(http.MethodGet, v3+"/registration/acme.lib;=x/index.json", "reader"),
		http.StatusBadRequest)
}

// The registration is gzipped when the request accepts gzip, which the
// client always does.
func TestRegistrationGzip(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	path := v3 + "/registration/acme.lib/index.json"
	plain := f.do(http.MethodGet, path, "reader")
	wantStatus(t, plain, http.StatusOK)
	for accept, gzipped := range map[string]bool{
		"gzip, deflate":     true,
		"br;q=1, GZIP;q=.5": true,
		"*":                 true,
		"gzip;q=0":          false,
		"gzip;q=0, *":       false,
		"*, gzip;q=0":       false,
		"*;q=0, gzip":       true,
		"*;q=0":             false,
		"gzip;q=x":          true,
		"deflate":           false,
		"":                  false,
	} {
		rec := f.do(http.MethodGet, path, "reader", "Accept-Encoding", accept)
		wantStatus(t, rec, http.StatusOK)
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != gzipped {
			t.Fatalf("%q: gzipped %v", accept, got)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%q: Vary %q", accept, rec.Header().Get("Vary"))
		}
		body := rec.Body.Bytes()
		if gzipped {
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(zr)
		}
		if !bytes.Equal(body, plain.Body.Bytes()) {
			t.Fatalf("%q: %s", accept, body)
		}
	}
}

// A read the index fails is a fault, answered 500 and logged.
func TestV3Faults(t *testing.T) {
	for _, failing := range []string{"find", "versions", "version", "metadata"} {
		f := newFixture(t, func(st store.Store) store.Store {
			return failingStore{Store: st, failing: failing}
		})
		f.push("Acme.Lib", "1.0.0", "")
		for _, path := range []string{
			v3 + "/flatcontainer/acme.lib/index.json",
			v3 + "/flatcontainer/acme.lib/1.0.0/acme.lib.1.0.0.nupkg",
			v3 + "/registration/acme.lib/index.json",
			v3 + "/registration/acme.lib/page/1.0.0/1.0.0.json",
			v3 + "/registration/acme.lib/1.0.0.json",
			v3 + "/registration/Download/Acme.Lib/1.0.0",
		} {
			rec := f.do(http.MethodGet, path, "admin")
			// A read that does not reach the failing call answers as it
			// would.
			if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusOK {
				t.Errorf("%s: %s: %d %s", failing, path, rec.Code, rec.Body)
			}
		}
		if len(f.faults) == 0 {
			t.Errorf("%s: no fault logged", failing)
		}
	}
}
