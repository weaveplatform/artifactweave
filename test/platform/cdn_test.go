package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// split download works across it. Cloud CDN comes up after apply returns, so
// gcp/cdn first waits for it, and checks one thing Google does not document:
// that the response overrides reach the bucket on a cache fill.

const cdnRepo = "platform-cdn"

func init() {
	scenarioChecks["aws/cdn"] = cdnChecks(cloudFrontSigner)
	scenarioChecks["gcp/cdn"] = append([]check{
		{"Cloud CDN passes both response overrides on a cache fill", overridesOnACacheFill},
	}, cdnChecks(cloudCDNSigner)...)
	scenarioReady["gcp/cdn"] = waitForCloudCDN
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

// cloudCDNSigner signs with the key in the scenario's blob.gcs.cdn.cloud_cdn,
// as artifactweave does.
func cloudCDNSigner(t *testing.T, h *harness, objectKey string, now time.Time) *url.URL {
	t.Helper()
	var c struct {
		Blob struct {
			GCS struct {
				CDN struct {
					CloudCDN struct {
						Domain     string `yaml:"domain"`
						KeyName    string `yaml:"key_name"`
						KeyFile    string `yaml:"key_file"`
						PathPrefix string `yaml:"path_prefix"`
					} `yaml:"cloud_cdn"`
				} `yaml:"cdn"`
			} `yaml:"gcs"`
		} `yaml:"blob"`
	}
	readConfig(t, h, &c)
	cc := c.Blob.GCS.CDN.CloudCDN
	key, err := os.ReadFile(cc.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := cdn.NewCloudCDN(cdn.CloudCDNConfig{
		Domain: cc.Domain, KeyName: cc.KeyName, Key: key, PathPrefix: cc.PathPrefix,
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

// cloudCDNWait bounds waitForCloudCDN: Google issues the certificate once
// its authorisation record resolves, commonly within 10 to 30 minutes, the
// load balancer takes minutes more to answer on its address, and the fill
// account's grant a few more to reach the bucket. platform-tests.sh's
// timeout leaves room for it.
const cloudCDNWait = 45 * time.Minute

// readyRepo holds the blob waitForCloudCDN fetches through the edge.
const readyRepo = "platform-cdn-ready"

// waitForCloudCDN deploys a blob of its own and waits until the edge serves
// it from a URL artifactweave signs, which needs all of the above: before
// then, the request fails, or the bucket refuses it while the grant is on
// its way. Cloud CDN does not cache a 403, so the wait sees the grant
// arrive. The blob is not the generic checks' payload, whose first fetch
// through the edge is theirs to make.
func waitForCloudCDN(t *testing.T, h *harness) {
	t.Helper()
	createRepository(t, h, map[string]any{
		"key": readyRepo, "rclass": "local", "packageType": "generic", "downloadRedirect": true,
	})
	data := make([]byte, 2<<20) // above download.redirect.min_size
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	deployData(t, h, readyRepo, "ready.bin", data)
	started := time.Now()
	last := ""
	for {
		why, ready := probeCloudCDN(
			t,
			h,
			redirectIn(t, h, readyRepo, "ready.bin", nil),
			data[:1024],
		)
		if ready {
			t.Logf("Cloud CDN served the blob after %s", time.Since(started).Round(time.Second))
			return
		}
		if why != last {
			t.Logf(
				"after %s, the edge is not ready: %s",
				time.Since(started).Round(time.Second),
				why,
			)
			last = why
		}
		if time.Since(started) > cloudCDNWait {
			t.Fatalf("Cloud CDN was not ready within %s; last: %s", cloudCDNWait, why)
		}
		time.Sleep(20 * time.Second)
	}
}

// probeCloudCDN reads the first KiB of a blob through the edge and reports
// what came back, without the URL, whose query is a credential.
func probeCloudCDN(t *testing.T, h *harness, u *url.URL, want []byte) (why string, ready bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", len(want)-1))
	resp, err := h.plain.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err.Error(), false
	}
	body := drain(t, resp)
	if resp.StatusCode == http.StatusPartialContent && bytes.Equal(body, want) {
		return "", true
	}
	return fmt.Sprintf("%d %s", resp.StatusCode, tail(strings.TrimSpace(string(body)), 200)), false
}

// deployData deploys data to repo/p with its checksums.
func deployData(t *testing.T, h *harness, repo, p string, data []byte) {
	t.Helper()
	s256, s1 := sha256.Sum256(data), sha1.Sum(data)
	resp, err := h.do(t, http.MethodPut, "/artifactory/"+repo+"/"+p, data, http.Header{
		"X-Checksum-Sha256": {hex.EncodeToString(s256[:])},
		"X-Checksum-Sha1":   {hex.EncodeToString(s1[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b := drain(t, resp); resp.StatusCode != http.StatusCreated {
		t.Fatalf("deploy %s/%s: %d %s", repo, p, resp.StatusCode, b)
	}
}

// overridesOnACacheFill settles what Google does not document (research 19,
// section 3.2): whether Cloud CDN passes response-content-disposition and
// response-content-type to the bucket when it fills its cache. New content
// is a blob no one has fetched, so its first download through the edge is a
// fill, and a .json name asks for a media type the stored object does not
// have. If Cloud CDN drops them, decision 0029's fallback applies: the
// stored object's headers are served, and package clients name files from
// the request.
func overridesOnACacheFill(t *testing.T, h *harness) {
	data := make([]byte, 2<<20) // above download.redirect.min_size
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	name := "scenario/fill-check.json"
	deployData(t, h, redirectRepo, name, data)
	loc := redirectIn(t, h, redirectRepo, name, nil)
	if !h.storageHosts[loc.Hostname()] {
		t.Fatalf("redirected to %s, want the CDN host %v", loc.Hostname(), h.storageHosts)
	}
	resp, got := fetchAll(t, h, loc, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("the edge served %d with %d bytes, want 200 with the %d deployed",
			resp.StatusCode, len(got), len(data))
	}
	disposition, mediaType := resp.Header.Get(
		"Content-Disposition",
	), resp.Header.Get(
		"Content-Type",
	)
	t.Logf("cache fill: Content-Disposition %q, Content-Type %q, Age %q",
		disposition, mediaType, resp.Header.Get("Age"))
	if !strings.Contains(disposition, path.Base(name)) {
		t.Errorf(
			"Cloud CDN dropped response-content-disposition on a cache fill: Content-Disposition %q, want %s",
			disposition,
			path.Base(name),
		)
	}
	if mediaType != "application/json" {
		t.Errorf(
			"Cloud CDN dropped response-content-type on a cache fill: Content-Type %q, want application/json",
			mediaType,
		)
	}
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
