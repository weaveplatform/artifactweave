package chef

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	cheffmt "github.com/weaveplatform/artifactweave/internal/format/chef"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitUpstreamVersionIdentityAndURLBoundaries(t *testing.T) {
	c := repo.Config{URL: "https://supermarket.example/root"}
	valid := map[string]any{
		"version":           "1.2",
		"file":              "https://objects.example/archive.tgz?signature=opaque",
		"cookbook":          "https://supermarket.example/root/api/v1/cookbooks/widget",
		"tarball_file_size": 123,
		"dependencies":      map[string]string{"helper": "~> 1.0"},
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"version mismatch", "version", "2.0"},
		{"size zero", "tarball_file_size", 0},
		{"size excessive", "tarball_file_size", cheffmt.MaxArchiveBytes + 1},
		{"archive empty", "file", ""},
		{"archive malformed", "file", "%zz"},
		{"archive credentials", "file", "https://user:secret@objects.example/file"},
		{"archive traversal", "file", "https://objects.example/a/../file"},
		{"archive scheme", "file", "file:///etc/passwd"},
		{"archive fragment", "file", "https://objects.example/file#fragment"},
		{"wrong cookbook", "cookbook", "https://other.example/root/api/v1/cookbooks/widget"},
		{"malformed cookbook", "cookbook", "%zz"},
		{"invalid checksum", "sha256", "zz"},
		{"short checksum", "sha256", "abcd"},
		{"self dependency", "dependencies", map[string]string{"widget": ">= 0.0"}},
		{"invalid constraint", "dependencies", map[string]string{"helper": "bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{}
			for k, v := range valid {
				doc[k] = v
			}
			doc[tc.key] = tc.value
			data, _ := json.Marshal(doc)
			if _, err := parseVersion(data, "widget", "1.2", c); err == nil {
				t.Fatal("accepted invalid upstream metadata")
			}
		})
	}
	data, _ := json.Marshal(valid)
	rel, err := parseVersion(data, "widget", "1.2", c)
	if err != nil || rel.meta.Version != "1.2" ||
		!strings.Contains(rel.remoteURL, "signature=opaque") {
		t.Fatalf("valid signed object URL: %+v %v", rel, err)
	}
	valid["file"] = "../../../../archives/widget.tgz"
	data, _ = json.Marshal(valid)
	if _, err := parseVersion(data, "widget", "1.2", c); err != nil {
		t.Fatalf("relative archive URL: %v", err)
	}
	for _, raw := range []string{
		`{"version":"1.2","file":"/x","cookbook":"https://supermarket.example/root/api/v1/cookbooks/widget","tarball_file_size":123,"dependencies":{"helper":">= 1.0","helper":"< 1.0"}}`,
		`{"version":"1.2","file":"/x","cookbook":"https://supermarket.example/root/api/v1/cookbooks/widget","tarball_file_size":123,"platforms":{"ubuntu":">= 20.04","ubuntu":"< 20.04"}}`,
		`{"Version":"1.2","file":"/x","cookbook":"https://supermarket.example/root/api/v1/cookbooks/widget","tarball_file_size":123}`,
	} {
		if _, err := parseVersion([]byte(raw), "widget", "1.2", c); err == nil {
			t.Fatal("accepted ambiguous JSON")
		}
	}
	valid["file"] = "https://objects.example/archive.tgz"
	data, _ = json.Marshal(valid)
	for _, base := range []string{"%zz", "file:///tmp", "https://u:p@registry.example/"} {
		if _, err := parseVersion(data, "widget", "1.2", repo.Config{URL: base}); err == nil {
			t.Fatal("accepted invalid configured registry")
		}
	}
}

func TestUnitUpstreamCookbookVersionCatalogBoundaries(t *testing.T) {
	c := repo.Config{URL: "https://supermarket.example/root"}
	prefix := "https://supermarket.example/root/api/v1/cookbooks/widget/versions/"
	for _, versions := range [][]string{{prefix + "1_0", prefix + "2_0_0"}, {"1_0"}} {
		data, _ := json.Marshal(cookbookDocument{Name: "widget", Versions: versions})
		_, got, err := cookbookVersions(data, "widget", c)
		if err != nil || len(got) != len(versions) {
			t.Fatalf("valid version links: %v %v", got, err)
		}
	}
	for _, versions := range [][]string{nil, {prefix + "1_0", prefix + "1_0"}, {prefix + "latest"}, {prefix + "1_0?x=y"}, {"%zz"}, {"https://evil.example/1_0"}, {prefix + "1_0/more"}, make([]string, maxVersions+1)} {
		data, _ := json.Marshal(cookbookDocument{Name: "widget", Versions: versions})
		if _, _, err := cookbookVersions(data, "widget", c); err == nil {
			t.Fatalf("accepted invalid versions: %v", versions[:min(3, len(versions))])
		}
	}
	data, _ := json.Marshal(cookbookDocument{Name: "widget", Versions: []string{prefix + "1_0"}})
	if _, _, err := cookbookVersions(data, "other", c); err == nil {
		t.Fatal("accepted wrong cookbook identity")
	}
	if _, _, err := cookbookVersions(data, "widget", repo.Config{URL: "%zz"}); err == nil {
		t.Fatal("accepted invalid base")
	}
	for _, raw := range []string{"", `{`, "null", `{"name":"widget","name":"other"}`, strings.Repeat(" ", maxMetadataBytes+1)} {
		var doc cookbookDocument
		if decode([]byte(raw), &doc) == nil {
			t.Fatal("accepted malformed JSON")
		}
	}
	if validURL(nil) {
		t.Fatal("accepted nil URL")
	}
	for _, raw := range []string{"https://example.com/a\\b", "https://example.com/./b", "https://example.com/a%00b"} {
		u, _ := url.Parse(raw)
		if validURL(u) {
			t.Fatal("accepted unsafe URL")
		}
	}
}

func TestUnitVersionOrderAndEmptyDependencyObjects(t *testing.T) {
	releases := []release{
		{meta: cheffmt.Metadata{Version: "1.10"}},
		{meta: cheffmt.Metadata{Version: "2.0.0"}},
		{meta: cheffmt.Metadata{Version: "1.2.0"}},
		{meta: cheffmt.Metadata{Version: "1.2"}},
	}
	sortReleases(releases)
	want := []string{"2.0.0", "1.10", "1.2", "1.2.0"}
	for i, rel := range releases {
		if rel.meta.Version != want[i] {
			t.Fatalf("order %d = %s, want %s", i, rel.meta.Version, want[i])
		}
	}
	if nonNil(nil) == nil {
		t.Fatal("native client expects an object, not null")
	}
}
