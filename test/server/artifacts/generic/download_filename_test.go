package generic_test

// This file holds the file name headers of #1578 and the content browsing
// answers of #1579: how a download names its file in X-Artifactory-Filename
// and Content-Disposition, and what a repository that allows content
// browsing changes; and #1594's file served from a remote's stream, named
// as the copy it would keep.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// browserAgent is a browser's User-Agent as 7.161.26's
// BrowserRequestHelper.isBrowserUserAgent finds one: it starts with
// Mozilla and names WebKit, Blink and Gecko.
const browserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// named is a file's name as a download sends it: X-Artifactory-Filename and
// Content-Disposition, "" for none.
type named struct{ filename, disposition string }

// wantNamed fails t unless rec answers status and names its file as want,
// sending no Content-Disposition when want has none.
func wantNamed(t *testing.T, label string, rec *httptest.ResponseRecorder, status int, want named) {
	t.Helper()
	h := rec.Header()
	_, sent := h["Content-Disposition"]
	if rec.Code != status || h.Get("X-Artifactory-Filename") != want.filename ||
		h.Get("Content-Disposition") != want.disposition || sent != (want.disposition != "") {
		t.Errorf("%s: %d, X-Artifactory-Filename %q, Content-Disposition %q; want %d, %q, %q",
			label, rec.Code, h.Get("X-Artifactory-Filename"), h.Get("Content-Disposition"),
			status, want.filename, want.disposition)
	}
}

// namedRequests are the requests a file download names its file on, with
// the status each answers: a GET, a HEAD, a range and a 304.
func namedRequests(sha string) []struct {
	label   string
	method  string
	status  int
	headers []string
} {
	return []struct {
		label   string
		method  string
		status  int
		headers []string
	}{
		{"GET", http.MethodGet, http.StatusOK, nil},
		{"HEAD", http.MethodHead, http.StatusOK, nil},
		{"range", http.MethodGet, http.StatusPartialContent, []string{"Range", "bytes=1-2"}},
		{"304", http.MethodGet, http.StatusNotModified, []string{"If-None-Match", sha}},
	}
}

// TestAccDownloadFileNameIsQueryEncoded: a file's name is sent in
// X-Artifactory-Filename query-encoded, and in the item route's filename*
// the same way, on a GET, a HEAD, a range and a 304, and on a checksum's
// HEAD and 304, which carry its file's headers. 7.161.26's
// HttpArtifactoryResponse and JerseyArtifactoryResponse send
// BasicHttpUtils.encodeQuery(name): a space is %20 and '+', '(', ';' and
// '\” stay. A name outside printable ASCII is named by filename* alone,
// every byte but RFC 3986's unreserved characters encoded
// (RequestResponseHelper.setHeaderForNonAscii); the package download
// service, Vagrant's here, quotes the name with no filename* (#1505). Both
// were sent raw, and filename* path-escaped beside the quoted name (#1578).
func TestAccDownloadFileNameIsQueryEncoded(t *testing.T) {
	w, data, sha := downloadWorld(t)
	body := string(data)
	for _, c := range []struct {
		deploy, path string
		want         named
	}{
		{
			"/artifactory/server-local/d/a%20b+c(1)%3Bd'e.zip", "/artifactory/server-local/d/a%20b+c(1)%3Bd'e.zip",
			named{
				"a%20b+c(1);d'e.zip",
				`attachment; filename="a b+c(1);d'e.zip"; filename*=UTF-8''a%20b+c(1);d'e.zip`,
			},
		},
		{
			"/artifactory/server-local/d/na%C3%AFve%201.0.zip", "/artifactory/server-local/d/na%C3%AFve%201.0.zip",
			named{"na%C3%AFve%201.0.zip", "attachment; filename*=UTF-8''na%C3%AFve%201.0.zip"},
		},
		{
			"/artifactory/boxes/acme/focal%201+0.box;box_name=focal;box_provider=virtualbox;box_version=1.0",
			"/artifactory/api/vagrant/boxes/acme/focal%201+0.box",
			named{"focal%201+0.box", `attachment; filename="focal 1+0.box"`},
		},
		{
			"/artifactory/boxes/acme/f%C3%B5cal%201+0.box;box_name=focal;box_provider=virtualbox;box_version=1.0",
			"/artifactory/api/vagrant/boxes/acme/f%C3%B5cal%201+0.box",
			named{"f%C3%B5cal%201+0.box", "attachment; filename*=UTF-8''f%C3%B5cal%201%2B0.box"},
		},
	} {
		harness.WantStatus(t, w.Do(http.MethodPut, c.deploy, "admin", body), http.StatusCreated)
		for _, q := range namedRequests(sha) {
			wantNamed(t, q.label+" "+c.path, w.Do(q.method, c.path, "reader", "", q.headers...),
				q.status, c.want)
		}
		if c.path != c.deploy {
			continue
		}
		wantNamed(t, "checksum HEAD "+c.path, w.Do(http.MethodHead, c.path+".sha1", "reader", ""),
			http.StatusOK, c.want)
		wantNamed(t, "checksum 304 "+c.path,
			w.Do(http.MethodGet, c.path+".sha1", "reader", "", "If-None-Match", sha),
			http.StatusNotModified, c.want)
		wantNamed(t, "checksum GET "+c.path, w.Do(http.MethodGet, c.path+".sha1", "reader", ""),
			http.StatusOK, named{})
	}
}

