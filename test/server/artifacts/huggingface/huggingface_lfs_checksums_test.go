package huggingface_test

import (
	"crypto/md5"  //nolint:gosec // the checksum header 7.x sends
	"crypto/sha1" //nolint:gosec // the checksum header 7.x sends
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestAccHuggingFaceLFSFileKeepsItsUploadChecksums (#1581): a Git LFS file
// a commit stores keeps the SHA-1 and MD5 its upload computed, so its item
// download sends all three checksums and its SHA-1 as the ETag and its
// storage answer lists all three, as 7.x keeps them for every stored file
// (RequestResponseHelper.setBasicHeaders, 7.161.26), and a later revision
// that inherits the file keeps them. The commit stored the SHA-256 alone,
// and the download sent X-Checksum-Sha1 and X-Checksum-Md5 empty.
func TestAccHuggingFaceLFSFileKeepsItsUploadChecksums(t *testing.T) {
	w := newResolveWorld(t)
	small, large := resolveFiles()
	s1, s256, m5 := sha1.Sum(
		large.body,
	), sha256.Sum256(
		large.body,
	), md5.Sum(
		large.body,
	) //nolint:gosec // checksum headers
	want := map[string]string{
		"sha1": hex.EncodeToString(s1[:]), "sha256": hex.EncodeToString(s256[:]),
		"md5": hex.EncodeToString(m5[:]),
	}
	first := publishResolveFiles(t, w, "hf", small, large)
	next := w.Request(
		http.MethodPost,
		"/artifactory/api/huggingfaceml/hf/api/models/acme/demo/commit/main",
		"admin",
		`{"key":"header","value":{"summary":"readme"}}`+"\n"+
			fmt.Sprintf(
				`{"key":"file","value":{"path":"README.md","content":%q,"encoding":"base64"}}`,
				base64.StdEncoding.EncodeToString([]byte("# demo\n")),
			)+"\n",
		"Content-Type",
		"application/x-ndjson",
	)
	wantStatus(t, next, http.StatusOK)
	var result struct {
		OID string `json:"commitOid"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &result); err != nil || result.OID == first {
		t.Fatalf("second commit %s: %v", next.Body, err)
	}
	for _, revision := range []string{first, result.OID} {
		path := "hf/models--acme--demo/snapshots/" + revision + "/" + large.name
		rec := w.Request(http.MethodGet, "/artifactory/"+path, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		for k, v := range map[string]string{
			"X-Checksum-Sha1": want["sha1"], "X-Checksum-Sha256": want["sha256"],
			"X-Checksum-Md5": want["md5"], "ETag": want["sha1"],
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s %q, want %q", path, k, got, v)
			}
		}
		info := w.Request(http.MethodGet, "/artifactory/api/storage/"+path, "admin", "")
		wantStatus(t, info, http.StatusOK)
		var doc struct {
			Checksums map[string]string `json:"checksums"`
		}
		if err := json.Unmarshal(info.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		for k, v := range want {
			if doc.Checksums[k] != v {
				t.Errorf("%s: storage checksum %s %q, want %q", path, k, doc.Checksums[k], v)
			}
		}
	}
}
