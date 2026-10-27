package jetbrainsplugins

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitNativeIDERequestGrammarAndCacheIdentity(t *testing.T) {
	c := repo.Config{URL: "https://plugins.jetbrains.com/"}
	compatible := "build=IC-252.28238.7&os=Linux+6.12.76-linuxkit&pluginXmlId=com.weave.fixture"
	r, id, err := parseResource(c, "api/search/updates/compatible", compatible)
	if err != nil || id != "" || r.Kind != source.Metadata ||
		r.PermissionPath != "plugins/com.weave.fixture" ||
		!strings.Contains(r.ExternalURL, "api/search/updates/compatible?") {
		t.Fatalf("compatible metadata: %+v, %q, %v", r, id, err)
	}
	meta, _, err := parseResource(c, "files/98765/12345/meta.json", "")
	if err != nil || !meta.PermissionRoot || meta.Kind != source.Metadata ||
		meta.Path != "catalog/files/98765/12345.json" {
		t.Fatalf("update metadata: %+v, %v", meta, err)
	}
	a, id, err := parseResource(c, "pluginManager",
		"id=com.weave.fixture&build=IC-252.28238.7&uuid=opaque-client-telemetry&updatedFrom=")
	if err != nil || id != "com.weave.fixture" || a.Kind != source.Artifact ||
		strings.Contains(
			a.ExternalURL,
			"uuid",
		) || !strings.HasPrefix(a.Path, "plugins/com.weave.fixture/") {
		t.Fatalf("plugin archive: %+v, %q, %v", a, id, err)
	}
	b, _, err := parseResource(
		c,
		"pluginManager",
		"id=com.weave.fixture&build=IC-252.28238.7&uuid=other&updatedFrom=",
	)
	if err != nil || a.Path != b.Path {
		t.Fatalf("client telemetry changed cache identity: %q != %q: %v", a.Path, b.Path, err)
	}
}

func TestUnitNativeIDEArchitectureSeparatesCompatibleUpdates(t *testing.T) {
	c := repo.Config{URL: "https://plugins.jetbrains.com/"}
	paths := map[string]bool{}
	for _, arch := range []string{"X86_64", "ARM64"} {
		t.Run(strings.ToLower(strings.ReplaceAll(arch, "_", " ")), func(t *testing.T) {
			query := url.Values{
				"build":       {"IU-253.28294.334"},
				"os":          {"Linux 6.11.0-1018-azure"},
				"pluginXmlId": {"com.weave.fixture"},
				"arch":        {arch},
			}
			r, _, err := parseResource(c, "api/search/updates/compatible", query.Encode())
			if err != nil {
				t.Fatalf("native IDE architecture %q rejected: %v", arch, err)
			}
			u, err := url.Parse(r.ExternalURL)
			if err != nil || u.Query().Get("arch") != arch {
				t.Fatalf("architecture lost from upstream query: %q, %v", r.ExternalURL, err)
			}
			if r.PermissionPath != "plugins/com.weave.fixture" || paths[r.Path] {
				t.Fatalf("architecture changed permissions or reused a cache entry: %+v", r)
			}
			paths[r.Path] = true
		})
	}
}

func TestUnitRejectsUnboundedOrAmbiguousMarketplaceRequests(t *testing.T) {
	c := repo.Config{URL: "https://plugins.jetbrains.com/"}
	for _, tc := range []struct{ path, query string }{
		{"pluginManager", "id=com.weave.fixture&build=IC-252.28238.7&evil=1"},
		{"pluginManager", "id=com.weave.fixture&id=com.other&build=IC-252.28238.7"},
		{"pluginManager", "id=%0a&build=IC-252.28238.7"},
		{"api/search/updates/compatible", "pluginXmlId=com.weave.fixture"},
		{"api/search/updates/compatible", "build=IU-253.28294.334&pluginXmlId=com.weave.fixture&arch=../ARM64"},
		{"api/search/updates/compatible", "build=IU-253.28294.334&pluginXmlId=com.weave.fixture&arch=" + strings.Repeat("A", 33)},
		{"api/search/updates/compatible", "build=IU-253.28294.334&pluginXmlId=com.weave.fixture&arch=X86_64&arch=ARM64"},
		{"api/search/updates/compatible", "build=IU-253.28294.334&pluginXmlId=com.weave.fixture&unexpected=1"},
		{"files/0/12345/meta.json", ""},
		{"files/98765/12345/meta.json", "token=secret"},
		{"api/search/plugins", "search=foo&build=IC-252.28238.7%0d%0aHeader"},
		{"pluginManager/../../etc", ""},
		{"files%2F98765%2F12345%2Fmeta.json", ""},
	} {
		if _, _, err := parseResource(c, tc.path, tc.query); err == nil {
			t.Errorf("accepted %s?%s", tc.path, tc.query)
		}
	}
}

func TestUnitOpaqueMarketplacePluginIDsUseSafeCacheKeys(t *testing.T) {
	c := repo.Config{URL: "https://plugins.jetbrains.com"}
	for _, id := range []string{"Gold Section", "RSS/Atom feed reader", "../legacy"} {
		values := url.Values{"build": {"IC-252.28238.7"}, "pluginXmlId": {id}}
		r, _, err := parseResource(c, "api/search/updates/compatible", values.Encode())
		if err != nil || !strings.HasPrefix(r.Path, "plugins/opaque-") ||
			strings.ContainsAny(pluginCacheKey(id), " /\\") ||
			!strings.Contains(r.ExternalURL, "pluginXmlId=") {
			t.Fatalf("opaque plugin ID %q: %+v %v", id, r, err)
		}
	}
}

func TestUnitMarketplaceRedirectGuardAllowsOnlyCatalogAndArchiveCDN(t *testing.T) {
	target, _ := url.Parse("https://plugins.jetbrains.com/pluginManager?id=com.weave.fixture")
	guard := redirectGuard(target, true)
	for _, raw := range []string{
		target.String(),
		"https://plugins.jetbrains.com/files/98765/12345/weave-fixture.zip?updateId=12345",
		"https://downloads.marketplace.jetbrains.com/files/98765/12345/weave-fixture.zip?updateId=12345",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err != nil {
			t.Errorf("rejected intended redirect %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"https://evil.example/files/98765/12345/weave-fixture.zip",
		"http://downloads.marketplace.jetbrains.com/files/98765/12345/weave-fixture.zip",
		"https://downloads.marketplace.jetbrains.com/files/98765/12345/../../admin.zip",
		"https://downloads.marketplace.jetbrains.com/api/secret",
		"https://plugins.jetbrains.com/files/98765/12345/weave-fixture.exe",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err == nil {
			t.Errorf("accepted unsafe redirect %s", raw)
		}
	}
}