// TestAccArchiveEntryFileNameIsQueryEncoded: an archive entry's
// X-Artifactory-Filename is its name query-encoded, GET and HEAD, as
// 7.161.26's setBasicHeaders hands a ZipEntryResource's entry name to the
// same encodeQuery as a file's. It was sent raw (#1578).
func TestAccArchiveEntryFileNameIsQueryEncoded(t *testing.T) {
	w, _, _ := downloadWorld(t)
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, n := range []string{"e/a b+c(1).txt", "e/naïve.txt"} {
		f, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte("entry")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	harness.WantStatus(
		t,
		w.Do(http.MethodPut, "/artifactory/libs-local/z/names.zip", "admin", zb.String()),
		http.StatusCreated,
	)
	for entry, want := range map[string]string{
		"a%20b+c(1).txt": "a%20b+c(1).txt",
		"na%C3%AFve.txt": "na%C3%AFve.txt",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.Do(method, "/artifactory/libs-local/z/names.zip!/e/"+entry, "reader", "")
			if rec.Code != http.StatusOK || rec.Header().Get("X-Artifactory-Filename") != want {
				t.Errorf(
					"%s %s: %d, X-Artifactory-Filename %q; want 200, %q",
					method,
					entry,
					rec.Code,
					rec.Header().Get("X-Artifactory-Filename"),
					want,
				)
			}
		}
	}
}

