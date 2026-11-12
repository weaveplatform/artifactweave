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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
// URL it redirects to, checking it points at one of the scenario's storage
// hosts (a mirror may sign on any member that holds the blob).
func redirectLocation(t *testing.T, h *harness, header http.Header) *url.URL {
	t.Helper()
	return redirectFor(t, h, artifactPath, file.sha256, header)
}

// redirectFor is redirectLocation for any file of the redirect repository,
// whose content has the sha256 digest want.
func redirectFor(t *testing.T, h *harness, path, want string, header http.Header) *url.URL {
	t.Helper()
	resp, err := h.do(t, http.MethodGet, "/artifactory/"+redirectRepo+"/"+path, nil, header)
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
	if !h.storageHosts[loc.Hostname()] {
		t.Fatalf(
			"redirected to %s, want one of the scenario's storage hosts %v",
			loc.Hostname(),
			h.storageHosts,
		)
	}
	if got := resp.Header.Get("X-Checksum-Sha256"); got != want {
		t.Errorf("302 X-Checksum-Sha256 = %q, want %q", got, want)
	}
	return loc
}

func redirectToStorage(t *testing.T, h *harness) {
	loc := redirectLocation(t, h, nil)
	resp, got := fetchAll(t, h, loc, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed URL: %s", describe(resp, got))
	}
	if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != file.sha256 {
		t.Fatalf("signed URL served different content: %d bytes, %s", len(got), describe(resp, nil))
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
		t.Fatalf("signed URL with Range: %s, want 206", describe(resp, got))
	}
	if !bytes.Equal(got, file.data[from:to+1]) {
		t.Fatalf("signed URL served %d bytes that differ from bytes %d-%d, %s",
			len(got), from, to, describe(resp, nil))
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
			"storage served the blob without a signature (%s): the store is public",
			describe(resp, nil),
		)
	}
	t.Logf("unsigned request refused with %d", resp.StatusCode)
}

// fetch requests a storage URL as a package client would after leaving the
// registry: no credentials. A request to one of the scenario's storage hosts
// also carries the scenario's edgeHeader, which asks only for diagnostic
// headers.
func fetch(t *testing.T, h *harness, u *url.URL, header http.Header) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.storageHosts[u.Hostname()] {
		for k, v := range h.edgeHeader {
			req.Header[k] = v
		}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.plain.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", redact(u), withoutURL(err))
	}
	return resp
}

// fetchAll fetches a storage or edge URL and reads the whole body. A
// transport error while reading, such as the provider resetting the stream
// (Azure did once, 40 MiB into run 38906963602), is logged with the
// provider's request ID and retried once: the signed URL is still valid, a
// download client retries the same way, and what is under test is the URL
// artifactweave issued, not the provider's connection.
func fetchAll(t *testing.T, h *harness, u *url.URL, header http.Header) (*http.Response, []byte) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		resp := fetch(t, h, u, header)
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err == nil {
			return resp, b
		}
		t.Logf("GET %s: reading the body failed on attempt %d (%s, %d bytes read, %s): %v",
			redact(u), attempt, resp.Proto, len(b), describe(resp, nil), err)
		if attempt == 2 {
			t.Fatalf("GET %s: %v", redact(u), err)
		}
	}
}

// answerHeaders are the response headers that say which tier answered a
// request and name it in that provider's logs. A status alone cannot: in run
// 42979295008 Front Door answered 404 and the log could not show whether an
// edge server, the account or something else had refused (#1211).
//   - Front Door: X-Azure-Ref finds the request in its access log; X-Cache
//     says whether the edge answered from its cache; X-Azure-OriginStatusCode,
//     X-Azure-InternalError and X-Azure-ExternalError, sent only to a request
//     that carries X-Azure-DebugInfo, give the origin's status and Front
//     Door's own error.
//   - Azure Storage: X-Ms-Error-Code and X-Ms-Request-Id, which only the
//     account sets, so through Front Door they show the account answered.
//   - CloudFront and S3: X-Cache ("Error from cloudfront"), X-Amz-Cf-Pop,
//     X-Amz-Cf-Id and X-Amz-Request-Id.
//   - Cloud CDN and GCS: Via ("1.1 google") for Google's front end, Age for a
//     cached copy, and X-Guploader-Uploadid for the bucket's request.
var answerHeaders = []string{
	"X-Azure-Ref", "X-Cache", "X-Azure-OriginStatusCode", "X-Azure-InternalError",
	"X-Azure-ExternalError", "X-Ms-Error-Code", "X-Ms-Request-Id", "X-Amz-Cf-Pop", "X-Amz-Cf-Id",
	"X-Amz-Request-Id", "Via", "Age", "X-Guploader-Uploadid",
}

// answerBodyBytes bounds the body a description quotes: Azure, S3 and GCS
// put an error's code and message in the first few hundred bytes of their
// XML, and Front Door's own error page names itself near its start.
const answerBodyBytes = 512

// describe tells what answered a storage or edge request, for a check that
// did not expect the answer: the status, the answerHeaders present, and the
// start of body. A nil body (not read, or the payload itself) is left out.
func describe(resp *http.Response, body []byte) string {
	var b strings.Builder
	b.WriteString(resp.Status)
	var found []string
	for _, k := range answerHeaders {
		if v := resp.Header.Get(k); v != "" {
			found = append(found, k+": "+v)
		}
	}
	if len(found) == 0 {
		b.WriteString(" [no provider headers]")
	} else {
		b.WriteString(" [" + strings.Join(found, ", ") + "]")
	}
	if body != nil {
		b.WriteString(" body: " + bodyStart(body))
	}
	return b.String()
}

// bodyStart quotes the start of an answer's body with its whitespace
// collapsed, or says it is not text: a blob served where an error was due
// would only flood the log.
func bodyStart(body []byte) string {
	if len(body) == 0 {
		return "empty"
	}
	p, cut := body, false
	if len(p) > answerBodyBytes {
		p, cut = p[:answerBodyBytes], true
		// The cut may split a character; drop its leading bytes.
		for i := 1; i < utf8.UTFMax && !utf8.Valid(p); i++ {
			p = p[:len(p)-1]
		}
	}
	if !utf8.Valid(p) {
		return "not text"
	}
	s := strconv.Quote(strings.Join(strings.Fields(string(p)), " "))
	if cut {
		s += " …"
	}
	return s
}

// withoutURL drops the request URL that the HTTP client puts in its errors:
// a signed URL's query is a credential, which redact keeps out of messages.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
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
