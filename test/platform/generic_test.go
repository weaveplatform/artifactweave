package platform

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	streamRepo   = "platform-stream"
	redirectRepo = "platform-redirect"
	artifactPath = "scenario/payload.bin"
	// Above the JFrog CLI's split threshold (5,120,000 bytes) and the
	// redirect minimum (1 MiB), and big enough that a storage backend
	// uploads it in parts.
	payloadSize = 40 << 20
)

// payload is the file every generic check deploys and reads back.
type payload struct {
	data                 []byte
	sha256, sha1, md5sum string
}

var file payload

func genericChecks() []check {
	return []check{
		{"repositories", createRepositories},
		{"deploy 40 MiB", deploy},
		{"round trip", roundTrip},
		{"ranged GET", rangedGet},
		{"checksums", checksums},
		{"HEAD answered locally", headLocal},
		{"downloadRedirect lands on the storage host", redirectToStorage},
		{"ranged GET through the redirect", rangedRedirect},
		{"storage refuses the URL without its signature", unsignedRefused},
	}
}

func createRepositories(t *testing.T, h *harness) {
	for _, repo := range []map[string]any{
		{"key": streamRepo, "rclass": "local", "packageType": "generic"},
		{"key": redirectRepo, "rclass": "local", "packageType": "generic", "downloadRedirect": true},
	} {
		body, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := h.do(
			t,
			http.MethodPut,
			"/artifactory/api/repositories/"+repo["key"].(string),
			body,
			http.Header{"Content-Type": {"application/json"}},
		)
		if err != nil {
			t.Fatal(err)
		}
		b := drain(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create %s: %d %s", repo["key"], resp.StatusCode, b)
		}
	}
}

func deploy(t *testing.T, h *harness) {
	file.data = make([]byte, payloadSize)
	if _, err := rand.Read(file.data); err != nil {
		t.Fatal(err)
	}
	s256 := sha256.Sum256(file.data)
	s1 := sha1.Sum(file.data)
	m5 := md5.Sum(file.data)
	file.sha256, file.sha1, file.md5sum = hex.EncodeToString(
		s256[:],
	), hex.EncodeToString(
		s1[:],
	), hex.EncodeToString(
		m5[:],
	)
	for _, repo := range []string{streamRepo, redirectRepo} {
		started := time.Now()
		resp, err := h.do(t, http.MethodPut, "/artifactory/"+repo+"/"+artifactPath, file.data,
			http.Header{"X-Checksum-Sha256": {file.sha256}, "X-Checksum-Sha1": {file.sha1}})
		if err != nil {
			t.Fatal(err)
		}
		b := drain(t, resp)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("deploy to %s: %d %s", repo, resp.StatusCode, b)
		}
		t.Logf(
			"deployed %d bytes to %s in %s",
			payloadSize,
			repo,
			time.Since(started).Round(time.Millisecond),
		)
	}
}

func roundTrip(t *testing.T, h *harness) {
	resp, err := h.do(t, http.MethodGet, "/artifactory/"+streamRepo+"/"+artifactPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != file.sha256 {
		t.Fatalf("round trip changed the content: %d bytes back, sha256 %x", len(got), sum)
	}
}

func rangedGet(t *testing.T, h *harness) {
	from, to := 3<<20, 5<<20-1
	resp, err := h.do(t, http.MethodGet, "/artifactory/"+streamRepo+"/"+artifactPath, nil,
		http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", from, to)}})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, resp)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("ranged GET: %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, file.data[from:to+1]) {
		t.Fatalf("ranged GET returned %d bytes that differ from bytes %d-%d", len(got), from, to)
	}
}

func checksums(t *testing.T, h *harness) {
	resp, err := h.do(
		t,
		http.MethodGet,
		"/artifactory/api/storage/"+streamRepo+"/"+artifactPath,
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	b := drain(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("storage info: %d %s", resp.StatusCode, b)
	}
	var info map[string]any
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	sums, _ := info["checksums"].(map[string]any)
	for name, want := range map[string]string{"sha256": file.sha256, "sha1": file.sha1, "md5": file.md5sum} {
		if got, _ := sums[name].(string); got != want {
			t.Errorf("checksums.%s = %q, want %q", name, got, want)
		}
	}
}

// headLocal: the JFrog CLI sends HEAD before a split download and needs a
// 200 with the length; a URL presigned for GET would answer it 403 (#117).
func headLocal(t *testing.T, h *harness) {
	resp, err := h.do(t, http.MethodHead, "/artifactory/"+redirectRepo+"/"+artifactPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
		t.Fatalf(
			"HEAD: %d Location=%q, want 200 from the registry",
			resp.StatusCode,
			resp.Header.Get("Location"),
		)
	}
	if resp.ContentLength != payloadSize {
		t.Errorf("HEAD Content-Length = %d, want %d", resp.ContentLength, payloadSize)
	}
	if got := resp.Header.Get("X-Checksum-Sha256"); got != file.sha256 {
		t.Errorf("HEAD X-Checksum-Sha256 = %q", got)
	}
}

// redirectLocation asks the registry for the file and returns the signed
// URL it redirects to, checking it points at the scenario's storage host.
func redirectLocation(t *testing.T, h *harness, header http.Header) *url.URL {
	t.Helper()
	resp, err := h.do(t, http.MethodGet, "/artifactory/"+redirectRepo+"/"+artifactPath, nil, header)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET: %d, want 302 to the storage host", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Hostname() != h.storageHost {
		t.Fatalf("redirected to %s, want %s", loc.Hostname(), h.storageHost)
	}
	if got := resp.Header.Get("X-Checksum-Sha256"); got != file.sha256 {
		t.Errorf("302 X-Checksum-Sha256 = %q, want %q", got, file.sha256)
	}
	return loc
}

func redirectToStorage(t *testing.T, h *harness) {
	loc := redirectLocation(t, h, nil)
	resp := fetch(t, h, loc, nil)
	got := drain(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed URL: %d", resp.StatusCode)
	}
	if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != file.sha256 {
		t.Fatalf("signed URL served different content: %d bytes", len(got))
	}
}

// rangedRedirect: the CLI's split download sends ranged GETs; each is
// redirected (decision 0029) and the storage service answers the range.
func rangedRedirect(t *testing.T, h *harness) {
	from, to := 10<<20, 12<<20-1
	rng := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", from, to)}}
	loc := redirectLocation(t, h, rng)
	resp := fetch(t, h, loc, rng)
	got := drain(t, resp)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("signed URL with Range: %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(got, file.data[from:to+1]) {
		t.Fatalf("signed URL served %d bytes that differ from bytes %d-%d", len(got), from, to)
	}
}

// unsignedRefused: without its query the URL must not serve the blob, so
// the bucket or container is private.
func unsignedRefused(t *testing.T, h *harness) {
	loc := redirectLocation(t, h, nil)
	bare := *loc
	bare.RawQuery = ""
	resp := fetch(t, h, &bare, nil)
	drain(t, resp)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		t.Fatalf(
			"storage served the blob without a signature (%d): the store is public",
			resp.StatusCode,
		)
	}
	t.Logf("unsigned request refused with %d", resp.StatusCode)
}

// fetch requests a storage URL as a package client would after leaving the
// registry: no credentials.
func fetch(t *testing.T, h *harness, u *url.URL, header http.Header) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.plain.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", redact(u), err)
	}
	return resp
}

func drain(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// redact drops a signed URL's query, which is a credential, from messages.
func redact(u *url.URL) string {
	c := *u
	if c.RawQuery != "" {
		c.RawQuery = "…"
	}
	return strings.TrimSpace(c.String())
}
