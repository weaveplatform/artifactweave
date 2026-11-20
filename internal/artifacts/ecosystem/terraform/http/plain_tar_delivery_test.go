package terraformhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func TestAccPlainTarDeliveryRetainsOriginalAndUsesPortableRepresentation(t *testing.T) {
	for _, key := range []string{"remote", "virtual"} {
		t.Run(key, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			raw := archiveTar(t, "main.tf")
			f.location("1.0.0", "/archives/1.0.0.tar?archive=tar")
			f.up.Set("/archives/1.0.0.tar", fakeupstream.Resource{Body: raw})
			if key == "virtual" {
				virtualMembers(t, f.fixture, "remote")
			}
			metadata := f.do("GET", prefix+"v1/modules/"+key+"__ns/name/aws/1.0.0/download", "admin")
			expect(t, metadata, 204)
			public := metadata.Header().Get("X-Terraform-Get")
			u, err := url.Parse(public)
			if err != nil || u.Query().Get("archive") != "tar.gz" {
				t.Fatal("plain TAR did not advertise a native-supported delivery format")
			}
			result := f.do("GET", public, "")
			expect(t, result, 200)
			gz, err := gzip.NewReader(bytes.NewReader(result.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			delivered := tar.NewReader(gz)
			original := tar.NewReader(bytes.NewReader(raw))
			want, err := original.Next()
			if err != nil {
				t.Fatal(err)
			}
			got, err := delivered.Next()
			if err != nil || got.Name != want.Name || got.Mode != want.Mode {
				t.Fatal("portable delivery changed module path or permissions", err)
			}
			wantBody, err := io.ReadAll(original)
			if err != nil {
				t.Fatal(err)
			}
			gotBody, err := io.ReadAll(delivered)
			if err != nil || !bytes.Equal(wantBody, gotBody) {
				t.Fatal("portable delivery changed module contents", err)
			}
			if _, err := delivered.Next(); err != io.EOF {
				t.Fatal("portable delivery added a module entry", err)
			}
			stored, err := f.db.Items().Get(t.Context(), "remote", true, "archives/1.0.0.tar")
			if err != nil {
				t.Fatal(err)
			}
			opened, _, err := f.items.Blobs.Open(t.Context(), stored.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			cached, readErr := io.ReadAll(opened)
			closeErr := opened.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(cached, raw) {
				t.Fatal("native delivery changed the original cached archive", readErr, closeErr)
			}
			wireHash := sha256.Sum256(result.Body.Bytes())
			if result.Header().Get("X-Checksum-Sha256") != hex.EncodeToString(wireHash[:]) ||
				result.Header().Get("X-Checksum-Sha256") == stored.Actual.SHA256 {
				t.Fatal("public checksum does not describe its derived representation")
			}
			head := f.do("HEAD", public, "")
			expect(t, head, 200)
			if head.Body.Len() != 0 || head.Header().Get("Content-Length") != result.Header().Get("Content-Length") ||
				head.Header().Get("ETag") != result.Header().Get("ETag") {
				t.Fatal("HEAD describes a different representation")
			}
			req := httptest.NewRequest("GET", public, nil)
			req.Header.Set("If-None-Match", result.Header().Get("ETag"))
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			expect(t, rec, http.StatusNotModified)
			req = httptest.NewRequest("GET", public, nil)
			req.Header.Set("Range", "bytes=0-9")
			rec = httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			expect(t, rec, http.StatusPartialContent)
			if !bytes.Equal(rec.Body.Bytes(), result.Body.Bytes()[:10]) {
				t.Fatal("range describes original bytes instead of delivered bytes")
			}
			query := u.Query()
			query.Set("archive", "tar")
			u.RawQuery = query.Encode()
			expect(t, f.do("GET", u.String(), ""), http.StatusForbidden)
		})
	}
}
