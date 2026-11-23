package access_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccProjectQuotaHTTP(t *testing.T) {
	projectQuotaHTTP(t, inmem.New())
}

func TestAccProjectQuotaSQLHTTP(t *testing.T) {
	projectRepositorySQL(t, projectQuotaHTTP)
}

func projectQuotaHTTP(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	blobs, err := fs.New(t.TempDir())
	storekit.Must(t, err)
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	storekit.Must(t, err)
	h := harness.Router(Config{Store: db, Blobs: blobs, Auth: auth, BaseURL: "https://quota.invalid"})
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		headers := support.Basic("admin", "pw")
		headers["Content-Type"] = "application/json"
		rec := support.Do(t, h, method, path, headers, body)
		wantStatus(t, rec, want)
		return rec
	}
	bytes := func(key string, want int64) {
		t.Helper()
		value, exists, err := db.Projects().Storage(ctx, key)
		if err != nil || !exists || value.Bytes != want {
			t.Fatalf("%s committed logical bytes = %+v, initialized=%v, want %d: %v", key, value, exists, want, err)
		}
	}
	refused := func(rec *httptest.ResponseRecorder, access bool) {
		t.Helper()
		// 7.161.26's ProjectsUtils.PROJ_LIMIT_MSG and SC_REQUEST_TOO_LONG;
		// 7.x's Access answer is not in its open sources, so attach keeps 403.
		status := http.StatusRequestEntityTooLarge
		want := `{"errors":[{"message":"Project storage quota exceeded. If you require more storage, contact your Platform Admin.","status":413}]}`
		if access {
			status = http.StatusForbidden
			want = `{"errors":[{"code":"FORBIDDEN","message":"project storage quota exceeded"}]}`
		}
		if rec.Code != status || strings.TrimSpace(rec.Body.String()) != want || rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("quota refusal exposed private totals/owner or changed the route envelope", rec.Body.String(), rec.Header())
		}
	}
	request(http.MethodPost, "/access/api/v1/projects", `{"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":100}`, http.StatusCreated)
	request(http.MethodPost, "/access/api/v1/projects", `{"project_key":"beta","display_name":"Beta","storage_quota_bytes":10}`, http.StatusCreated)
	request(http.MethodPut, "/artifactory/api/repositories/alpha-generic", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, http.StatusOK)
	request(http.MethodPut, "/artifactory/api/repositories/incoming", `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	const base = "/artifactory/alpha-generic/"
	original := strings.Repeat("a", 70)
	request(http.MethodPut, base+"base.bin", original, http.StatusCreated)
	bytes("alpha", 70)
	start, responses := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
	var writers sync.WaitGroup
	for i := range 2 {
		writers.Go(func() {
			<-start
			responses <- support.Do(t, h, http.MethodPut, fmt.Sprintf("%swriter-%d.bin", base, i), support.Basic("admin", "pw"), strings.Repeat(fmt.Sprint(i), 20))
		})
	}
	close(start)
	writers.Wait()
	close(responses)
	accepted, denied := 0, 0
	winner := ""
	for i := range 2 {
		rec := <-responses
		switch rec.Code {
		case http.StatusCreated:
			accepted++
		case http.StatusRequestEntityTooLarge:
			refused(rec, false)
			denied++
		default:
			t.Fatal("racing upload failed outside quota admission", i, rec.Code, rec.Body.String())
		}
	}
	if accepted != 1 || denied != 1 {
		t.Fatal("parallel HTTP uploads exceeded the hard boundary", accepted, denied)
	}
	bytes("alpha", 90)
	for i := range 2 {
		path := fmt.Sprintf("writer-%d.bin", i)
		if rec := support.Do(t, h, http.MethodGet, base+path, support.Basic("admin", "pw"), ""); rec.Code == http.StatusOK {
			winner = path
		}
	}
	if winner == "" {
		t.Fatal("committed writer was not downloadable")
	}
	refused(request(http.MethodPut, base+"base.bin", strings.Repeat("x", 95), http.StatusRequestEntityTooLarge), false)
	if rec := request(http.MethodGet, base+"base.bin", "", http.StatusOK); rec.Body.String() != original {
		t.Fatal("refused overwrite changed downloadable content")
	}
	digest := sha256.Sum256([]byte(original))
	headers := support.Basic("admin", "pw")
	headers["X-Checksum-Deploy"], headers["X-Checksum-Sha256"] = "true", hex.EncodeToString(digest[:])
	checksum := support.Do(t, h, http.MethodPut, base+"checksum.bin", headers, "")
	refused(checksum, false)
	bytes("alpha", 90)
	request(http.MethodPut, "/artifactory/incoming/unassigned.bin", strings.Repeat("u", 15), http.StatusCreated)
	refused(request(http.MethodPut, "/access/api/v1/projects/_/attach/repositories/incoming/beta", "", http.StatusForbidden), true)
	configuration, err := db.Repos().Get(ctx, "incoming")
	storekit.Must(t, err)
	if configuration.ProjectKey != "" {
		t.Fatal("refused attach changed canonical ownership")
	}
	bytes("beta", 0)
	refused(request(http.MethodPost, "/artifactory/api/repositories/alpha-generic", `{"projectKey":"beta"}`, http.StatusRequestEntityTooLarge), false)
	bytes("alpha", 90)
	bytes("beta", 0)
	request(http.MethodPut, "/access/api/v1/projects/_/share/repositories/alpha-generic/beta?readOnly=true", "", http.StatusNoContent)
	bytes("alpha", 90)
	bytes("beta", 0)
	request(http.MethodPut, "/access/api/v1/projects/alpha", `{"storage_quota_bytes":25}`, http.StatusOK)
	refused(request(http.MethodPut, base+"above-lowered.bin", "z", http.StatusRequestEntityTooLarge), false)
	request(http.MethodPut, base+"base.bin", "shortened!", http.StatusCreated)
	bytes("alpha", 30)
	request(http.MethodDelete, base+winner, "", http.StatusNoContent)
	bytes("alpha", 10)
	request(http.MethodPut, "/access/api/v1/projects/alpha", `{"soft_limit":true}`, http.StatusOK)
	request(http.MethodPut, base+"soft.bin", strings.Repeat("s", 100), http.StatusCreated)
	bytes("alpha", 110)
	request(http.MethodPut, "/access/api/v1/projects/alpha", `{"soft_limit":false,"storage_quota_bytes":-1}`, http.StatusOK)
	request(http.MethodPut, base+"unlimited.bin", strings.Repeat("n", 100), http.StatusCreated)
	bytes("alpha", 210)
}
