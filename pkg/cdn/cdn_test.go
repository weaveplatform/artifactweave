package cdn_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
)

func TestExpiryIsTheWindowEndPlusTheTTL(t *testing.T) {
	ttl := 5 * time.Minute
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct{ now, want string }{
		{"2026-10-14T12:00:00Z", "2026-10-14T12:10:00Z"},
		{"2026-10-14T12:04:59Z", "2026-10-14T12:10:00Z"},
		{"2026-10-14T12:05:00Z", "2026-10-14T12:15:00Z"},
	}
	for _, c := range cases {
		got, err := cdn.Expiry(at(c.now), ttl)
		if err != nil || !got.Equal(at(c.want)) {
			t.Errorf("Expiry(%s) = %s, %v; want %s", c.now, got, err, c.want)
		}
	}
	if _, err := cdn.Expiry(time.Now(), 500*time.Millisecond); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("a TTL under a second: %v", err)
	}
}

func TestParseDomain(t *testing.T) {
	for in, want := range map[string]string{
		"d111.cloudfront.net":      "https://d111.cloudfront.net",
		"https://cdn.example.com/": "https://cdn.example.com",
		"http://localhost:8080":    "http://localhost:8080",
	} {
		u, err := cdn.ParseDomain(in)
		if err != nil || u.String() != want {
			t.Errorf("ParseDomain(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, bad := range []string{
		"", "ftp://cdn.example.com", "https://cdn.example.com/path", "https://cdn.example.com?a=b",
		"https://user@cdn.example.com", "https://:443", "https://cdn.example.com#x", "%zz",
	} {
		if _, err := cdn.ParseDomain(bad); !errors.Is(err, cdn.ErrConfig) {
			t.Errorf("ParseDomain(%q) accepted: %v", bad, err)
		}
	}
}

// keyPEM is a generated key in one PEM form.
type keyPEM struct {
	name string
	pem  []byte
	pub  crypto.PublicKey
}

func keys(t *testing.T) []keyPEM {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := func(typ string, der []byte) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	}
	rsa8, err := x509.MarshalPKCS8PrivateKey(rk)
	if err != nil {
		t.Fatal(err)
	}
	ec1, err := x509.MarshalECPrivateKey(ek)
	if err != nil {
		t.Fatal(err)
	}
	ec8, err := x509.MarshalPKCS8PrivateKey(ek)
	if err != nil {
		t.Fatal(err)
	}
	return []keyPEM{
		{"RSA PKCS#1", enc("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rk)), &rk.PublicKey},
		{"RSA PKCS#8", enc("PRIVATE KEY", rsa8), &rk.PublicKey},
		{"ECDSA SEC 1", enc("EC PRIVATE KEY", ec1), &ek.PublicKey},
		{"ECDSA PKCS#8", enc("PRIVATE KEY", ec8), &ek.PublicKey},
	}
}

// origin is an fs store holding body.
func origin(t *testing.T, body []byte) (blob.Store, string) {
	t.Helper()
	s, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := blobtest.Put(context.Background(), t, s, body)
	return s, info.Digests.SHA256
}

func get(t *testing.T, raw string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

func TestCloudFrontURLsPassTheEdge(t *testing.T) {
	body := blobtest.Content(7, 4096)
	store, sha := origin(t, body)
	key := "art/sha256/" + sha[:2] + "/" + sha
	for _, k := range keys(t) {
		for _, hash := range []string{"", "sha1", "SHA256"} {
			t.Run(k.name+" "+hash, func(t *testing.T) {
				edge, err := cdntest.Start(cdntest.Options{
					Validator: cdntest.CloudFront{Keys: map[string]crypto.PublicKey{"K1": k.pub}},
					Origin:    store,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = edge.Close() })
				s, err := cdn.NewCloudFront(cdn.CloudFrontConfig{
					Domain: edge.URL, KeyPairID: "K1", PrivateKey: k.pem, Hash: hash,
					PathPrefix: "/art",
				})
				if err != nil {
					t.Fatal(err)
				}
				if s.Provider() != "cloudfront" || s.Covers("art/") != nil || s.Covers("") == nil {
					t.Fatal("provider or prefix coverage")
				}
				u, err := s.Sign(key, cdn.Request{
					TTL: time.Minute, Filename: "lib 1.0+beta.tgz", ContentType: "application/gzip",
				})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(u, edge.URL+"/sha256/") || strings.Contains(u, "+") {
					t.Fatalf("URL %q: want the origin path stripped and no literal +", u)
				}
				resp, got := get(t, u, map[string]string{"Range": "bytes=10-19"})
				if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, body[10:20]) {
					t.Fatalf("signed ranged GET: %d %q", resp.StatusCode, got)
				}
				if resp.Header.Get("Content-Disposition") != blob.Disposition("lib 1.0+beta.tgz") ||
					resp.Header.Get("Content-Type") != "application/gzip" {
					t.Fatalf("overrides: %v", resp.Header)
				}
			})
		}
	}
}

func TestCloudFrontURLsRepeatWithinAWindow(t *testing.T) {
	k := keys(t)[0]
	now := time.Unix(1_800_000_000, 0)
	s, err := cdn.NewCloudFront(cdn.CloudFrontConfig{
		Domain: "d111.cloudfront.net", KeyPairID: "K1", PrivateKey: k.pem,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	r := cdn.Request{TTL: 5 * time.Minute}
	a, err := s.Sign("sha256/ab/abc", r)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Minute)
	b, _ := s.Sign("sha256/ab/abc", r)
	now = now.Add(5 * time.Minute)
	c, _ := s.Sign("sha256/ab/abc", r)
	if a != b || a == c {
		t.Fatalf(
			"URLs within a window must repeat and the next window's differ:\n%s\n%s\n%s",
			a,
			b,
			c,
		)
	}
	u, err := url.Parse(a)
	if err != nil || u.Scheme != "https" || u.Host != "d111.cloudfront.net" ||
		u.Query().Get("Expires") != "1800000600" {
		t.Fatalf("URL %q", a)
	}
}

func TestCloudFrontRefusals(t *testing.T) {
	k := keys(t)[0]
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ed8, err := x509.MarshalPKCS8PrivateKey(edPriv)
	if err != nil {
		t.Fatal(err)
	}
	ok := cdn.CloudFrontConfig{Domain: "d1.cloudfront.net", KeyPairID: "K1", PrivateKey: k.pem}
	bad := map[string]func(c *cdn.CloudFrontConfig){
		"no key pair id": func(c *cdn.CloudFrontConfig) { c.KeyPairID = "" },
		"bad domain":     func(c *cdn.CloudFrontConfig) { c.Domain = "ftp://x" },
		"not PEM":        func(c *cdn.CloudFrontConfig) { c.PrivateKey = []byte("nope") },
		"not a key": func(c *cdn.CloudFrontConfig) {
			c.PrivateKey = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
		},
		"ed25519": func(c *cdn.CloudFrontConfig) {
			c.PrivateKey = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ed8})
		},
		"bad hash": func(c *cdn.CloudFrontConfig) { c.Hash = "md5" },
	}
	for name, mutate := range bad {
		c := ok
		mutate(&c)
		if _, err := cdn.NewCloudFront(c); !errors.Is(err, cdn.ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	prefixed := ok
	prefixed.PathPrefix = "art"
	s, err := cdn.NewCloudFront(prefixed)
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
		t.Fatalf("a key outside the origin path: %v", err)
	}
	if _, err := s.Sign("art/sha256/ab/abc", cdn.Request{}); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("a zero TTL: %v", err)
	}
}
