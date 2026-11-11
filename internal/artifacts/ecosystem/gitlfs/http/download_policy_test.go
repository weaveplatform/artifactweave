package gitlfshttp_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A direct object download answers the download policy's refusal with its
// 404 and message, for an administrator too: from a blacked-out repository,
// and of an object the patterns exclude. The bare refusal once reached the
// kit's Error, which answered it as a fault, 500.
func TestUnitObjectDownloadPolicy(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := []byte("refused Git LFS object bytes\n")
	sum := sha256.Sum256(body)
	oid := hex.EncodeToString(sum[:])
	status, upload := f.batch("upload", oid, int64(len(body)))
	if status != http.StatusOK || len(upload.Objects) != 1 {
		t.Fatalf("batch upload: status=%d response=%+v", status, upload)
	}
	put, err := http.NewRequest(http.MethodPut, upload.Objects[0].Actions["upload"].Href,
		bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.server.Client().Do(put)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d", res.StatusCode)
	}
	object := "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
	get := func(want int, message string) {
		t.Helper()
		res, err := f.server.Client().Get(f.server.URL + "/artifactory/api/lfs/lfs/objects/" + oid)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != want || !strings.Contains(string(got), message) {
			t.Fatalf("download: status=%d body=%s, want %d %q", res.StatusCode, got, want, message)
		}
	}
	patch := func(change func(*repo.Config)) {
		t.Helper()
		c, err := f.db.Repos().Get(t.Context(), "lfs")
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	get(http.StatusOK, string(body))
	blackedOut, excluded := true, object
	patch(func(c *repo.Config) { c.BlackedOut = &blackedOut })
	get(http.StatusNotFound,
		"The repository 'lfs' is blacked out and cannot serve artifact 'lfs:"+object+"'.")
	blackedOut = false
	patch(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &blackedOut, &excluded })
	get(http.StatusNotFound,
		"The repository 'lfs' rejected the artifact 'lfs:"+object+"' due to its include/exclude")
}
