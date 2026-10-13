package server

import (
	"crypto/sha1" //nolint:gosec // the protocol's own checksum
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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
