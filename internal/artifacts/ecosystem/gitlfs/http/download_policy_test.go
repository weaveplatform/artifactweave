package gitlfshttp_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
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

// policyReply is a batch answer with each object's error message, which
// batchReply leaves out.
type policyReply struct {
	Objects []struct {
		Actions map[string]struct {
			Href string `json:"href"`
		} `json:"actions"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"objects"`
}

// lfsCall sends one request under /artifactory/api/lfs/ as user, or as the
// fixture's administrator when user is empty.
func lfsCall(t *testing.T, server *httptest.Server, method, path, user, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+"/artifactory/api/lfs/"+path,
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(data)
}

func policyBatch(
	t *testing.T,
	server *httptest.Server,
	key, user, operation, oid string,
	size int,
) policyReply {
	t.Helper()
	status, body := lfsCall(t, server, http.MethodPost, key+"/objects/batch", user,
		fmt.Sprintf(`{"operation":%q,"objects":[{"oid":%q,"size":%d}]}`, operation, oid, size))
	var reply policyReply
	if status != http.StatusOK || json.Unmarshal([]byte(body), &reply) != nil ||
		len(reply.Objects) != 1 {
		t.Fatalf("%s batch: status=%d body=%s", operation, status, body)
	}
	return reply
}

// wantObjectError checks a batch object's error. Its message stays the
// batch's own whatever refused the object; the transfer answers why.
func wantObjectError(t *testing.T, reply policyReply, code int) {
	t.Helper()
	got := reply.Objects[0]
	if got.Error == nil || got.Error.Code != code ||
		got.Error.Message != "Git LFS object unavailable" || len(got.Actions) != 0 {
		t.Fatalf("batch object: %+v, want error %d", got, code)
	}
}

func wantAnswer(t *testing.T, status int, body string, want int, message string) {
	t.Helper()
	if status != want || !strings.Contains(body, message) {
		t.Fatalf("answer: status=%d body=%s, want %d %q", status, body, want, message)
	}
}

func patchLFS(t *testing.T, db *inmem.Store, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err := db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

// seedLFS stores body through key's object route as the administrator and
// returns its OID and stored path.
func seedLFS(t *testing.T, server *httptest.Server, key string, body []byte) (string, string) {
	t.Helper()
	sum := sha256.Sum256(body)
	oid := hex.EncodeToString(sum[:])
	status, answer := lfsCall(t, server, http.MethodPut,
		key+"/objects/"+oid+"?size="+strconv.Itoa(len(body)), "", string(body))
	if status != http.StatusOK {
		t.Fatalf("seed: status=%d body=%s", status, answer)
	}
	return oid, "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
}

// A download batch checks Read permission before the download policy
// (DV-057): a caller who may not read gets the object's 403 whatever the
// repository's settings, where the policy once answered 404 first; a reader
// gets 404. An upload batch looks the stored object up without the policy,
// so it hands out no upload action for it, and a PUT of the stored object
// that the patterns reject answers success, as before the policy reached
// the lookups.
func TestUnitBatchChecksReadPermissionBeforeThePolicy(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := []byte("policy-checked Git LFS bytes\n")
	oid, _ := seedLFS(t, f.server, "lfs", body)
	blackedOut, excluded := true, "objects/**"
	for _, tc := range []struct {
		name   string
		change func(*repo.Config)
	}{
		{"blacked out", func(c *repo.Config) { c.BlackedOut = &blackedOut }},
		{"excluded", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = nil, &excluded }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patchLFS(t, f.db, "lfs", tc.change)
			reader := policyBatch(t, f.server, "lfs", "reader", "download", oid, len(body))
			wantObjectError(t, reader, http.StatusNotFound)
			intruder := policyBatch(t, f.server, "lfs", "intruder", "download", oid, len(body))
			wantObjectError(t, intruder, http.StatusForbidden)
			upload := policyBatch(t, f.server, "lfs", "", "upload", oid, len(body))
			if got := upload.Objects[0]; got.Error != nil || len(got.Actions) != 0 {
				t.Fatalf("upload batch for a stored object: %+v", got)
			}
		})
	}
	status, answer := lfsCall(t, f.server, http.MethodPut,
		"lfs/objects/"+oid+"?size="+strconv.Itoa(len(body)), "", string(body))
	if status != http.StatusOK {
		t.Fatalf("PUT of the stored excluded object: status=%d body=%s", status, answer)
	}
}

