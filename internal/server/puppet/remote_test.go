package puppet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	puppetfmt "github.com/weaveplatform/artifactweave/internal/format/puppet"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRemoteDocumentValidation(t *testing.T) {
	const good = `{"results":[{"version":"1.0.0","file_uri":"/v3/files/acme-widget-1.0.0.tar.gz","file_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","file_size":100,"metadata":{"name":"acme-widget","version":"1.0.0","dependencies":[]}}],"pagination":{"offset":0,"total":1,"next":null}}`
	for _, tc := range []struct {
		name, body, base string
		valid            bool
	}{
		{"valid", good, "https://forge.example/v3/releases", true},
		{"empty", "", "https://forge.example/v3/releases", false},
		{"too large", strings.Repeat(" ", maxMetadataBytes+1), "https://forge.example/v3/releases", false},
		{"invalid base", good, "bad", false},
		{"empty url", strings.Replace(good, "/v3/files/acme-widget-1.0.0.tar.gz", "", 1), "https://forge.example/v3/releases", false},
		{"invalid url escape", strings.Replace(good, "/v3/files/acme-widget-1.0.0.tar.gz", "%xx", 1), "https://forge.example/v3/releases", false},
		{"cross origin", strings.Replace(good, "/v3/files/acme-widget-1.0.0.tar.gz", "https://evil.example/file", 1), "https://forge.example/v3/releases", false},
		{"credential url", strings.Replace(good, "/v3/files/acme-widget-1.0.0.tar.gz", "https://u:p@forge.example/a", 1), "https://forge.example/v3/releases", false},
		{"wrong name", strings.Replace(good, "acme-widget\",\"version", "acme-other\",\"version", 1), "https://forge.example/v3/releases", false},
		{"invalid checksum", strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("z", 64), 1), "https://forge.example/v3/releases", false},
		{"missing checksum", strings.Replace(good, strings.Repeat("a", 64), "", 1), "https://forge.example/v3/releases", false},
		{"missing next", strings.Replace(good, `"total":1`, `"total":1001`, 1), "https://forge.example/v3/releases", false},
		{"extra next", strings.Replace(good, `"next":null`, `"next":"https://evil.example/"`, 1), "https://forge.example/v3/releases", false},
		{"invalid json", "{", "https://forge.example/v3/releases", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseVersions([]byte(tc.body), "acme-widget", tc.base, 0)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
	var doc upstreamDocument
	if err := json.Unmarshal([]byte(good), &doc); err != nil {
		t.Fatal(err)
	}
	doc.Results = append(doc.Results, doc.Results[0])
	doc.Pagination.Total = 2
	data, _ := json.Marshal(doc)
	if _, _, err := parseVersions(
		data,
		"acme-widget",
		"https://forge.example/v3/releases",
		0,
	); err == nil {
		t.Fatal("duplicate release")
	}
	doc.Results = nil
	for i := 0; i < 1000; i++ {
		v := fmt.Sprintf("1.0.%d", i)
		doc.Results = append(
			doc.Results,
			upstreamVersion{
				Version:    v,
				FileURI:    "/v3/files/file.tar.gz",
				FileSHA256: strings.Repeat("a", 64),
				FileSize:   100,
				Metadata:   puppetfmt.Manifest{Name: "acme-widget", Version: v},
			},
		)
	}
	next := "/v3/releases?offset=1000"
	doc.Pagination = &pagination{Offset: 0, Total: 1001, Next: &next}
	data, _ = json.Marshal(doc)
	if got, more, err := parseVersions(
		data,
		"acme-widget",
		"https://forge.example/v3/releases",
		0,
	); err != nil || !more ||
		len(got) != 1000 {
		t.Fatalf("bounded page: %d %t %v", len(got), more, err)
	}
}

func TestUnitArchiveURLBoundary(t *testing.T) {
	c := repo.Config{URL: "https://forge.example/root/"}
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://forge.example/root/v3/files/a?x=1", true},
		{"https://forge.example/root/../secret", false},
		{"https://forge.example/root/%2e%2e/secret", false},
		{"https://other.example/root/a", false},
		{"https://forge.example/root/", false},
		{"https://forge.example/root//x", false},
		{"%xx", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			_, _, ok := withinRegistry(c, tc.url)
			if ok != tc.ok {
				t.Fatal(ok)
			}
		})
	}
	if _, err := (virtualSource{}).Item(t.Context(), nil, c, ""); err != source.ErrUnsupported {
		t.Fatal(err)
	}
	if p := Protocol(Options{RemoteSources: &source.Service{}}); p.Sources[repo.Remote] == nil {
		t.Fatal("remote source missing")
	}
}

func TestUnitSortVersions(t *testing.T) {
	releases := []release{
		{meta: puppetfmt.Metadata{Version: "1.0.0"}},
		{meta: puppetfmt.Metadata{Version: "2.0.0"}},
		{meta: puppetfmt.Metadata{Version: "1.0.0+build"}},
	}
	sortReleases(releases)
	if releases[0].meta.Version != "2.0.0" || releases[1].meta.Version != "1.0.0" {
		t.Fatal(releases)
	}
}

func TestAccRemoteArchiveRejectsMissingAuthorityBeforeSource(t *testing.T) {
	h := handler{k: &pkgroute.Kit{}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.remoteArchive(w, r, release{})
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.remoteArchive(
		w,
		r,
		release{
			checkRead:  func(context.Context, string) error { return source.ErrDenied },
			revalidate: func(context.Context) error { return nil },
		},
	)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestUnitArchiveFeedRootAndEncodedTraversal(t *testing.T) {
	base, _ := url.Parse("https://forge.example/root/v3/releases")
	for _, value := range []string{"https://forge.example/outside/file", "https://forge.example/root/%2e/file", "https://forge.example/root/%2e%2e/file"} {
		target, _ := url.Parse(value)
		if archiveWithinFeed(base, target) {
			t.Fatalf("accepted %s", value)
		}
	}
}
