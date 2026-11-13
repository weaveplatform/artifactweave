package generic_test

// This file holds the Content-Type suites of #1378, #1379 and #1380: the
// type a generated checksum downloads with, the type of a file copied or
// moved to another name, and the artifactory.content-type property.

import (
	"crypto/md5"  //nolint:gosec // the checksum a client asks for, not a security use
	"crypto/sha1" //nolint:gosec // the checksum a client asks for, not a security use
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// checksumTypes is each generated checksum's GET type, from 7.x's MIME
// table, and its digest. 7.x has no .sha512 row, so it takes the default.
var checksumTypes = []struct {
	ext, get string
	sum      func() hash.Hash
}{
	{"sha1", checksumType, sha1.New},
	{"sha256", checksumType, sha256.New},
	{"md5", checksumType, md5.New},
	{"sha512", octetStream, sha512.New},
}

func digest(h hash.Hash, body string) string {
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}

// TestAccGenericChecksumContentType: a generated checksum downloads with
// its path's type from the MIME table on a GET and application/x-checksum
// on a HEAD, as 7.x's sendBodyResponseForChecksum and ChecksumResource
// type them, from a local repository, a remote and its cache (#1378).
func TestAccGenericChecksumContentType(t *testing.T) {
	const body = "jar bytes"
	local := harness.NewItemWorld(t, false)
	harness.WantStatus(
		t,
		local.Do(http.MethodPut, "/artifactory/server-local/sums/a.jar", "admin", body),
		http.StatusCreated,
	)
	remote := newRemoteWorld(t)
	upstream(remote, "/sums/a.jar", javaArchive, body)
	wantDownload(t, remote.request(http.MethodGet, "/artifactory/remote/sums/a.jar", "reader", ""),
		http.MethodGet, javaArchive, body)
	for _, c := range checksumTypes {
		sum := digest(c.sum(), body)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			typ := c.get
			if method == http.MethodHead {
				typ = checksumType
			}
			wantDownload(
				t,
				local.Do(method, "/artifactory/server-local/sums/a.jar."+c.ext, "admin", ""),
				method,
				typ,
				sum,
			)
			for _, key := range []string{"remote", "remote-cache"} {
				rec := remote.request(
					method,
					"/artifactory/"+key+"/sums/a.jar."+c.ext,
					"reader",
					"",
				)
				wantDownload(t, rec, method, typ, sum)
			}
		}
	}

	// A SHA-256 the remote retrieves for a file it has not cached is typed
	// the same way.
	cold := strings.Repeat("a", 64)
	upstream(remote, "/cold/a.jar.sha256", "text/plain", cold)
	on := true
	remote.c.RetrieveSha256FromServer = &on
	remote.update()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		typ := checksumType
		rec := remote.request(method, "/artifactory/remote/cold/a.jar.sha256", "reader", "")
		wantDownload(t, rec, method, typ, cold)
	}
}

// TestAccGenericCopyMoveContentType: a file copied or moved to another
// name downloads with the type of its new name, and its storage info and
// trash listing name it, as 7.x types a file by its path
// (FileResource.getMimeType); a copy to the same extension keeps the type
// (#1379).
func TestAccGenericCopyMoveContentType(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	for _, tc := range []struct{ op, from, to, want string }{
		{"copy", "a.bin", "a.jar", javaArchive},
		{"move", "a.bin", "a.jar", javaArchive},
		{"copy", "a.yaml", "a.json", "application/json"},
		{"move", "a.yaml", "a.json", "application/json"},
		{"copy", "a.jar", "a.bin", octetStream},
		{"copy", "a.jar", "b.jar", javaArchive},
	} {
		dir := "/" + tc.op + "/" + tc.from + "-" + tc.to
		body := "bytes of " + dir
		harness.WantStatus(
			t,
			w.Do(http.MethodPut, "/artifactory/libs-local"+dir+"/"+tc.from, "admin", body),
			http.StatusCreated,
		)
		to := url.QueryEscape("/other-local" + dir + "/" + tc.to)
		rec := w.Do(
			http.MethodPost,
			"/artifactory/api/"+tc.op+"/libs-local"+dir+"/"+tc.from+"?to="+to,
			"admin",
			"",
		)
		harness.WantStatus(t, rec, http.StatusOK)
		target := "other-local" + dir + "/" + tc.to
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			wantDownload(
				t,
				w.Do(method, "/artifactory/"+target, "admin", ""),
				method,
				tc.want,
				body,
			)
		}
		rec = w.Do(http.MethodGet, "/artifactory/api/storage/"+target, "admin", "")
		harness.WantStatus(t, rec, http.StatusOK)
		if got := mimeTypeOf(t, rec); got != tc.want {
			t.Fatalf(
				"%s %s to %s: storage mimeType %q, want %q",
				tc.op,
				tc.from,
				tc.to,
				got,
				tc.want,
			)
		}
		harness.WantStatus(
			t,
			w.Do(http.MethodDelete, "/artifactory/"+target, "admin", ""),
			http.StatusNoContent,
		)
		rec = w.Do(http.MethodGet, "/artifactory/api/storage/auto-trashcan/"+target, "admin", "")
		harness.WantStatus(t, rec, http.StatusOK)
		if got := mimeTypeOf(t, rec); got != tc.want {
			t.Fatalf("%s %s to %s: trash mimeType %q, want %q", tc.op, tc.from, tc.to, got, tc.want)
		}
	}
}

