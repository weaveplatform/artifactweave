package pubhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const metadataBase = "https://pub.example/api/packages/widget_pub"

func TestUnitParseVersionsRejectsUntrustedMetadata(t *testing.T) {
	const good = `{"name":"widget_pub","versions":[{"version":"1.2.3","archive_url":"../../packages/widget_pub/versions/1.2.3.tar.gz","archive_sha256":"` +
		`aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` +
		`","pubspec":{"name":"widget_pub","version":"1.2.3"}}]}`
	versions, err := parseVersions([]byte(good), "widget_pub", metadataBase)
	if err != nil || len(versions) != 1 || versions[0].remoteURL !=
		"https://pub.example/packages/widget_pub/versions/1.2.3.tar.gz" {
		t.Fatalf("valid relative archive URL: %+v, %v", versions, err)
	}
	for _, tc := range []struct {
		name string
		body string
		base string
	}{
		{"wrong package", strings.Replace(good, `"name":"widget_pub"`, `"name":"other"`, 1), metadataBase},
		{"duplicate version", strings.Replace(good, `}]}`, `},{"version":"1.2.3","archive_url":"https://pub.example/archive","pubspec":{"name":"widget_pub","version":"1.2.3"}}]}`, 1), metadataBase},
		{"invalid version", strings.Replace(good, `"version":"1.2.3"`, `"version":"../bad"`, 1), metadataBase},
		{"wrong pubspec", strings.Replace(good, `"pubspec":{"name":"widget_pub"`, `"pubspec":{"name":"other"`, 1), metadataBase},
		{"missing pubspec", strings.Replace(good, `"pubspec":{"name":"widget_pub","version":"1.2.3"}`, `"pubspec":null`, 1), metadataBase},
		{"missing archive url", strings.Replace(good, `"archive_url":"../../packages/widget_pub/versions/1.2.3.tar.gz"`, `"archive_url":""`, 1), metadataBase},
		{"file archive url", strings.Replace(good, `../../packages/widget_pub/versions/1.2.3.tar.gz`, `file:///etc/passwd`, 1), metadataBase},
		{"credential url", strings.Replace(good, `../../packages/widget_pub/versions/1.2.3.tar.gz`, `https://user:pass@pub.example/archive`, 1), metadataBase},
		{"fragment url", strings.Replace(good, `../../packages/widget_pub/versions/1.2.3.tar.gz`, `https://pub.example/archive#secret`, 1), metadataBase},
		{"bad checksum", strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), metadataBase},
		{"bad response base", good, "not-a-url"},
		{"oversized metadata", strings.Repeat(" ", maxMetadataBytes+1), metadataBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseVersions([]byte(tc.body), "widget_pub", tc.base); err == nil {
				t.Fatal("untrusted upstream metadata accepted")
			}
		})
	}
}

func TestUnitWithinRegistryRequiresDescendantURL(t *testing.T) {
	c := repo.Config{URL: "https://pub.example/root/pub/"}
	for _, tc := range []struct {
		url  string
		path string
		ok   bool
	}{
		{"https://pub.example/root/pub/packages/widget_pub/1.tar.gz?x=1", "packages/widget_pub/1.tar.gz", true},
		{"https://pub.example/root/publish/archive", "", false},
		{"https://other.example/root/pub/archive", "", false},
		{"http://pub.example/root/pub/archive", "", false},
		{"https://pub.example/root/pub/../secret", "", false},
		{"https://pub.example/root/pub/%2e%2e/secret", "", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			path, _, ok := withinRegistry(c, tc.url)
			if path != tc.path || ok != tc.ok {
				t.Fatalf("withinRegistry(%q) = %q, %t", tc.url, path, ok)
			}
		})
	}
}

func TestUnitVirtualSourceHasNoSingleItemLookup(t *testing.T) {
	_, err := virtualSource{}.Item(t.Context(), nil, repo.Config{}, "widget_pub")
	if !errors.Is(err, source.ErrUnsupported) {
		t.Fatalf("virtual item lookup: %v", err)
	}
}

// A remote release always carries the read check and revalidation of the
// metadata that listed it; one without either is refused, not fetched.
func TestUnitRemoteArchiveRefusesReleaseWithoutMetadataAuthority(t *testing.T) {
	h := handler{k: pkgroute.Mount(chi.NewRouter(), pkgroute.Options{}, Protocol(Options{}))}
	allowed := func(context.Context, string) error { return nil }
	current := func(context.Context) error { return nil }
	for name, rel := range map[string]release{
		"no read check":   {revalidate: current},
		"no revalidation": {checkRead: allowed},
	} {
		rel.remoteURL = "https://pub.example/packages/widget_pub/versions/1.2.3.tar.gz"
		rec := httptest.NewRecorder()
		h.remoteArchive(rec, httptest.NewRequest(http.MethodGet, "/", nil), rel)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
