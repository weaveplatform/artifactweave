package terraformhttp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func checksumFileModule(t *testing.T, f *remoteFixture, body []byte, record string) {
	t.Helper()
	f.modules("1.0.0")
	f.location("1.0.0", "/archives/1.0.0.zip?archive=zip&checksum="+url.QueryEscape("file:"+f.up.URL+"/SUMS?file=owned"))
	f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/zip"}}})
	f.up.Set("/SUMS", fakeupstream.Resource{Body: []byte(record), Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}})
}

func TestAccRemoteModuleChecksumFilesColdWarmOfflineAndRedirects(t *testing.T) {
	for _, mode := range []string{"gnu", "binary", "portable-marker", "bsd", "standalone", "relative-name", "checksum-redirect", "archive-redirect", "vanity-parent", "vanity-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			body := archiveZip(t, "main.tf")
			digest := sha256.Sum256(body)
			record := fmt.Sprintf("%x  1.0.0.zip\n", digest)
			switch mode {
			case "binary":
				record = fmt.Sprintf("%x *1.0.0.zip\r\n", digest)
			case "portable-marker":
				record = fmt.Sprintf("%x ?1.0.0.zip", digest)
			case "bsd":
				record = fmt.Sprintf("SHA256 (1.0.0.zip) = %x\n", digest)
			case "standalone":
				record = fmt.Sprintf("%x", digest)
			case "relative-name":
				record = fmt.Sprintf("%x  archives/1.0.0.zip\n", digest)
			}
			checksumFileModule(t, f, body, record)
			if mode == "checksum-redirect" {
				f.up.Set("/SUMS", fakeupstream.Resource{Redirect: "/effective/SUMS?redirect=private"})
				f.up.Set("/effective/SUMS", fakeupstream.Resource{Body: []byte(record), Header: http.Header{"Content-Type": {"application/octet-stream"}}})
			}
			if mode == "archive-redirect" {
				f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Redirect: "/actual/different-name.zip"})
				f.up.Set("/actual/different-name.zip", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/zip"}}})
			}
			if strings.HasPrefix(mode, "vanity-") {
				selected := "/archives/1.0.0.zip"
				initial := "/vanity/start"
				if mode == "vanity-parent" {
					initial += "?checksum=" + url.QueryEscape("file:"+f.up.URL+"/SUMS?file=owned")
				} else {
					selected += "?checksum=" + url.QueryEscape("file:"+f.up.URL+"/SUMS?file=owned")
				}
				f.location("1.0.0", initial)
				f.up.Set("/vanity/start", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {selected}}})
			}
			public := f.signed("1.0.0", "admin")
			response := f.do("GET", public, "")
			expect(t, response, 200)
			if !bytes.Equal(response.Body.Bytes(), body) {
				t.Fatal("checksum-file delivery changed original bytes")
			}
			if f.up.Count("GET", "/SUMS") != 1 {
				t.Fatal("one resolution acquired its checksum file more than once")
			}
			for _, request := range f.up.Requests() {
				if request.Path == "/SUMS" && (request.Query.Has("terraform-get") || request.Query.Has("archive") || request.Query.Has("checksum")) {
					t.Fatal("checksum read inherited getter controls")
				}
				if request.Path == "/archives/1.0.0.zip" && (request.Query.Has("checksum") || request.Query.Has("file")) {
					t.Fatal("archive read inherited checksum source query")
				}
			}
			f.up.Close()
			calls := len(f.up.Requests())
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			c.Offline = new(true)
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			public = f.signed("1.0.0", "admin")
			expect(t, f.do("GET", public, ""), 200)
			if len(f.up.Requests()) != calls {
				t.Fatal("offline checksum decision contacted upstream")
			}
		})
	}
}

func TestAccModuleChecksumMismatchAndInvalidFileDoNotPublishAndRetry(t *testing.T) {
	for _, mode := range []string{"mismatch", "duplicate", "missing-digest", "malformed-other", "html", "truncated-file", "transient-mismatch", "mismatch-after-policy-edit"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			body := archiveZip(t, "main.tf")
			digest := sha256.Sum256(body)
			valid := fmt.Sprintf("%x  1.0.0.zip\n", digest)
			checksumFileModule(t, f, body, valid)
			resource := fakeupstream.Resource{Header: http.Header{"Content-Type": {"text/plain"}}}
			switch mode {
			case "mismatch", "transient-mismatch", "mismatch-after-policy-edit":
				resource.Body = []byte(strings.Repeat("a", 64) + "  1.0.0.zip\n")
			case "duplicate":
				resource.Body = []byte(valid + valid)
			case "missing-digest":
				resource.Body = []byte("  1.0.0.zip\n")
			case "malformed-other":
				resource.Body = []byte("malformed other record\n" + valid)
			case "html":
				resource.Body = []byte(valid)
				resource.Header.Set("Content-Type", "text/html")
			case "truncated-file":
				resource.Body = []byte(valid)
				resource.Fault = fakeupstream.Fault{TruncateAfter: 4}
			}
			if mode == "transient-mismatch" {
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				c.StoreArtifactsLocally = new(false)
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			f.up.Set("/SUMS", resource)
			if mode == "mismatch-after-policy-edit" {
				expect(t, f.do("GET", remoteModuleBase+"versions", "admin"), 200)
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				c.RetrievalCachePeriodSecs = new(10)
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 502)
			if _, err := f.db.Items().Get(t.Context(), "remote", true, "archives/1.0.0.zip"); err == nil {
				t.Fatal("invalid checksum decision published archive bytes")
			}
			if mode != "mismatch" && mode != "transient-mismatch" && mode != "mismatch-after-policy-edit" && f.up.Count("", "/archives/1.0.0.zip") != 0 {
				t.Fatal("invalid checksum file permitted archive acquisition")
			}
			f.up.Set("/SUMS", fakeupstream.Resource{Body: []byte(valid), Header: http.Header{"Content-Type": {"text/plain"}}})
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 204)
		})
	}
}

func TestAccRefreshedModuleChecksumFencesOldSignedArchiveAndPreservesValidCopy(t *testing.T) {
	f := newRemoteFixture(t)
	original := archiveZip(t, "original.tf")
	replacement := archiveZip(t, "replacement.tf")
	a, b := sha256.Sum256(original), sha256.Sum256(replacement)
	checksumFileModule(t, f, original, fmt.Sprintf("%x  1.0.0.zip\n", a))
	old := f.signed("1.0.0", "admin")
	f.up.Set("/SUMS", fakeupstream.Resource{Body: []byte(fmt.Sprintf("%x  1.0.0.zip\n", b)), Header: http.Header{"Content-Type": {"text/plain"}}})
	f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Body: replacement, Header: http.Header{"Content-Type": {"application/zip"}}})
	f.clock.Advance(6 * time.Second)
	expect(t, f.do("GET", old, ""), 403)
	stored, err := f.db.Items().Get(t.Context(), "remote", true, "archives/1.0.0.zip")
	if err != nil || stored.Actual.SHA256 != fmt.Sprintf("%x", a) {
		t.Fatal("rejected old capability changed valid cached bytes", err)
	}
	fresh := f.signed("1.0.0", "admin")
	response := f.do("GET", fresh, "")
	expect(t, response, 200)
	if !bytes.Equal(response.Body.Bytes(), replacement) {
		t.Fatal("fresh checksum did not select new archive identity")
	}
}