// A virtual repository's own patterns refuse an object once a member the
// caller may read is found (DV-057): a reader's transfer answers 404 "No
// repository found to serve the request", Artifactory's answer for a
// virtual repository, where the object was served (main) or answered "Git
// LFS object unavailable" (#1250); a caller who may read no member gets 403.
func TestUnitVirtualPatternsRefuseAfterReadPermission(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	body := []byte("virtual policy Git LFS bytes\n")
	oid, object := seedLFS(t, f.server, "lfs", body)
	transfer := "lfs/objects/" + oid + "?size=" + strconv.Itoa(len(body))
	status, answer := lfsCall(t, f.server, http.MethodGet, transfer, "reader", "")
	wantAnswer(t, status, answer, http.StatusOK, string(body))
	excluded := "objects/**"
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	refused := "No repository found to serve the request for lfs:" + object
	for _, user := range []string{"", "reader"} {
		status, answer = lfsCall(t, f.server, http.MethodGet, transfer, user, "")
		wantAnswer(t, status, answer, http.StatusNotFound, refused)
		status, answer = lfsCall(t, f.server, http.MethodHead, transfer, user, "")
		wantAnswer(t, status, answer, http.StatusNotFound, "")
	}
	status, answer = lfsCall(t, f.server, http.MethodGet, transfer, "intruder", "")
	wantAnswer(t, status, answer, http.StatusForbidden, "Git LFS read denied")
	reader := policyBatch(t, f.server, "lfs", "reader", "download", oid, len(body))
	wantObjectError(t, reader, http.StatusNotFound)
	intruder := policyBatch(t, f.server, "lfs", "intruder", "download", oid, len(body))
	wantObjectError(t, intruder, http.StatusForbidden)
	upload := policyBatch(t, f.server, "lfs", "", "upload", oid, len(body))
	if got := upload.Objects[0]; got.Error != nil || len(got.Actions) != 0 {
		t.Fatalf("upload batch for a stored object: %+v", got)
	}
}

// A remote repository's batch checks Read permission before its policy too:
// the source boundary checks the policy, after remoteAvailable checks the
// caller, and the upstream is never asked.
func TestUnitRemoteBatchChecksReadPermissionBeforeThePolicy(t *testing.T) {
	f := newRemoteFixture(t)
	blackedOut := true
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.BlackedOut = &blackedOut })
	admin := policyBatch(t, f.server, "lfs", "", "download", f.oid, len(f.object))
	wantObjectError(t, admin, http.StatusNotFound)
	intruder := policyBatch(t, f.server, "lfs", "intruder", "download", f.oid, len(f.object))
	wantObjectError(t, intruder, http.StatusForbidden)
	if f.batches.Load() != 0 || f.objects.Load() != 0 {
		t.Fatalf("upstream asked: batches=%d objects=%d", f.batches.Load(), f.objects.Load())
	}
}

// A virtual repository's own patterns refuse a remote member's object as
// they refuse a local member's, though the source boundary checks only the
// remote's own policy: the object was served (main and #1250). A
// blacked-out remote member is passed over, and with no other member the
// answer is the virtual repository's own "not found".
func TestUnitVirtualPatternsRefuseARemoteMembersObject(t *testing.T) {
	f := newRemoteFixture(t)
	excluded := "objects/**"
	virtual := *repo.Defaults(&repo.Config{
		Key: "lfs-virtual", Class: repo.Virtual, PackageType: repo.GitLFS,
		Repositories: []string{"lfs"}, ExcludesPattern: &excluded,
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	object := "objects/" + f.oid[:2] + "/" + f.oid[2:4] + "/" + f.oid
	transfer := "lfs-virtual/objects/" + f.oid + "?size=" + strconv.Itoa(len(f.object))
	status, answer := lfsCall(t, f.server, http.MethodGet, transfer, "", "")
	wantAnswer(t, status, answer, http.StatusNotFound,
		"No repository found to serve the request for lfs-virtual:"+object)
	batch := policyBatch(t, f.server, "lfs-virtual", "", "download", f.oid, len(f.object))
	wantObjectError(t, batch, http.StatusNotFound)
	if f.batches.Load() != 0 || f.objects.Load() != 0 {
		t.Fatalf("upstream asked: batches=%d objects=%d", f.batches.Load(), f.objects.Load())
	}
	patchLFS(t, f.db, "lfs-virtual", func(c *repo.Config) { c.ExcludesPattern = nil })
	status, answer = lfsCall(t, f.server, http.MethodGet, transfer, "", "")
	wantAnswer(t, status, answer, http.StatusOK, string(f.object))
	blackedOut := true
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.BlackedOut = &blackedOut })
	status, answer = lfsCall(t, f.server, http.MethodGet, transfer, "", "")
	wantAnswer(t, status, answer, http.StatusNotFound, "Git LFS object unavailable")
}