// browsingWorld is downloadWorld with repositories that allow content
// browsing: brows-local (server-generated checksums), brows-fed and the
// Vagrant boxes-brows; brows-virt, a virtual repository over brows-local,
// and flagged-virt, a virtual repository with the flag over libs-local,
// which has none. It returns downloadWorld's box and its SHA-1 too.
func browsingWorld(t *testing.T) (*harness.ItemWorld, []byte, string) {
	t.Helper()
	w, data, sha := downloadWorld(t)
	for _, c := range []repo.Config{
		{
			Key: "brows-local", Class: repo.Local, ArchiveBrowsingEnabled: new(true),
			ChecksumPolicyType: repo.ServerGeneratedChecksums,
		},
		{Key: "brows-fed", Class: repo.Federated, ArchiveBrowsingEnabled: new(true)},
		{Key: "boxes-brows", Class: repo.Local, PackageType: repo.Vagrant, ArchiveBrowsingEnabled: new(true)},
		{Key: "brows-virt", Class: repo.Virtual, Repositories: []string{"brows-local"}},
		{
			Key: "flagged-virt", Class: repo.Virtual, Repositories: []string{"libs-local"},
			ArchiveBrowsingEnabled: new(true),
		},
	} {
		if err := w.DB.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w, data, sha
}

// TestAccBrowsableRepositoryNamesNoAttachment: a download from a local,
// federated or Vagrant repository that allows content browsing, on the item
// route and the package download service, and through a virtual repository
// over one, sends X-Artifactory-Filename and no Content-Disposition, on a
// GET, a HEAD, a range and a 304; a name outside printable ASCII is named
// inline by filename* alone, except on the 304. A virtual repository's own
// flag changes nothing: its member's decides. 7.161.26's
// RequestResponseHelper.setBasicHeaders sends the attachment only when
// contentBrowsingDisabled holds for the response's repository, which a
// virtual repository always does, and setHeaderForNonAscii names a
// non-ASCII file inline there on a GET and a HEAD. Every download sent the
// attachment (#1579).
func TestAccBrowsableRepositoryNamesNoAttachment(t *testing.T) {
	w, data, sha := browsingWorld(t)
	body := string(data)
	box := ";box_name=focal;box_provider=virtualbox;box_version=1.0"
	for _, c := range []struct {
		deploy string
		paths  []string
		want   named
	}{
		{
			"/artifactory/brows-local/d/file.bin",
			[]string{"/artifactory/brows-local/d/file.bin", "/artifactory/brows-virt/d/file.bin"},
			named{"file.bin", ""},
		},
		{
			"/artifactory/brows-fed/d/na%C3%AFve%201.0.zip",
			[]string{"/artifactory/brows-fed/d/na%C3%AFve%201.0.zip"},
			named{"na%C3%AFve%201.0.zip", "inline; filename*=UTF-8''na%C3%AFve%201.0.zip"},
		},
		{
			"/artifactory/boxes-brows/acme/focal-1.0.box" + box,
			[]string{"/artifactory/boxes-brows/acme/focal-1.0.box", "/artifactory/api/vagrant/boxes-brows/acme/focal-1.0.box"},
			named{"focal-1.0.box", ""},
		},
		{
			"/artifactory/boxes-brows/acme/f%C3%B5cal-1.0.box" + box,
			[]string{"/artifactory/api/vagrant/boxes-brows/acme/f%C3%B5cal-1.0.box"},
			named{"f%C3%B5cal-1.0.box", "inline; filename*=UTF-8''f%C3%B5cal-1.0.box"},
		},
		{
			"/artifactory/libs-local/d/plain.bin",
			[]string{"/artifactory/flagged-virt/d/plain.bin"},
			named{"plain.bin", `attachment; filename="plain.bin"; filename*=UTF-8''plain.bin`},
		},
	} {
		harness.WantStatus(t, w.Do(http.MethodPut, c.deploy, "admin", body), http.StatusCreated)
		for _, p := range c.paths {
			for _, q := range namedRequests(sha) {
				want := c.want
				if q.status == http.StatusNotModified && want.disposition != "" &&
					want.disposition[0] == 'i' {
					want.disposition = ""
				}
				wantNamed(
					t,
					q.label+" "+p,
					w.Do(q.method, p, "reader", "", q.headers...),
					q.status,
					want,
				)
			}
		}
	}
	// A checksum's HEAD and 304 carry its file's headers, by the flag of
	// the repository that holds the file.
	for _, p := range []string{"/artifactory/brows-local/d/file.bin.sha1", "/artifactory/brows-virt/d/file.bin.sha1"} {
		wantNamed(
			t,
			"HEAD "+p,
			w.Do(http.MethodHead, p, "reader", ""),
			http.StatusOK,
			named{"file.bin", ""},
		)
		wantNamed(t, "304 "+p, w.Do(http.MethodGet, p, "reader", "", "If-None-Match", sha),
			http.StatusNotModified, named{"file.bin", ""})
	}
}

// TestAccBrowsableRepositorySandboxesBrowsers: a browser's GET of a file
// from a repository that allows content browsing, directly or through a
// virtual repository over it, gets Content-Security-Policy: sandbox, and a
// .pom application/xml and a .md, .md5 or .sha1 text/plain; a range it
// cannot satisfy keeps the policy. A HEAD, a client that is not a browser,
// a repository without the flag, a virtual repository with the flag over a
// member without it, and a checksum asked for through a virtual repository
// get neither. 7.161.26's RequestResponseHelper.getModifiedMimeType sets
// them in sendBodyResponse, by the response's repository, before the range
// is read, and in sendBodyResponseForChecksum by the requested repository;
// BrowserRequestHelper.getMimeType gives the types (RTFACT-8743). No
// download sent either (#1579).
func TestAccBrowsableRepositorySandboxesBrowsers(t *testing.T) {
	w, _, _ := browsingWorld(t)
	for _, p := range []string{
		"/artifactory/brows-local/org/lib.pom", "/artifactory/brows-local/org/README.md",
		"/artifactory/brows-local/org/page.html", "/artifactory/libs-local/org/lib.pom",
	} {
		harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", "<project/>"), http.StatusCreated)
	}
	for _, c := range []struct {
		label, method, path string
		headers             []string
		status              int
		csp, contentType    string
	}{
		{"pom", http.MethodGet, "/artifactory/brows-local/org/lib.pom", nil, 200, "sandbox", "application/xml"},
		{"virtual pom", http.MethodGet, "/artifactory/brows-virt/org/lib.pom", nil, 200, "sandbox", "application/xml"},
		{"md", http.MethodGet, "/artifactory/brows-local/org/README.md", nil, 200, "sandbox", "text/plain"},
		{"html", http.MethodGet, "/artifactory/brows-local/org/page.html", nil, 200, "sandbox", "text/html"},
		{"sha1", http.MethodGet, "/artifactory/brows-local/org/lib.pom.sha1", nil, 200, "sandbox", "text/plain"},
		{"md5", http.MethodGet, "/artifactory/brows-local/org/lib.pom.md5", nil, 200, "sandbox", "text/plain"},
		{
			"416", http.MethodGet, "/artifactory/brows-local/org/lib.pom",
			[]string{"Range", "bytes=900-"},
			416, "sandbox", "",
		},
		{"HEAD", http.MethodHead, "/artifactory/brows-local/org/lib.pom", nil, 200, "", "application/x-maven-pom+xml"},
		{"no flag", http.MethodGet, "/artifactory/libs-local/org/lib.pom", nil, 200, "", "application/x-maven-pom+xml"},
		{
			"flagged virtual", http.MethodGet, "/artifactory/flagged-virt/org/lib.pom", nil, 200,
			"", "application/x-maven-pom+xml",
		},
		{
			"virtual checksum", http.MethodGet, "/artifactory/brows-virt/org/lib.pom.sha1", nil, 200,
			"", "application/x-checksum",
		},
	} {
		rec := w.Do(
			c.method,
			c.path,
			"reader",
			"",
			append([]string{"User-Agent", browserAgent}, c.headers...)...)
		h := rec.Header()
		if rec.Code != c.status || h.Get("Content-Security-Policy") != c.csp ||
			h.Get("Content-Type") != c.contentType {
			t.Errorf(
				"%s %s: %d, Content-Security-Policy %q, Content-Type %q; want %d, %q, %q",
				c.label,
				c.path,
				rec.Code,
				h.Get("Content-Security-Policy"),
				h.Get("Content-Type"),
				c.status,
				c.csp,
				c.contentType,
			)
		}
	}
	rec := w.Do(http.MethodGet, "/artifactory/brows-local/org/lib.pom", "reader", "",
		"User-Agent", "Apache-Maven/3.9.6 (Java 21; Linux)")
	if h := rec.Header(); h.Get("Content-Security-Policy") != "" ||
		h.Get("Content-Type") != "application/x-maven-pom+xml" {
		t.Errorf(
			"Maven GET: Content-Security-Policy %q, Content-Type %q",
			h.Get("Content-Security-Policy"),
			h.Get("Content-Type"),
		)
	}
}

// TestAccBrowsableRemoteNamesNoAttachment: a remote repository that allows
// content browsing serves a file from its cache, on the first download and
// again, by the remote's name and its cache's, with X-Artifactory-Filename
// and no Content-Disposition, and a checksum's HEAD with none either: a
// remote's cache reads the remote's flag
// (LocalCacheRepoDescriptor.isArchiveBrowsingEnabled, 7.161.26) (#1579).
func TestAccBrowsableRemoteNamesNoAttachment(t *testing.T) {
	w := newRemoteWorld(t)
	w.c.ArchiveBrowsingEnabled = new(true)
	w.update()
	w.up.Set("/b/file.jar", fakeupstream.Resource{Body: []byte(coldJar), Header: http.Header{
		"Content-Type": {javaArchive},
	}})
	for _, p := range []string{
		"/artifactory/remote/b/file.jar", "/artifactory/remote/b/file.jar",
		"/artifactory/remote-cache/b/file.jar",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			wantNamed(t, method+" "+p, w.request(method, p, "reader", ""), http.StatusOK,
				named{"file.jar", ""})
		}
	}
	wantNamed(
		t,
		"checksum HEAD",
		w.request(http.MethodHead, "/artifactory/remote/b/file.jar.sha1", "reader", ""),
		http.StatusOK,
		named{"file.jar", ""},
	)
}

