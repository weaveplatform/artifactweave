package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// The CDN scenarios (#132 to #134): a store behind a real edge. A CDN
// scenario's storage_host is the CDN host, so the generic checks' redirected
// downloads, whole and ranged, and their refusal of an unsigned URL already
// go through the edge (decision 0029). These checks add what only a real
// edge can show: that cdnRedirect alone redirects, that the S3 header still
// reaches the bucket, that the edge keys its cache on the filename, that it
// refuses a URL whose signature is valid but expired, and that the JFrog CLI's
// split download works across it.

const cdnRepo = "platform-cdn"

func init() {
	scenarioChecks["aws/cdn"] = cdnChecks(cloudFrontSigner)
}

// edgeSigner signs a URL for objectKey on the scenario's CDN as if at now,
// with the key the scenario's configuration names, so a check can make a
// URL that has already expired.
type edgeSigner func(t *testing.T, h *harness, objectKey string, now time.Time) *url.URL

func cdnChecks(sign edgeSigner) []check {
	return []check{
		{"cdnRedirect alone lands on the CDN host", cdnRedirectAlone},
		{"X-JFrog-Download-Redirect-To: S3 lands on the store", storeHeaderLandsOnTheOrigin},
		{"the edge keeps each item's filename", filenamesSurviveTheEdgeCache},
		{"the edge refuses an expired signature", func(t *testing.T, h *harness) {
			expiredSignatureRefused(t, h, sign)
		}},
		{"jf rt dl splits a download across the edge", jfDownloadsThroughTheEdge},
	}
}

// cdnRedirectAlone: a repository with cdnRedirect and no downloadRedirect
// redirects to the CDN, as Artifactory's SaaS CDN does.
func cdnRedirectAlone(t *testing.T, h *harness) {
	createRepository(t, h, map[string]any{
		"key": cdnRepo, "rclass": "local", "packageType": "generic", "cdnRedirect": true,
	})
	deployByChecksum(t, h, cdnRepo, artifactPath)
	loc := redirectIn(t, h, cdnRepo, artifactPath, nil)
	if !h.storageHosts[loc.Hostname()] {
		t.Fatalf("redirected to %s, want the CDN host %v", loc.Hostname(), h.storageHosts)
	}
	fetchRange(t, h, loc, 0, 1<<20-1)
}

// storeHeaderLandsOnTheOrigin: S3 means the object store, not the CDN, so a
// downloadRedirect repository's download goes to a presigned URL on the
// bucket itself, which the store still signs beside the CDN.
func storeHeaderLandsOnTheOrigin(t *testing.T, h *harness) {
	origin := h.output(t, "origin_host")
	loc := redirectIn(t, h, redirectRepo, artifactPath,
		http.Header{"X-Jfrog-Download-Redirect-To": {"S3"}})
	if loc.Hostname() != origin {
		t.Fatalf("redirected to %s, want the store's own host %s", loc.Hostname(), origin)
	}
	fetchRange(t, h, loc, 5<<20, 6<<20-1)
}

// filenamesSurviveTheEdgeCache: two items with the same content are one blob,
// one object key, but each URL carries its own filename as a response
// override. The edge has already cached the first item's whole download, so
// the second comes back with its own filename only if the cache key holds
// the override.
func filenamesSurviveTheEdgeCache(t *testing.T, h *harness) {
	second := "scenario/second-name.bin"
	deployByChecksum(t, h, redirectRepo, second)
	for _, p := range []string{artifactPath, second} {
		loc := redirectIn(t, h, redirectRepo, p, nil)
		resp := fetchRange(t, h, loc, 0, 1023)
		got := resp.Header.Get("Content-Disposition")
		if !strings.Contains(got, path.Base(p)) {
			t.Fatalf("the edge served %s with Content-Disposition %q, want its own name %s",
				p, got, path.Base(p))
		}
		t.Logf("%s: Content-Disposition %q, X-Cache %q", p, got, resp.Header.Get("X-Cache"))
	}
}

// expiredSignatureRefused signs a URL for the blob with the scenario's own
// key twice: now, which the edge must serve, so the key is the one it
// trusts; and two hours ago, which it must refuse, because it checks the
// expiry on every request, cached or not.
func expiredSignatureRefused(t *testing.T, h *harness, sign edgeSigner) {
	key := strings.TrimPrefix(redirectIn(t, h, redirectRepo, artifactPath, nil).Path, "/")
	fetchRange(t, h, sign(t, h, key, time.Now()), 0, 1023)

	expired := sign(t, h, key, time.Now().Add(-2*time.Hour))
	resp := fetch(t, h, expired, http.Header{"Range": {"bytes=0-1023"}})
	drain(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an expired signature got %d, want 403", resp.StatusCode)
	}
}

