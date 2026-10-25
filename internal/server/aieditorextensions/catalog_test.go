package aieditorextensions

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const marketplaceGallery = "https://marketplace.visualstudio.com/_apis/public/gallery"

func TestUnitMarketplaceCatalogProjectionPreservesIdentityAndRewritesAssets(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/marketplace-hexeditor.json")
	if err != nil {
		t.Fatal(err)
	}
	q := exactQuery("ms-vscode", "hexeditor")
	catalog, err := readCatalog(
		bytes.NewReader(body),
		int64(len(body)),
		marketplaceGallery+"/extensionquery",
		q,
	)
	if err != nil {
		t.Fatalf("official Marketplace response rejected: %v", err)
	}
	v := catalog.Results[0].Extensions[0].Versions[0]
	if !strings.Contains(v.AssetURI, "/extensions/ms-vscode/hexeditor/1.11.1/") ||
		!strings.Contains(
			v.FallbackAssetURI,
			"/publisher/ms-vscode/extension/hexeditor/1.11.1/assetbyname",
		) {
		t.Fatalf("unexpected native asset URLs: %+v", v)
	}
	projected, paths, err := projectCatalog(
		t.Context(),
		catalog,
		"https://registry.example/artifactory/api/aieditorextensions/editor",
		func(_ context.Context, path string) bool {
			return path == "extensions/ms-vscode/hexeditor"
		},
	)
	if err != nil || len(paths) != 1 ||
		bytes.Contains(projected, []byte("gallerycdn.vsassets.io")) ||
		bytes.Contains(projected, []byte("gallery.vsassets.io")) {
		t.Fatalf("unsafe projection: paths=%v err=%v body=%s", paths, err, projected)
	}
	var out galleryCatalog
	if err := json.Unmarshal(projected, &out); err != nil {
		t.Fatal(err)
	}
	got := out.Results[0].Extensions[0].Versions[0]
	if got.AssetURI != "https://registry.example/artifactory/api/aieditorextensions/editor/assets/ms-vscode/hexeditor/1.11.1" ||
		!strings.HasSuffix(fileSource(got, packageAsset), "/Microsoft.VisualStudio.Services.VSIXPackage") {
		t.Fatalf("native asset route missing: %+v", got)
	}
}

func TestUnitMarketplaceCatalogRejectsOriginIdentityAndChecksumChanges(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/marketplace-hexeditor.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, next string }{
		{"asset-origin", "ms-vscode.gallerycdn.vsassets.io", "evil.example"},
		{"other-publisher-cdn", "ms-vscode.gallerycdn.vsassets.io", "other.gallerycdn.vsassets.io"},
		{"fallback-origin", "ms-vscode.gallery.vsassets.io", "evil.example"},
		{"asset-identity", "/extensions/ms-vscode/hexeditor/", "/extensions/other/hexeditor/"},
		{"fallback-identity", "/publisher/ms-vscode/extension/hexeditor/", "/publisher/other/extension/hexeditor/"},
		{"checksum", "441e583a9df4b5f304cc6c97a4ec0f207cd7a9995119cfa95e22777e87a0a3b6", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := bytes.Replace(body, []byte(tc.old), []byte(tc.next), 1)
			if bytes.Equal(changed, body) {
				t.Fatal("fixture replacement did not apply")
			}
			if _, err := readCatalog(
				bytes.NewReader(changed),
				int64(len(changed)),
				marketplaceGallery+"/extensionquery",
				exactQuery("ms-vscode", "hexeditor"),
			); err == nil {
				t.Fatal("malformed upstream catalog accepted")
			}
		})
	}
}

func TestUnitCatalogRejectsMalformedAndAmbiguousUpstreamMetadata(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/marketplace-hexeditor.json")
	if err != nil {
		t.Fatal(err)
	}
	q := exactQuery("ms-vscode", "hexeditor")
	for _, tc := range []struct {
		name   string
		mutate func(*galleryCatalog)
	}{
		{"duplicate-extension", func(c *galleryCatalog) {
			c.Results[0].Extensions = append(c.Results[0].Extensions, c.Results[0].Extensions[0])
		}},
		{"missing-version", func(c *galleryCatalog) { c.Results[0].Extensions[0].Versions = nil }},
		{"duplicate-asset", func(c *galleryCatalog) {
			v := &c.Results[0].Extensions[0].Versions[0]
			v.Files = append(v.Files, v.Files[0])
		}},
		{"missing-checksum", func(c *galleryCatalog) {
			v := &c.Results[0].Extensions[0].Versions[0]
			for i := range v.Properties {
				if v.Properties[i].Key == "Microsoft.VisualStudio.Services.VsixSha256" {
					v.Properties = append(v.Properties[:i], v.Properties[i+1:]...)
					break
				}
			}
		}},
		{"duplicate-checksum", func(c *galleryCatalog) {
			v := &c.Results[0].Extensions[0].Versions[0]
			for _, p := range v.Properties {
				if p.Key == "Microsoft.VisualStudio.Services.VsixSha256" {
					v.Properties = append(v.Properties, p)
					break
				}
			}
		}},
		{"missing-vsix", func(c *galleryCatalog) {
			v := &c.Results[0].Extensions[0].Versions[0]
			for i := range v.Files {
				if v.Files[i].AssetType == packageAsset {
					v.Files = append(v.Files[:i], v.Files[i+1:]...)
					break
				}
			}
		}},
		{"foreign-extension", func(c *galleryCatalog) { c.Results[0].Extensions[0].ExtensionName = "other" }},
		{"bad-asset-type", func(c *galleryCatalog) { c.Results[0].Extensions[0].Versions[0].Files[0].AssetType = "../manifest" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c galleryCatalog
			if err := json.Unmarshal(body, &c); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&c)
			changed, _ := json.Marshal(c)
			if _, err := readCatalog(
				bytes.NewReader(changed),
				int64(len(changed)),
				marketplaceGallery+"/extensionquery",
				q,
			); err == nil {
				t.Fatal("untrusted catalog admitted")
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
		size int64
	}{{"trailing-json", append(append([]byte(nil), body...), []byte(`{}`)...), int64(len(body) + 2)}, {"declared-size", body, int64(len(body) + 1)}, {"oversized", body, maxCatalog + 1}, {"empty", nil, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readCatalog(
				bytes.NewReader(tc.data),
				tc.size,
				marketplaceGallery+"/extensionquery",
				q,
			); err == nil {
				t.Fatal("malformed catalog admitted")
			}
		})
	}
}
