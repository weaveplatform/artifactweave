package server

import (
	"context"
	"crypto/sha1" //nolint:gosec // the protocol's own checksum
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestMultipartUploadProtocol drives the JFrog CLI's multipart upload as
// jfrog-client-go does: create with the caller's credentials, a URL per part
// with the upload token, the parts PUT without credentials and out of order,
// complete, poll the status, and the checksum deploy that stores the item.
func TestMultipartUploadProtocol(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/v1/uploads/config",
		admin,
		"",
	); !strings.Contains(
		rec.Body.String(),
		`"supported":true`,
	) {
		t.Fatalf("config %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodPost,
		uploadsCreate+"?repoKey=libs-local&repoPath=big%2Fdata.bin&partSizeMB=1", admin, "")
	var created struct{ Token string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &created) != nil ||
		created.Token == "" {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	token := map[string]string{"Authorization": "Bearer " + created.Token}

	data := strings.Repeat("0123456789abcdef", 1<<16) + "tail" // 1 MiB + 4 bytes
	parts := []string{data[:1<<20], data[1<<20:]}
	for _, n := range []int{2, 1} {
		rec := do(
			t,
			h,
			http.MethodPost,
			uploadsURLPart+"?partNumber="+string(rune('0'+n)),
			token,
			"",
		)
		var u struct{ URL string }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &u) != nil {
			t.Fatalf("urlPart %d %s", rec.Code, rec.Body)
		}
		parsed, err := url.Parse(u.URL)
		if err != nil || parsed.Scheme == "" {
			t.Fatalf("an absolute part URL: %q %v", u.URL, err)
		}
		if rec := do(
			t,
			h,
			http.MethodPut,
			parsed.RequestURI(),
			nil,
			parts[n-1],
		); rec.Code != http.StatusOK {
			t.Fatalf("part %d: %d %s", n, rec.Code, rec.Body)
		}
	}
	sum := sha1.Sum([]byte(data)) //nolint:gosec // the protocol's own checksum
	sha := hex.EncodeToString(sum[:])
	if rec := do(
		t,
		h,
		http.MethodPost,
		uploadsComplete+"?sha1="+sha,
		token,
		"",
	); rec.Code != http.StatusAccepted {
		t.Fatalf("complete %d %s", rec.Code, rec.Body)
	}
	var st struct {
		Status        string
		ChecksumToken string
	}
	for deadline := time.Now().Add(10 * time.Second); st.Status != "FINISHED"; {
		rec := do(t, h, http.MethodPost, uploadsStatus, token, "")
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		if st.Status != "FINISHED" && st.Status != "PROCESSING" || time.Now().After(deadline) {
			t.Fatalf("status %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The CLI finishes with a checksum deploy carrying the checksum token.
	deploy := map[string]string{
		"Authorization":           admin["Authorization"],
		"X-Checksum-Deploy":       "true",
		"X-Checksum-Sha1":         sha,
		"X-Checksum-Deploy-Token": st.ChecksumToken,
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		"/artifactory/libs-local/big/data.bin;team=core",
		deploy,
		"",
	); rec.Code != http.StatusCreated {
		t.Fatalf("checksum deploy %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/libs-local/big/data.bin",
		admin,
		"",
	); rec.Code != http.StatusOK ||
		rec.Body.String() != data {
		t.Fatalf("download %d, %d bytes", rec.Code, rec.Body.Len())
	}
	// Aborting after the deploy discards only the session.
	if rec := do(t, h, http.MethodPost, uploadsAbort, token, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("abort %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		uploadsStatus,
		token,
		"",
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an aborted upload's token: %d", rec.Code)
	}
}

func TestMultipartErrors(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	rec := do(t, h, http.MethodPost,
		uploadsCreate+"?repoKey=libs-local&repoPath=f.bin&partSizeMB=1", admin, "")
	var created struct{ Token string }
	if json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	token := map[string]string{"Authorization": "Bearer " + created.Token}
	for name, tc := range map[string]struct {
		method, path string
		want         int
	}{
		"part zero":        {http.MethodPost, uploadsURLPart + "?partNumber=0", http.StatusBadRequest},
		"malformed sha1":   {http.MethodPost, uploadsComplete + "?sha1=nope", http.StatusBadRequest},
		"nothing uploaded": {http.MethodPost, uploadsComplete + "?sha1=" + strings.Repeat("a", 40), http.StatusAccepted},
	} {
		if rec := do(t, h, tc.method, tc.path, token, ""); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Completion has started: no more part URLs.
	if rec := do(
		t,
		h,
		http.MethodPost,
		uploadsURLPart+"?partNumber=1",
		token,
		"",
	); rec.Code != http.StatusConflict {
		t.Fatalf("a part URL after completion: %d %s", rec.Code, rec.Body)
	}
	// Without a multipart service the routes answer 501 and config false.
	bare := statusFixture(t, false, func(c *Config) { c.Multipart = nil })
	if rec := do(
		t,
		bare,
		http.MethodGet,
		"/artifactory/api/v1/uploads/config",
		admin,
		"",
	); !strings.Contains(
		rec.Body.String(),
		`"supported":false`,
	) {
		t.Fatalf("config without a service: %s", rec.Body)
	}
	for _, p := range []struct{ method, path string }{
		{http.MethodPost, uploadsCreate},
		{http.MethodPost, uploadsURLPart},
		{http.MethodPut, uploadsPart},
		{http.MethodPost, uploadsComplete},
		{http.MethodPost, uploadsStatus},
		{http.MethodPost, uploadsAbort},
	} {
		if rec := do(t, bare, p.method, p.path, admin, ""); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s without a service: %d", p.method, p.path, rec.Code)
		}
	}
}

// uploadInParts runs the CLI's multipart upload of data, in one part, to
// path in libs-local as the user as, and returns the checksum token the
// finished upload's status hands out.
func uploadInParts(t *testing.T, w *itemWorld, as, path, data string) string {
	t.Helper()
	rec := w.do(http.MethodPost,
		uploadsCreate+"?repoKey=libs-local&repoPath="+url.QueryEscape(path)+"&partSizeMB=1", as, "")
	var created struct{ Token string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	bearer := map[string]string{"Authorization": "Bearer " + created.Token}
	rec = do(t, w.h, http.MethodPost, uploadsURLPart+"?partNumber=1", bearer, "")
	var u struct{ URL string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &u) != nil {
		t.Fatalf("urlPart %d %s", rec.Code, rec.Body)
	}
	part, err := url.Parse(u.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, w.h, http.MethodPut, part.RequestURI(), nil, data); rec.Code != http.StatusOK {
		t.Fatalf("part %d %s", rec.Code, rec.Body)
	}
	rec = do(t, w.h, http.MethodPost, uploadsComplete+"?sha1="+sha1Hex(data), bearer, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("complete %d %s", rec.Code, rec.Body)
	}
	var st struct{ Status, ChecksumToken string }
	for deadline := time.Now().Add(10 * time.Second); st.Status != "FINISHED"; {
		rec := do(t, w.h, http.MethodPost, uploadsStatus, bearer, "")
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		if st.Status != "FINISHED" && st.Status != "PROCESSING" || time.Now().After(deadline) {
			t.Fatalf("status %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return st.ChecksumToken
}

// TestMultipartChecksumToken (#379): the checksum deploy that ends a
// multipart upload stores content no file holds yet, so there is nothing
// the caller could read. The checksum token from the status poll lets it
// through instead, for the uploader and that content only.
func TestMultipartChecksumToken(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Multipart = &multipart.Service{Store: c.Store, Blobs: c.Blobs, Dir: t.TempDir()}
	})
	// user may deploy to libs-local, but read nothing.
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "user-deploys",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionWrite}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/other-local/secret.bin", "admin", "secret"),
		http.StatusCreated,
	)
	data := strings.Repeat("uploaded in parts ", 1000)
	token := uploadInParts(t, w, "deployer", "big/data.bin", data)
	deploy := func(as, content string, token string) *httptest.ResponseRecorder {
		return w.do(http.MethodPut, "/artifactory/libs-local/big/data.bin;team=core", as, "",
			"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex(content),
			"X-Checksum-Deploy-Token", token)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"without the token":             deploy("deployer", data, ""),
		"with a forged token":           deploy("deployer", data, token+"0"),
		"another caller with the token": deploy("user", data, token),
		// The token proves that upload's content, not content elsewhere.
		"other content with the token": deploy("deployer", "secret", token),
	} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	wantStatus(t, deploy("deployer", data, token), http.StatusCreated)
	if got := w.do(
		http.MethodGet,
		"/artifactory/libs-local/big/data.bin",
		"deployer",
		"",
	); got.Body.String() != data {
		t.Fatalf("download %d, %d bytes", got.Code, got.Body.Len())
	}
}