// TestAccGenericContentTypeProperty: a file whose artifactory.content-type
// property is set downloads with its value, GET and HEAD, from a local
// repository and a remote's cache, as 7.x's DbStoringRepoMixin.setMimeType
// types the resource it answers from (#1380). Storage info and a signed
// redirect URL keep the path's type, as 7.x's StorageInfoHelper and
// BinaryElementFactory take it from the path. A blank value is ignored,
// and removing the property restores the path's type.
func TestAccGenericContentTypeProperty(t *testing.T) {
	const custom = "text/x-custom"
	w := harness.NewItemWorld(t, false)
	for _, key := range []string{"libs-local", "redirect-local"} {
		harness.WantStatus(
			t,
			w.Do(http.MethodPut, "/artifactory/"+key+"/prop/a.jar", "admin", "jar bytes"),
			http.StatusCreated,
		)
		harness.WantStatus(t, w.Do(
			http.MethodPut,
			"/artifactory/api/storage/"+key+"/prop/a.jar?properties=artifactory.content-type="+custom,
			"admin",
			"",
		),
			http.StatusNoContent)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wantDownload(
			t,
			w.Do(method, "/artifactory/libs-local/prop/a.jar", "admin", ""),
			method,
			custom,
			"jar bytes",
		)
	}
	rec := w.Do(http.MethodGet, "/artifactory/api/storage/libs-local/prop/a.jar", "admin", "")
	harness.WantStatus(t, rec, http.StatusOK)
	if got := mimeTypeOf(t, rec); got != javaArchive {
		t.Fatalf("storage mimeType %q, want %q", got, javaArchive)
	}
	rec = w.Do(http.MethodGet, "/artifactory/redirect-local/prop/a.jar", "admin", "")
	harness.WantStatus(t, rec, http.StatusFound)
	if loc, err := url.Parse(
		rec.Header().Get("Location"),
	); err != nil ||
		loc.Query().Get("type") != javaArchive {
		t.Fatalf("redirect %q, want type %q (%v)", rec.Header().Get("Location"), javaArchive, err)
	}

	harness.WantStatus(t, w.Do(
		http.MethodPut,
		"/artifactory/api/storage/libs-local/prop/a.jar?properties=artifactory.content-type=%20",
		"admin",
		"",
	),
		http.StatusNoContent)
	wantDownload(t, w.Do(http.MethodGet, "/artifactory/libs-local/prop/a.jar", "admin", ""),
		http.MethodGet, javaArchive, "jar bytes")
	harness.WantStatus(t, w.Do(
		http.MethodPut,
		"/artifactory/api/storage/libs-local/prop/a.jar?properties=artifactory.content-type="+custom,
		"admin",
		"",
	),
		http.StatusNoContent)
	harness.WantStatus(t, w.Do(
		http.MethodDelete,
		"/artifactory/api/storage/libs-local/prop/a.jar?properties=artifactory.content-type",
		"admin",
		"",
	),
		http.StatusNoContent)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wantDownload(
			t,
			w.Do(method, "/artifactory/libs-local/prop/a.jar", "admin", ""),
			method,
			javaArchive,
			"jar bytes",
		)
	}

	remote := newRemoteWorld(t)
	upstream(remote, "/prop/a.jar", javaArchive, "jar bytes")
	wantDownload(t, remote.request(http.MethodGet, "/artifactory/remote/prop/a.jar", "reader", ""),
		http.MethodGet, javaArchive, "jar bytes")
	remoteStatus(t, remote.request(
		http.MethodPut,
		"/artifactory/api/storage/remote-cache/prop/a.jar?properties=artifactory.content-type="+custom,
		"admin",
		"",
	),
		http.StatusNoContent)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wantDownload(
			t,
			remote.request(method, "/artifactory/remote/prop/a.jar", "reader", ""),
			method,
			custom,
			"jar bytes",
		)
	}
}
