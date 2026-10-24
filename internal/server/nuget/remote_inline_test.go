package nuget_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRemoteV3InlineRegistrationPageIdentity(t *testing.T) {
	f := newRemoteFixture(t)
	entries := f.packageDocs("Acme.Mixed", "1.0.0")
	index := "/reg/acme.mixed/index.json"
	f.setJSON(index, map[string]any{
		"@id":   f.up.URL + index,
		"count": 1,
		"items": []any{map[string]any{
			"@id":    f.up.URL + index + "#page/1.0.0/1.0.0",
			"parent": f.up.URL + index,
			"lower":  "1.0.0", "upper": "1.0.0", "count": 1, "items": entries,
		}},
	})
	registration := f.do("GET", remoteV3+"registration-semver2/acme.mixed/index.json", "reader")
	wantStatus(t, registration, 200)
	if strings.Contains(registration.Body.String(), f.up.URL) ||
		strings.Contains(registration.Body.String(), "#page/") {
		t.Fatal("inline identity leaked instead of rewritten registration URLs")
	}
	path := remoteV3 + "flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg"
	first := f.do("GET", path, "reader")
	wantStatus(t, first, 200)
	want := nupkg(
		"Acme.Mixed",
		"1.0.0",
		`<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`,
	)
	if !bytes.Equal(first.Body.Bytes(), want) {
		t.Fatal("inline registration did not deliver the declared package bytes")
	}
	second := f.do("GET", path, "reader")
	wantStatus(t, second, 200)
	if !bytes.Equal(second.Body.Bytes(), want) ||
		f.up.Count("GET", "/objects/acme.mixed-1.0.0.zip") != 1 {
		t.Fatal("warm package was not reused")
	}
	if f.up.Count("GET", index) != 1 || f.up.Count("GET", "/opaque/page/acme.mixed") != 0 {
		t.Fatal("inline page identity initiated a page fetch")
	}
}

func inlineRegistrationDocument() map[string]any {
	return map[string]any{
		"@id": "https://up.example/registration/acme/index.json", "count": 1,
		"items": []any{map[string]any{
			"@id":   "https://up.example/registration/acme/index.json#page/1.0.0/1.0.0",
			"lower": "1.0.0", "upper": "1.0.0", "count": 1,
			"items": []any{map[string]any{
				"@id":            "https://up.example/registration/acme/1.0.0.json",
				"packageContent": "https://up.example/packages/acme.1.0.0.nupkg",
				"catalogEntry": map[string]any{
					"@id":     "https://up.example/catalog/acme.1.0.0.json",
					"id":      "acme",
					"version": "1.0.0",
				},
			}},
		}},
	}
}

func TestUnitRemoteV3InlineIdentityPreservesURLAndDocumentAdmission(t *testing.T) {
	c := repo.Config{URL: "https://up.example", V3FeedURL: "https://up.example/index.json"}
	for _, raw := range []string{
		"https://user:secret@up.example/index.json#page/1/1",
		"https://up.example/a/%2e%2e/index.json#page/1/1",
		"https://up.example/%00/index.json#page/1/1",
		"http://up.example/index.json#page/1/1",
		"https://localhost/index.json#page/1/1",
		"https://127.0.0.1/index.json#page/1/1",
		"https://10.0.0.1/index.json#page/1/1",
		"#page/1/1", "https://up.example/index.json#%xx",
		"https://up.example/index.json#%00", "https://up.example/index.json#%0a",
		"https://up.example/index.json#bad\\identity",
	} {
		t.Run(raw, func(t *testing.T) {
			d := inlineRegistrationDocument()
			d["items"].([]any)[0].(map[string]any)["@id"] = raw
			checkInlineAdmission(t, c, d, "registration", false)
		})
	}
	for _, kind := range []string{"linked-page", "download", "leaf", "catalog", "index", "fetched-page", "null-items", "wrong-count"} {
		t.Run(kind, func(t *testing.T) {
			d := inlineRegistrationDocument()
			page := d["items"].([]any)[0].(map[string]any)
			leaf := page["items"].([]any)[0].(map[string]any)
			documentKind := "registration"
			switch kind {
			case "linked-page":
				delete(page, "items")
			case "download":
				leaf["packageContent"] = "https://up.example/pkg#fragment"
			case "leaf":
				leaf["@id"] = "https://up.example/leaf#fragment"
			case "catalog":
				leaf["catalogEntry"].(map[string]any)["@id"] = "https://up.example/catalog#fragment"
			case "index":
				d["@id"] = "https://up.example/index#fragment"
			case "fetched-page":
				d, documentKind = page, "page"
			case "null-items":
				page["items"] = nil
			case "wrong-count":
				page["count"] = 2
			}
			checkInlineAdmission(t, c, d, documentKind, false)
		})
	}
	checkInlineAdmission(t, c, inlineRegistrationDocument(), "registration", true)
}

func checkInlineAdmission(
	t *testing.T,
	c repo.Config,
	raw map[string]any,
	kind string,
	allowed bool,
) {
	t.Helper()
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := nuget.DecodeRemote(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	err = nuget.ValidateRemoteDocument(c, doc, kind, "acme")
	if (err == nil) != allowed {
		t.Fatalf("admission=%v, wanted allowed=%v", err, allowed)
	}
}