// TestAccStreamedRemoteFileIsNamed: a remote that stores nothing serves a
// file from the upstream's stream named as the copy a remote that stores
// it serves, on the first download and again: X-Artifactory-Filename and
// the item route's Content-Disposition, by the remote's content browsing
// flag, on a GET, a HEAD, a cold HEAD, a range and a 304, a name outside
// printable ASCII by filename* alone; a browser's GET with the flag gets
// the sandbox and a .pom application/xml; a range it cannot satisfy names
// nothing. 7.161.26 answers the RemoteRepoResource of a remote that does
// not save it ("resource will be stream directly to the user",
// RemoteRepoBase) through RequestResponseHelper.sendBodyResponse and
// sendHeadResponse, as it answers a cached file, and setBasicHeaders names
// both alike. Such a stream was named by nothing (#1594).
func TestAccStreamedRemoteFileIsNamed(t *testing.T) {
	future := "Wed, 21 Oct 2099 07:28:00 GMT"
	for _, browse := range []bool{false, true} {
		w := newRemoteWorld(t)
		w.c.StoreArtifactsLocally = new(false)
		w.c.ArchiveBrowsingEnabled = new(browse)
		w.update()
		for _, f := range []string{"/b/a b.jar", "/b/naïve.jar", "/b/cold.jar", "/b/lib.pom"} {
			w.up.Set(f, fakeupstream.Resource{Body: []byte(coldJar), Header: http.Header{
				"Content-Type": {javaArchive},
			}})
		}
		spaced := named{"a%20b.jar", `attachment; filename="a b.jar"; filename*=UTF-8''a%20b.jar`}
		naive := named{"na%C3%AFve.jar", "attachment; filename*=UTF-8''na%C3%AFve.jar"}
		cold := named{"cold.jar", `attachment; filename="cold.jar"; filename*=UTF-8''cold.jar`}
		naiveNotModified := naive
		if browse {
			spaced.disposition, cold.disposition = "", ""
			naive.disposition = "inline; filename*=UTF-8''na%C3%AFve.jar"
			naiveNotModified.disposition = ""
		}
		label := fmt.Sprintf("browse %v: ", browse)
		ask := func(method, file string, headers ...string) *httptest.ResponseRecorder {
			return w.request(method, "/artifactory/remote/b/"+file, "reader", "", headers...)
		}
		wantNamed(t, label+"cold HEAD", ask(http.MethodHead, "cold.jar"), http.StatusOK, cold)
		first := ask(http.MethodGet, "a%20b.jar")
		wantNamed(t, label+"first GET", first, http.StatusOK, spaced)
		for _, q := range namedRequests(first.Header().Get("X-Checksum-Sha1")) {
			wantNamed(t, label+q.label, ask(q.method, "a%20b.jar", q.headers...), q.status, spaced)
		}
		wantNamed(
			t,
			label+"non-ASCII GET",
			ask(http.MethodGet, "na%C3%AFve.jar"),
			http.StatusOK,
			naive,
		)
		wantNamed(
			t,
			label+"non-ASCII HEAD",
			ask(http.MethodHead, "na%C3%AFve.jar"),
			http.StatusOK,
			naive,
		)
		wantNamed(
			t,
			label+"non-ASCII 304",
			ask(http.MethodGet, "na%C3%AFve.jar", "If-Modified-Since", future),
			http.StatusNotModified,
			naiveNotModified,
		)
		wantNamed(t, label+"416", ask(http.MethodGet, "a%20b.jar", "Range", "bytes=900-"),
			http.StatusRequestedRangeNotSatisfiable, named{})
		browser := ask(http.MethodGet, "lib.pom", "User-Agent", browserAgent)
		csp, contentType := "", "application/x-maven-pom+xml"
		if browse {
			csp, contentType = "sandbox", "application/xml"
		}
		if h := browser.Header(); browser.Code != http.StatusOK ||
			h.Get("Content-Security-Policy") != csp || h.Get("Content-Type") != contentType {
			t.Errorf(
				"%sbrowser GET: %d, Content-Security-Policy %q, Content-Type %q; want %q, %q",
				label,
				browser.Code,
				h.Get("Content-Security-Policy"),
				h.Get("Content-Type"),
				csp,
				contentType,
			)
		}
		// Every answer came from the upstream's stream: the cache holds
		// nothing.
		cache := w.request(http.MethodGet, "/artifactory/remote-cache/b/a%20b.jar", "reader", "")
		if cache.Code != http.StatusNotFound {
			t.Errorf("%sthe cache answers %d, want 404", label, cache.Code)
		}
	}
}
