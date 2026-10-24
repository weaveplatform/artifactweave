package cdn_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
)

// cloudCDNKey is the vectors' key: bytes 0 to 15, "AAECAwQFBgcICQoLDA0ODw==".
var cloudCDNKey = func() []byte {
	k := make([]byte, cdn.CloudCDNKeySize)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}()

// The expected URLs below were computed outside Go, by the Python sample on
// https://docs.cloud.google.com/cdn/docs/using-signed-urls (hmac and
// hashlib.sha1 over the URL with Expires and KeyName appended, then
// base64.urlsafe_b64encode), with the key above, KeyName "artifactweave-1"
// and Expires 1767225600. A mismatch means the Go signer has drifted from
// Google's algorithm.
func TestUnitSignCloudCDNMatchesGooglesSample(t *testing.T) {
	expires := time.Unix(1767225600, 0)
	cases := []struct{ in, want string }{
		{
			"https://cdn.example.com/art/sha256/ab/abcdef",
			"https://cdn.example.com/art/sha256/ab/abcdef?Expires=1767225600" +
				"&KeyName=artifactweave-1&Signature=nnISxcch4ucKOc-C26SRDLMfwEo=",
		},
		{
			"https://cdn.example.com/art/sha256/ab/abcdef?response-content-disposition=" +
				"attachment%3B%20filename%3D%22big.bin%22" +
				"&response-content-type=application%2Foctet-stream",
			"https://cdn.example.com/art/sha256/ab/abcdef?response-content-disposition=" +
				"attachment%3B%20filename%3D%22big.bin%22" +
				"&response-content-type=application%2Foctet-stream" +
				"&Expires=1767225600&KeyName=artifactweave-1&Signature=SsEJ56DwumDLNvUayBXweKL7-J4=",
		},
	}
	for _, c := range cases {
		if got := cdn.SignCloudCDN(c.in, "artifactweave-1", cloudCDNKey, expires); got != c.want {
			t.Errorf("SignCloudCDN(%s)\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}

// TestUnitCloudCDNSignsTheOverridesAndStripsThePathPrefix checks a whole URL,
// overrides included, against the same Python sample: the overrides are
// inside what is signed, so an edge refuses a URL whose filename was edited.
func TestUnitCloudCDNSignsTheOverridesAndStripsThePathPrefix(t *testing.T) {
	s, err := cdn.NewCloudCDN(cdn.CloudCDNConfig{
		Domain: "cdn.example.com", KeyName: "artifactweave-1", PathPrefix: "art",
		Key: []byte(base64.URLEncoding.EncodeToString(cloudCDNKey) + "\n"),
		// Windows are 5 minutes, so the URL expires at 1767225000 + 600.
		Now: func() time.Time { return time.Unix(1767225000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Sign("art/sha256/ab/abcdef", cdn.Request{
		TTL: 5 * time.Minute, Filename: "big.bin", ContentType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cdn.example.com/sha256/ab/abcdef?response-content-disposition=" +
		"attachment%3B%20filename%3D%22big.bin%22%3B%20filename%2A%3DUTF-8%27%27big.bin" +
		"&response-content-type=application%2Foctet-stream" +
		"&Expires=1767225600&KeyName=artifactweave-1&Signature=nUxXqxDlqRr0mBnQaRiqObXzwtM="
	if got != want {
		t.Fatalf("Sign\n got  %s\n want %s", got, want)
	}
}

func TestAccCloudCDNURLsPassTheEdge(t *testing.T) {
	body := blobtest.Content(9, 4096)
	store, sha := origin(t, body)
	now := time.Unix(1_800_000_000, 0)
	edge, err := cdntest.Start(cdntest.Options{
		Validator: cdntest.CloudCDN{Keys: map[string][]byte{"key-1": cloudCDNKey}},
		Origin:    store,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edge.Close() })
	signer := func(name string) *cdn.CloudCDN {
		s, err := cdn.NewCloudCDN(cdn.CloudCDNConfig{
			Domain: edge.URL, KeyName: name, PathPrefix: "/art/",
			Key: []byte(base64.RawURLEncoding.EncodeToString(cloudCDNKey)),
			Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := signer("key-1")
	if s.Provider() != "cloud_cdn" || s.Covers("art/") != nil || s.Covers("") == nil {
		t.Fatal("provider or prefix coverage")
	}
	u, err := s.Sign("art/sha256/"+sha[:2]+"/"+sha, cdn.Request{
		TTL: time.Minute, Filename: "lib 1.0+beta.tgz", ContentType: "application/gzip",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, got := get(t, u, map[string]string{"Range": "bytes=10-19"})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, body[10:20]) {
		t.Fatalf("signed ranged GET: %d %q", resp.StatusCode, got)
	}
	if resp.Header.Get("Content-Disposition") != blob.Disposition("lib 1.0+beta.tgz") ||
		resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("overrides: %v", resp.Header)
	}
	refused := map[string]string{
		"unsigned":         strings.SplitN(u, "?", 2)[0],
		"filename edited":  strings.Replace(u, "beta", "gamma", 1),
		"unknown key name": mustSign(t, signer("key-2"), sha),
	}
	for name, raw := range refused {
		if resp, _ := get(t, raw, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, resp.StatusCode)
		}
	}
	now = now.Add(3 * time.Minute)
	if resp, _ := get(t, u, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an expired URL: %d, want 403", resp.StatusCode)
	}
}

func mustSign(t *testing.T, s *cdn.CloudCDN, sha string) string {
	t.Helper()
	u, err := s.Sign("art/sha256/"+sha[:2]+"/"+sha, cdn.Request{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUnitCloudCDNURLsRepeatWithinAWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s, err := cdn.NewCloudCDN(cdn.CloudCDNConfig{
		Domain: "cdn.example.com", KeyName: "k",
		Key: []byte(base64.URLEncoding.EncodeToString(cloudCDNKey)),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	r := cdn.Request{TTL: 5 * time.Minute}
	a, _ := s.Sign("sha256/ab/abc", r)
	now = now.Add(4 * time.Minute)
	b, _ := s.Sign("sha256/ab/abc", r)
	now = now.Add(5 * time.Minute)
	c, _ := s.Sign("sha256/ab/abc", r)
	if a != b || a == c || !strings.Contains(a, "Expires=1800000600&") {
		t.Fatalf("URLs within a window must repeat and the next window's differ:\n%s\n%s\n%s",
			a, b, c)
	}
}

func TestUnitCloudCDNRefusals(t *testing.T) {
	key := []byte(base64.URLEncoding.EncodeToString(cloudCDNKey))
	ok := cdn.CloudCDNConfig{Domain: "cdn.example.com", KeyName: "key-1", Key: key}
	bad := map[string]func(c *cdn.CloudCDNConfig){
		"no key name":       func(c *cdn.CloudCDNConfig) { c.KeyName = "" },
		"upper-case name":   func(c *cdn.CloudCDNConfig) { c.KeyName = "Key1" },
		"name with a space": func(c *cdn.CloudCDNConfig) { c.KeyName = "key 1" },
		"name ending in -":  func(c *cdn.CloudCDNConfig) { c.KeyName = "key-" },
		"name over 63":      func(c *cdn.CloudCDNConfig) { c.KeyName = "k" + strings.Repeat("a", 63) },
		"bad domain":        func(c *cdn.CloudCDNConfig) { c.Domain = "ftp://x" },
		"key not base64url": func(c *cdn.CloudCDNConfig) { c.Key = []byte("not+base64/!") },
		"key of the wrong size": func(c *cdn.CloudCDNConfig) {
			c.Key = []byte(base64.URLEncoding.EncodeToString(make([]byte, 32)))
		},
	}
	for name, mutate := range bad {
		c := ok
		mutate(&c)
		if _, err := cdn.NewCloudCDN(c); !errors.Is(err, cdn.ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	longest := ok
	longest.KeyName = "k" + strings.Repeat("a", 62)
	if _, err := cdn.NewCloudCDN(longest); err != nil {
		t.Fatalf("a 63-character key name: %v", err)
	}
	prefixed := ok
	prefixed.PathPrefix = "art"
	s, err := cdn.NewCloudCDN(prefixed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(
		"other/sha256/ab/abc",
		cdn.Request{TTL: time.Minute},
	); !errors.Is(
		err,
		cdn.ErrConfig,
	) {
		t.Fatalf("a key outside the path prefix: %v", err)
	}
	if _, err := s.Sign("art/sha256/ab/abc", cdn.Request{}); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("a zero TTL: %v", err)
	}
}