// jfDownloadsThroughTheEdge: above its 5,120,000-byte threshold the JFrog
// CLI sends a HEAD, which the registry answers, then ranged GETs, each
// redirected to the edge without the CLI's credentials.
// platform-scenarios.yml installs the pinned jf; a local run without one
// skips this check.
func jfDownloadsThroughTheEdge(t *testing.T, h *harness) {
	jf := os.Getenv("ARTIFACTWEAVE_JF_BIN")
	if jf == "" {
		t.Skip("ARTIFACTWEAVE_JF_BIN is not set (platform-scenarios.yml installs the pinned jf)")
	}
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, jf, "rt", "dl", redirectRepo+"/"+artifactPath, out+"/",
		"--flat", "--url", h.url+"/artifactory", "--user", "admin", "--password", h.password)
	cmd.Env = append(os.Environ(),
		"JFROG_CLI_HOME_DIR="+t.TempDir(),
		"JFROG_CLI_REPORT_USAGE=false",
		"JFROG_CLI_AVOID_NEW_VERSION_WARNING=true",
		"CI=true",
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jf rt dl: %v\n%s", err, tail(string(b), 4000))
	}
	got, err := os.ReadFile(filepath.Join(out, path.Base(artifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, file.data) {
		t.Fatalf(
			"jf rt dl wrote %d bytes that differ from the %d deployed",
			len(got),
			len(file.data),
		)
	}
}

// cloudFrontSigner signs with the key pair in the scenario's
// blob.s3.cdn.cloudfront, as artifactweave does.
func cloudFrontSigner(t *testing.T, h *harness, objectKey string, now time.Time) *url.URL {
	t.Helper()
	var c struct {
		Blob struct {
			S3 struct {
				CDN struct {
					CloudFront struct {
						Domain         string `yaml:"domain"`
						KeyPairID      string `yaml:"key_pair_id"`
						PrivateKeyFile string `yaml:"private_key_file"`
					} `yaml:"cloudfront"`
				} `yaml:"cdn"`
			} `yaml:"s3"`
		} `yaml:"blob"`
	}
	readConfig(t, h, &c)
	cf := c.Blob.S3.CDN.CloudFront
	pem, err := os.ReadFile(cf.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := cdn.NewCloudFront(cdn.CloudFrontConfig{
		Domain: cf.Domain, KeyPairID: cf.KeyPairID, PrivateKey: pem,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signer.Sign(objectKey, cdn.Request{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func readConfig(t *testing.T, h *harness, into any) {
	t.Helper()
	raw, err := os.ReadFile(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", h.config, err)
	}
}

func createRepository(t *testing.T, h *harness, repo map[string]any) {
	t.Helper()
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
	if b := drain(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("create %s: %d %s", repo["key"], resp.StatusCode, b)
	}
}

// deployByChecksum deploys the generic checks' payload to repo/p without
// sending it again: the store already holds its blob.
func deployByChecksum(t *testing.T, h *harness, repo, p string) {
	t.Helper()
	resp, err := h.do(t, http.MethodPut, "/artifactory/"+repo+"/"+p, nil, http.Header{
		"X-Checksum-Deploy": {"true"},
		"X-Checksum-Sha256": {file.sha256},
		"X-Checksum-Sha1":   {file.sha1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b := drain(t, resp); resp.StatusCode != http.StatusCreated {
		t.Fatalf("checksum deploy to %s/%s: %d %s", repo, p, resp.StatusCode, b)
	}
}

// redirectIn asks the registry for repo/p and returns where its 302 points.
func redirectIn(t *testing.T, h *harness, repo, p string, header http.Header) *url.URL {
	t.Helper()
	resp, err := h.do(t, http.MethodGet, "/artifactory/"+repo+"/"+p, nil, header)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET %s/%s: %d, want 302", repo, p, resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// fetchRange reads bytes from to to (inclusive) of the payload from a signed
// URL and checks them, returning the response for its headers.
func fetchRange(t *testing.T, h *harness, u *url.URL, from, to int) *http.Response {
	t.Helper()
	resp := fetch(t, h, u, http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", from, to)}})
	got := drain(t, resp)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("GET %s with Range: %d, want 206", redact(u), resp.StatusCode)
	}
	if !bytes.Equal(got, file.data[from:to+1]) {
		t.Fatalf(
			"%s served %d bytes that differ from bytes %d-%d",
			u.Hostname(),
			len(got),
			from,
			to,
		)
	}
	return resp
}
