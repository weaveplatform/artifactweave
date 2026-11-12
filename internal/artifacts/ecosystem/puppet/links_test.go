package puppet

import (
	"archive/tar"
	"bytes"
	"testing"
)

// A module record's homepage_url and issues_url come from metadata.json's
// project_page and issues_url, as the Forge answers them (#1288): read
// under exactly those keys and only as strings, so a link of another type
// leaves the archive valid and the record's link null.
func TestUnitManifestLinks(t *testing.T) {
	for manifest, want := range map[string][2]string{
		`{"project_page":"https://a.invalid/?x=1&y=2","issues_url":"https://a.invalid/i"}`: {
			"https://a.invalid/?x=1&y=2", "https://a.invalid/i",
		},
		`{"Project_Page":"https://a.invalid","issues_url":5}`:      {"", ""},
		`{"project_page":null,"issues_url":["https://a.invalid"]}`: {"", ""},
		`{"project_page":"https://a.invalid"}`:                     {"https://a.invalid", ""},
		`[1]`:                                                      {"", ""},
		`not json`:                                                 {"", ""},
	} {
		if page, issues := ManifestLinks([]byte(manifest)); page != want[0] || issues != want[1] {
			t.Errorf("%s: got %q %q", manifest, page, issues)
		}
	}
	m := []byte(`{"name":"acme-widget","version":"1.2.3","project_page":"https://a.invalid/w",` +
		`"issues_url":{"url":"https://a.invalid/i"}}`)
	archive := archiveFixture(t,
		[]tar.Header{{Name: "acme-widget-1.2.3/metadata.json", Mode: 0o644, Size: int64(len(m))}},
		[][]byte{m})
	meta, err := ReadArchive(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProjectPage != "https://a.invalid/w" || meta.IssuesURL != "" {
		t.Fatalf("archive links: %+v", meta)
	}
}
