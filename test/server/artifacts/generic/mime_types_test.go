package generic_test

// This file holds the media type suites of #1315 and #1316: the type a
// stored file downloads with, from 7.x's default MIME table, on every route
// that answers it; and the upstream labels a remote repository caches a
// file under, which 7.x refuses only from its block list.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	javaArchive  = "application/java-archive"
	debPackage   = "application/x-debian-package"
	checksumType = "application/x-checksum"
	xzType       = "application/x-xz"
	plain        = "text/plain"
	cType        = "text/x-c"
	groovy       = "text/x-groovy-source"
	python       = "text/x-python"
	swift        = "text/x-swift"
	octetStream  = "application/octet-stream"
)

// tableTypes is every name whose type 7.x's default mimetypes.xml (version
// 17) gives differently from the 14-row table before #1315, with the 7.x
// type. A name is typed by its last extension, so file.tar.xz takes the xz
// row.
var tableTypes = map[string]string{
	"file.ear": javaArchive, "file.har": javaArchive, "file.hpi": javaArchive,
	"file.jpi": javaArchive, "file.sar": javaArchive,
	"file.info":    "application/json+info",
	"file.apk":     "application/vnd.android.package-archive",
	"file.7z":      "application/x-7z-compressed",
	"file.bz2":     "application/x-bzip2",
	"file.tar.bz2": "application/x-bzip2",
	"file.md5":     checksumType, "file.sha1": checksumType, "file.sha256": checksumType,
	"file.conda": "application/x-conda",
	"file.deb":   debPackage, "file.ddeb": debPackage, "file.DEB": debPackage,
	"file.ivy":     "application/x-ivy+xml",
	"file.jardiff": "application/x-java-archive-diff",
	"file.jnlp":    "application/x-java-jnlp-file",
	"file.nupkg":   "application/x-nupkg",
	"file.nuspec":  "application/x-nuspec+xml",
	"file.rar":     "application/x-rar-compressed",
	"file.rpm":     "application/x-rpm",
	"file.rz":      "application/x-ruby-marshal",
	"file.gem":     "application/x-rubygems",
	"file.xz":      xzType, "file.tar.xz": xzType, "file.nar.xz": xzType,
	"file.xhtml": "application/xhtml+xml",
	"file.xsi":   "application/xml",
	"file.dtd":   "application/xml-dtd",
	"file.ent":   "application/xml-external-parsed-entity",
	"file.xsd":   "application/xml-schema",
	"file.css":   "text/css",
	"file.htm":   "text/html",
	"file.asc":   plain, "file.log": plain, "file.md": plain, "README.md": plain,
	"file.mf": plain, "file.properties": plain, "file.tf": plain,
	"file.yaml": plain, "file.yml": plain, "file.YAML": plain, "index.yaml": plain,
	"file.mod": "text/plain+mod",
	"file.c":   cType, "file.cc": cType, "file.cpp": cType, "file.h": cType,
	"file.cs":     "text/x-csharp.sh",
	"file.gradle": groovy, "file.groovy": groovy,
	"file.java": "text/x-java-source",
	"file.fx":   "text/x-javafx-source",
	"file.py":   python, "conanfile.py": python,
	"file.rb":    "text/x-ruby-source",
	"file.scala": "text/x-scala-source",
	"file.sh":    "text/x-script.sh",
	"file.swift": swift, "Package.swift": swift,
	"file.xsl":  "text/xsl",
	"file.xslt": "text/xslt",
}

// keptTypes is names whose type stays as it was: rows the old table had
// with the 7.x type, two-part names typed by their last extension, and
// names with no row.
var keptTypes = map[string]string{
	"file.jar": javaArchive, "file.tar.gz": "application/x-gzip",
	"file.jar.pack.gz": "application/x-gzip", "file.json": "application/json",
	"file.pom": "application/x-maven-pom+xml", "file.box": "application/x-vagrant-box",
	"file.bin": octetStream, "LICENSE": octetStream, "file.yaml.bak": octetStream,
}

func allTypes() map[string]string {
	names := maps.Clone(tableTypes)
	maps.Copy(names, keptTypes)
	return names
}

