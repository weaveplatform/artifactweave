package p2

import (
	"errors"
	"testing"
)

func TestUnitP2ParseSiteURL(t *testing.T) {
	for _, tc := range []struct {
		raw         string
		key, suffix string
		local, ok   bool
	}{
		{raw: "https://download.eclipse.org/releases/luna", ok: true},
		{raw: "http://host.example:8081/artifactory/p2-virtual/", ok: true},
		{raw: "https://host.example/site.zip!/", ok: true},
		{raw: "https://host.example", ok: true},
		{raw: "local://p2-local", key: "p2-local", local: true, ok: true},
		{raw: "local://p2-local/", key: "p2-local", local: true, ok: true},
		{raw: "local://p2-local/path/to/metadata", key: "p2-local", suffix: "path/to/metadata", local: true, ok: true},
		{raw: "local://p2-local/eclipse-repository.zip!/", key: "p2-local", suffix: "eclipse-repository.zip!/", local: true, ok: true},
		{raw: "local://"},
		{raw: "local:///suffix"},
		{raw: "local://p2 local"},
		{raw: "local://p2%2flocal"},
		{raw: "local://p2-local/../other"},
		{raw: "local://p2-local//double"},
		{raw: "local://p2-local/a[b"},
		{raw: "ftp://download.eclipse.org/releases/luna"},
		{raw: "https:///releases/luna"},
		{raw: "https://user:secret@download.eclipse.org/releases/luna"},
		{raw: "https://download.eclipse.org/releases/luna?x=1"},
		{raw: "https://download.eclipse.org/releases/luna#top"},
		{raw: "https://download.eclipse.org/releases/../luna"},
		{raw: "https://download.eclipse.org/releases/%2e%2e/luna"},
		{raw: "https://download.eclipse.org/releases/a%2fb"},
		{raw: "https://download.eclipse.org/releases/a b"},
		{raw: "https://download.eclipse.org/releases/a\"b"},
		{raw: "https://download.eclipse.org\\releases"},
		{raw: "mailto:p2@example.org"},
		{raw: "relative/path"},
		{raw: ""},
	} {
		s, err := ParseSiteURL(tc.raw)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%q: %v, want ok %v", tc.raw, err, tc.ok)
			continue
		}
		if tc.ok &&
			(s.Raw != tc.raw || s.Local() != tc.local || s.Key != tc.key || s.Suffix != tc.suffix) {
			t.Errorf("%q: %+v", tc.raw, s)
		}
	}
}

func TestUnitP2SiteChild(t *testing.T) {
	members := []Member{
		{Key: "p2-local"},
		{Key: "download.eclipse.org", URL: "https://download.eclipse.org/"},
		{Key: "eclipse-releases", URL: "https://download.eclipse.org/releases/"},
		{Key: "bare-host", URL: "https://bare.example"},
		{Key: "archive", URL: "https://zip.example/site.zip!/"},
		{Key: "zip-host", URL: "https://zip.example/"},
	}
	for _, tc := range []struct {
		raw, key, child string
	}{
		// The first member in order whose url starts the URL serves it.
		{"https://download.eclipse.org/releases/luna", "download.eclipse.org", "download.eclipse.org/releases/luna"},
		{"https://download.eclipse.org/", "download.eclipse.org", "download.eclipse.org"},
		{"https://download.eclipse.org", "download.eclipse.org", "download.eclipse.org"},
		{"https://bare.example/site", "bare-host", "bare-host/site"},
		{"https://bare.example", "bare-host", "bare-host"},
		{"https://bare.example.other/site", "", ""},
		// Scheme and host compare without case, the path with it.
		{"HTTPS://Download.Eclipse.ORG/releases/luna", "download.eclipse.org", "download.eclipse.org/releases/luna"},
		{"https://download.eclipse.org/Releases/luna", "download.eclipse.org", "download.eclipse.org/Releases/luna"},
		{"https://zip.example/Site.zip!/", "", ""},
		{"https://ZIP.example/site.zip!/", "archive", "archive"},
		{"https://elsewhere.example/site", "", ""},
		// An archive entry is served only by the remote whose url is it.
		{"https://zip.example/site.zip!/", "archive", "archive"},
		{"https://zip.example/other.zip!/", "", ""},
		{"https://zip.example/plain/", "zip-host", "zip-host/plain/"},
		{"local://p2-local", "p2-local", "p2-local"},
		{"local://p2-local/eclipse-repository.zip!/", "p2-local", "p2-local/eclipse-repository.zip!/"},
		{"local://p2-missing/site", "", ""},
	} {
		s, err := ParseSiteURL(tc.raw)
		if err != nil {
			t.Fatalf("%q: %v", tc.raw, err)
		}
		key, child, ok := SiteChild(s, members)
		if key != tc.key || child != tc.child || ok != (tc.key != "") {
			t.Errorf("%q: %q %q %v, want %q %q", tc.raw, key, child, ok, tc.key, tc.child)
		}
	}
	// A url that is no absolute URL covers nothing.
	if _, ok := Covers("download.eclipse.org/", "https://download.eclipse.org/releases"); ok {
		t.Error("a url without a scheme covered a site")
	}
	// A key is path-escaped in the child, the rest of the URL kept as given.
	s, _ := ParseSiteURL("https://odd.example/a%20b/c")
	if _, child, _ := SiteChild(
		s,
		[]Member{{Key: "odd key's", URL: "https://odd.example/"}},
	); child != "odd%20key%27s/a%20b/c" {
		t.Errorf("escaped child %q", child)
	}
}
