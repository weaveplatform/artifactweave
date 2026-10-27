package pypi_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func jsonDocument(t *testing.T, f *fixture, suffix, user string) map[string]any {
	t.Helper()
	rec := f.do(http.MethodGet, api+"pypi/"+suffix, user)
	wantStatus(t, rec, http.StatusOK)
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The Warehouse schema is reduced exactly as decision 0037 specifies. These
// goldens retain nulls, all supported metadata fields, UTC microsecond times,
// both distribution kinds and digests, with no Warehouse-only fields.
func TestAccJSONDocuments(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	seedDemo(t, f)
	for _, c := range []struct{ path, golden string }{
		{"Demo/1.0.0/json", "metadata-1.0.json"},
		{"demo/json", "metadata-latest.json"},
		{"demo/2.0/json", "metadata-yanked.json"},
	} {
		rec := f.do(http.MethodGet, api+"pypi/"+c.path, "reader")
		wantStatus(t, rec, http.StatusOK)
		golden(t, c.golden, rec.Body.Bytes())
		head := f.do(http.MethodHead, api+"pypi/"+c.path, "reader")
		wantStatus(t, head, http.StatusOK)
		if head.Body.Len() != 0 ||
			head.Header().Get("Content-Length") != rec.Header().Get("Content-Length") {
			t.Fatalf("HEAD: %v %s", head.Header(), head.Body)
		}
	}
	rec := f.do(
		http.MethodGet,
		api+"pypi/demo/2/json",
		"reader",
		"X-JFrog-Override-Base-Url",
		"https://proxy.example/prefix",
	)
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(
		rec.Body.String(),
		`https://proxy.example/prefix`+api+`uploads/x%20y/demo-2.0-py3-none-any.whl`,
	) {
		t.Fatal(rec.Body)
	}
	for _, user := range []string{"reader", "partial"} {
		wantStatus(t, f.do(http.MethodGet, api+"pypi/missing/json", user), http.StatusNotFound)
		wantStatus(t, f.do(http.MethodGet, api+"pypi/demo/9/json", user), http.StatusNotFound)
	}
	wantStatus(t, f.do(http.MethodGet, api+"pypi/demo/json", "nobody"), http.StatusForbidden)
}

func TestAccJSONLatest(t *testing.T) {
	for _, c := range []struct {
		versions []string
		yanked   string
		expected string
	}{
		{[]string{"1.0", "2.0rc1", "3.0.dev1"}, "", "1.0"},
		{[]string{"1.0", "2.0", "3.0rc1"}, "2.0", "1.0"},
		{[]string{"1.0rc1", "2.0.dev1"}, "", "2.0.dev1"},
		{[]string{"1.0", "2.0"}, "all", "2.0"},
		{[]string{"1.0", "1.0.post1", "1.0+local"}, "", "1.0.post1"},
	} {
		t.Run(strings.Join(c.versions, "-"), func(t *testing.T) {
			f := newFixture(t, pypifmt.Settings{})
			for _, version := range c.versions {
				var props types.Properties
				if c.yanked == "all" || c.yanked == version {
					props = types.Properties{pypifmt.PropYanked: {"broken"}}
				}
				f.deploy(
					t,
					"pypi-local",
					fmt.Sprintf("demo/%s/demo-%s-py3-none-any.whl", version, version),
					wheel(t, "demo", version, metadata("Demo", version, "")),
					props,
				)
			}
			doc := jsonDocument(t, f, "demo/json", "reader")
			if got := doc["info"].(map[string]any)["version"]; got != c.expected {
				t.Fatalf("version %v, want %s", got, c.expected)
			}
		})
	}
}

// Earlier private files and newer private versions must not determine even
// the info object. Partial readers get metadata from their earliest visible file.
func TestAccJSONVisibility(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	f.deploy(
		t,
		"pypi-local",
		"private/demo-1.0-py3-none-any.whl",
		wheel(t, "demo", "1.0", metadata("Demo", "1.0", "Summary: secret earlier metadata")),
		nil,
	)
	f.deploy(
		t,
		"pypi-local",
		"demo/1.0/demo-1.0.tar.gz",
		sdist(t, "demo", "1.0", metadata("Demo", "1.0", "Summary: public metadata")),
		nil,
	)
	f.deploy(
		t,
		"pypi-local",
		"private/demo-2.0-py3-none-any.whl",
		wheel(t, "demo", "2.0", metadata("Demo", "2.0", "Summary: secret newer metadata")),
		nil,
	)
	for _, suffix := range []string{"demo/json", "Demo/1.0.0/json"} {
		doc := jsonDocument(t, f, suffix, "partial")
		info := doc["info"].(map[string]any)
		if info["summary"] != "public metadata" || info["version"] != "1.0" ||
			len(doc["urls"].([]any)) != 1 {
			t.Fatalf("visible document: %#v", doc)
		}
	}
	wantStatus(t, f.do(http.MethodGet, api+"pypi/demo/2.0/json", "partial"), http.StatusNotFound)
	doc := jsonDocument(t, f, "demo/1/json", "reader")
	if doc["info"].(map[string]any)["summary"] != "secret earlier metadata" {
		t.Fatal(doc)
	}
}

func TestAccJSONAllMetadata(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	text := "Metadata-Version: 2.4\nName: Rich\nVersion: 1.0\nAuthor: Author\nAuthor-email: a@example.org\nMaintainer: Maintainer\nMaintainer-email: m@example.org\nSummary: Summary\nDescription-Content-Type: text/markdown\nDownload-URL: https://example.org/download\nHome-page: https://example.org\nKeywords: one,two\nLicense: MIT\nLicense-Expression: MIT\nLicense-File: LICENSE\nLicense-File: NOTICE\nPlatform: any\nRequires-Python: >=3.9\nRequires-Dist: dep>=1\nRequires-Dist: optional; extra == 'test'\nProvides-Extra: test\nClassifier: One\nClassifier: Two\nDynamic: Requires-Dist\nProject-URL: Source, https://example.org/source\nProject-URL: malformed\nProject-URL: Issues, https://example.org/issues\n\n# Body\n"
	f.deploy(t, "pypi-local", "rich-1.0-py3-none-any.whl", wheel(t, "rich", "1.0", text), nil)
	rec := f.do(http.MethodGet, api+"pypi/rich/json", "reader")
	wantStatus(t, rec, http.StatusOK)
	golden(t, "metadata-rich.json", rec.Body.Bytes())
}

// Captured from the official Warehouse API on 2026-10-20. Compare its object
// shape after the omissions explicitly chosen in decision 0037; ownership is
// another Warehouse website-only field, not part of Artifactory's response.
func TestAccJSONWarehouseShape(t *testing.T) {
	data, err := os.ReadFile("testdata/warehouse-sampleproject-4.0.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var warehouse map[string]any
	if err = json.Unmarshal(data, &warehouse); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, pypifmt.Settings{})
	seedDemo(t, f)
	doc := jsonDocument(t, f, "demo/1.0/json", "reader")
	info := warehouse["info"].(map[string]any)
	for _, key := range []string{"bugtrack_url", "docs_url", "downloads", "package_url", "project_url", "release_url"} {
		delete(info, key)
	}
	file := warehouse["urls"].([]any)[0].(map[string]any)
	for _, key := range []string{"core-metadata", "downloads", "has_sig"} {
		delete(file, key)
	}
	delete(file["digests"].(map[string]any), "blake2b_256")
	check := func(name string, got, want map[string]any) {
		t.Helper()
		keys := func(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
		if !slices.Equal(keys(got), keys(want)) {
			t.Errorf("%s keys: %v, want %v", name, keys(got), keys(want))
		}
	}
	check("info", doc["info"].(map[string]any), info)
	gotFile := doc["urls"].([]any)[0].(map[string]any)
	check("urls", gotFile, file)
	check("digests", gotFile["digests"].(map[string]any), file["digests"].(map[string]any))
}

func TestAccJSONEgg(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	f.deploy(
		t,
		"pypi-local",
		"legacy-1.0-py3.9.egg",
		[]byte("property-indexed egg"),
		types.Properties{pypifmt.PropName: {"legacy"}, pypifmt.PropVersion: {"1.0"}},
	)
	doc := jsonDocument(t, f, "legacy/json", "reader")
	file := doc["urls"].([]any)[0].(map[string]any)
	if file["packagetype"] != "bdist_egg" || file["python_version"] != "3.9" {
		t.Fatal(file)
	}
}