// mimeTypeOf is the mimeType of a JSON answer.
func mimeTypeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var info struct {
		MimeType string `json:"mimeType"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("%v: %q", err, rec.Body)
	}
	return info.MimeType
}

// wantDownload holds a download to its type and body, with no body on a
// HEAD.
func wantDownload(t *testing.T, rec *httptest.ResponseRecorder, method, typ, body string) {
	t.Helper()
	harness.WantStatus(t, rec, http.StatusOK)
	if method == http.MethodHead {
		body = ""
	}
	if got := rec.Header().Get("Content-Type"); got != typ || rec.Body.String() != body {
		t.Fatalf("%s: Content-Type %q body %q, want %q %q", method, got, rec.Body, typ, body)
	}
}

// TestAccGenericMimeTypeTable: a stored file downloads with its 7.x type
// on the item route, GET and HEAD, the deploy and storage info answers
// name it, a redirected download signs it into the URL, and an archive
// entry of the same name downloads with it too.
func TestAccGenericMimeTypeTable(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	names := allTypes()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for n := range names {
		f, err := zw.Create("e/" + n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte("entry " + n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	rec := w.Do(http.MethodPut, "/artifactory/libs-local/z/archive.zip", "admin", zb.String())
	harness.WantStatus(t, rec, http.StatusCreated)
	for n, want := range names {
		t.Run(n, func(t *testing.T) {
			body := "content of " + n
			for _, key := range []string{"libs-local", "redirect-local"} {
				rec := w.Do(http.MethodPut, "/artifactory/"+key+"/p/"+n, "admin", body)
				harness.WantStatus(t, rec, http.StatusCreated)
				if got := mimeTypeOf(t, rec); got != want {
					t.Fatalf("deploy to %s: mimeType %q, want %q", key, got, want)
				}
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := w.Do(method, "/artifactory/libs-local/p/"+n, "admin", "")
				wantDownload(t, rec, method, want, body)
				rec = w.Do(method, "/artifactory/libs-local/z/archive.zip!/e/"+n, "admin", "")
				wantDownload(t, rec, method, want, "entry "+n)
			}
			rec := w.Do(http.MethodGet, "/artifactory/api/storage/libs-local/p/"+n, "admin", "")
			harness.WantStatus(t, rec, http.StatusOK)
			if got := mimeTypeOf(t, rec); got != want {
				t.Fatalf("storage mimeType %q, want %q", got, want)
			}
			rec = w.Do(http.MethodGet, "/artifactory/redirect-local/p/"+n, "admin", "")
			harness.WantStatus(t, rec, http.StatusFound)
			loc, err := url.Parse(rec.Header().Get("Location"))
			if err != nil || loc.Query().Get("type") != want {
				t.Fatalf("redirect %q, want type %q (%v)", rec.Header().Get("Location"), want, err)
			}
		})
	}
}

// TestAccGenericMimeTypeTrash: the trash listing names a deleted file's
// 7.x type.
func TestAccGenericMimeTypeTrash(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	for n, want := range map[string]string{
		"file.deb": debPackage, "file.yaml": plain, "file.bin": octetStream,
	} {
		p := "/artifactory/libs-local/p/" + n
		harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", "x"), http.StatusCreated)
		harness.WantStatus(t, w.Do(http.MethodDelete, p, "admin", ""), http.StatusNoContent)
		trash := "/artifactory/api/storage/auto-trashcan/libs-local/p/" + n
		rec := w.Do(http.MethodGet, trash, "admin", "")
		harness.WantStatus(t, rec, http.StatusOK)
		if got := mimeTypeOf(t, rec); got != want {
			t.Fatalf("%s: trash mimeType %q, want %q", n, got, want)
		}
	}
}

// upstreamLabels is labels the usual upstreams send for a name, beside its
// own type and the generic binary labels every name is tried under:
// Apache's and nginx's mime.types, the IANA registrations and S3.
var upstreamLabels = map[string][]string{
	"file.jar":    {"application/x-java-archive", "application/zip"},
	"file.ear":    {"application/x-java-archive"},
	"file.tar.gz": {"application/gzip", "application/x-compressed-tar"},
	"file.tar.xz": {"application/x-xz-compressed-tar"},
	"file.bz2":    {"application/x-bzip"},
	"file.deb":    {"application/vnd.debian.binary-package", "application/x-deb"},
	"file.rpm":    {"application/x-redhat-package-manager"},
	"file.gem":    {"application/x-tar"},
	"file.nupkg":  {"application/zip"},
	"file.conda":  {"application/zip"},
	"file.apk":    {"application/zip"},
	"file.yaml": {
		"text/yaml", "application/x-yaml", "application/yaml", "text/plain; charset=utf-8",
	},
	"file.yml":   {"text/yaml", "application/x-yaml"},
	"index.yaml": {"text/x-yaml", "application/x-yaml"},
	"file.json":  {"text/plain; charset=utf-8", "application/vnd.npm.install-v1+json"},
	"file.xml":   {"text/xml"},
	"file.pom":   {"text/xml", "application/xml"},
	"file.md":    {"text/markdown"},
	"file.py":    {"text/x-script.python"},
	"file.box":   {"application/x-tar", "application/gzip"},
	"file.swift": {"text/plain"},
	"file.sh":    {"application/x-sh"},
	"file.css":   {"text/css; charset=utf-8"},
}

// upstream serves body at path under label, or with no Content-Type when
// label is empty.
func upstream(w *remoteWorld, path, label, body string) {
	h := http.Header{"Content-Type": {label}}
	if label == "" {
		h["Content-Type"] = nil
	}
	w.up.Set(path, fakeupstream.Resource{Body: []byte(body), Header: h})
}

// TestAccGenericRemoteMIMELabels: a remote caches a file under its own
// type, the generic binary labels, no label and the labels its usual
// upstreams send, and serves it with its 7.x type.
func TestAccGenericRemoteMIMELabels(t *testing.T) {
	w := newRemoteWorld(t)
	for n, want := range allTypes() {
		if want == checksumType {
			// A remote answers a checksum path from the checksums of the
			// file it names, not from a cached file.
			continue
		}
		labels := []string{want, octetStream, "binary/octet-stream", ""}
		for i, label := range append(labels, upstreamLabels[n]...) {
			up := "/labels/" + string(rune('a'+i)) + "/" + n
			body := "upstream bytes of " + n
			upstream(w, up, label, body)
			rec := w.request(http.MethodGet, "/artifactory/remote"+up, "reader", "")
			wantDownload(t, rec, http.MethodGet, want, body)
			cached := "/artifactory/api/storage/remote-cache" + up
			rec = w.request(http.MethodGet, cached, "reader", "")
			remoteStatus(t, rec, http.StatusOK)
			if got := mimeTypeOf(t, rec); got != want {
				t.Fatalf("%s under %q: cached mimeType %q, want %q", n, label, got, want)
			}
		}
	}
}

// TestAccGenericRemoteMIMEBlocked: an HTML or XHTML label on a file whose
// type is another is refused, 409 with 7.x's message (#1376), and nothing
// is cached for it. The override list replaces that block list (#1316): a
// label it names is refused, and an HTML label is then cached. An HTML
// body is not read for its type, so under a label off the list it is
// cached like any other bytes.
func TestAccGenericRemoteMIMEBlocked(t *testing.T) {
	w := newRemoteWorld(t)
	const page = "<!DOCTYPE html><html>login</html>"
	wantRefused := func(path, label string) {
		t.Helper()
		upstream(w, path, label, page)
		rec := w.request(http.MethodGet, "/artifactory/remote"+path, "reader", "")
		typ := strings.TrimSpace(strings.Split(label, ";")[0])
		message := refusalOf(w, path, item.MimeType(path), typ, path[1:])
		wantRefusal(t, rec, http.MethodGet, message)
		rec = w.request(http.MethodGet, "/artifactory/api/storage/remote-cache"+path, "reader", "")
		remoteStatus(t, rec, http.StatusNotFound)
	}
	wantCached := func(path, label, typ string) {
		t.Helper()
		upstream(w, path, label, page)
		rec := w.request(http.MethodGet, "/artifactory/remote"+path, "reader", "")
		wantDownload(t, rec, http.MethodGet, typ, page)
	}
	wantRefused("/blocked/file.jar", "text/html; charset=utf-8")
	wantRefused("/blocked/file.yaml", "text/html")
	wantRefused("/blocked/file.deb", "application/xhtml+xml")
	wantCached("/unread/file.jar", octetStream, javaArchive)
	wantCached("/unread/file.deb", "application/x-other", debPackage)
	wantCached("/unread/file.bin", "text/html", octetStream)

	w.c.MismatchingMimeTypesOverrideList = "application/x-other"
	w.update()
	wantRefused("/override/file.jar", "application/x-other")
	wantCached("/override/file.yaml", "text/html", plain)
}

// TestAccGenericRemoteMetadataOverrideList: raw metadata a format adapter
// expects as JSON, here an npm package document, is refused under a label
// the override list names, which the list let through before #1316, and
// served under it while the list names another type. The refusal is the
// adapter's own check of the metadata it expects, not 7.x's block list,
// which checks the type of the URL asked for and so passes a document with
// no extension, and keeps its 502 where a file without a format hint
// answers 7.x's 409 (#1392).
func TestAccGenericRemoteMetadataOverrideList(t *testing.T) {
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{
		Key: w.c.Key, Class: repo.Remote, PackageType: repo.NPM, URL: w.up.URL,
	})
	w.update()
	sum := strings.Repeat("a", 40)
	doc := func(name, base string) string {
		return `{"dist-tags":{"latest":"1.0.0"},"name":"` + name + `","versions":{"1.0.0":` +
			`{"dist":{"shasum":"` + sum + `","tarball":"` + base + "/" + name + "/-/" + name +
			`-1.0.0.tgz"},"name":"` + name + `","version":"1.0.0"}}}`
	}
	served := doc("unlisted", "http://example.com/artifactory/api/npm/remote") + "\n"
	for _, tc := range []struct {
		pkg, override, body string
		status              int
	}{
		{"listed", "text/plain", `{"error":"Bad Gateway"}` + "\n", http.StatusBadGateway},
		{"unlisted", "application/x-other", served, http.StatusOK},
	} {
		w.c.MismatchingMimeTypesOverrideList = tc.override
		w.update()
		upstream(w, "/"+tc.pkg, "text/plain; charset=utf-8", doc(tc.pkg, w.up.URL))
		rec := w.request(http.MethodGet, "/artifactory/api/npm/remote/"+tc.pkg, "reader", "")
		remoteStatus(t, rec, tc.status)
		if rec.Body.String() != tc.body {
			t.Fatalf("%s: %q", tc.pkg, rec.Body)
		}
	}
}
