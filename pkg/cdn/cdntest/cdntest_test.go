package cdntest_test

import (
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
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
)

// failing is an origin whose Open fails with err.
type failing struct {
	blob.Store
	err error
}

func (f failing) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, f.err
}

type world struct {
	edge   *cdntest.Edge
	signer *cdn.CloudFront
	sha    string
	body   []byte
	now    time.Time
}

func newWorld(t *testing.T, origin func(blob.Store) blob.Store) *world {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ed, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{body: blobtest.Content(3, 2048), now: time.Unix(1_800_000_000, 0)}
	w.sha = blobtest.Put(context.Background(), t, s, w.body).Digests.SHA256
	var o blob.Store = s
	if origin != nil {
		o = origin(s)
	}
	w.edge, err = cdntest.Start(cdntest.Options{
		Validator: cdntest.CloudFront{Keys: map[string]crypto.PublicKey{
			"RSA": &rk.PublicKey, "EC": &ek.PublicKey, "ED": ed,
		}},
		Origin: o,
		Now:    func() time.Time { return w.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.edge.Close() })
	pemKey := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk),
	})
	w.signer, err = cdn.NewCloudFront(cdn.CloudFrontConfig{
		Domain: w.edge.URL, KeyPairID: "RSA", PrivateKey: pemKey,
		Now: func() time.Time { return w.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) url(t *testing.T, key string) string {
	t.Helper()
	u, err := w.signer.Sign(key, cdn.Request{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (w *world) key() string { return "sha256/" + w.sha[:2] + "/" + w.sha }

func do(t *testing.T, method, raw string, hdr http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hdr != nil {
		req.Header = hdr
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEdgeServesSignedRequestsAndRecordsThem(t *testing.T) {
	w := newWorld(t, nil)
	u := w.url(t, w.key())
	if code, body := do(
		t,
		http.MethodGet,
		u,
		nil,
	); code != http.StatusOK ||
		body != string(w.body) {
		t.Fatalf("signed GET: %d", code)
	}
	if code, _ := do(
		t,
		http.MethodHead,
		u,
		http.Header{"Authorization": {"Bearer x"}},
	); code != http.StatusOK {
		t.Fatalf("signed HEAD: %d", code)
	}
	if code, _ := do(t, http.MethodPost, u, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", code)
	}
	reqs := w.edge.Requests()
	if len(reqs) != 2 || reqs[0].Authorization || !reqs[1].Authorization ||
		reqs[0].Status != http.StatusOK || reqs[1].Method != http.MethodHead ||
		!strings.HasSuffix(reqs[0].Path, w.sha) {
		t.Fatalf("recorded %+v", reqs)
	}
}

func TestEdgeRefusesBadSignatures(t *testing.T) {
	w := newWorld(t, nil)
	good := w.url(t, w.key())
	replace := func(param, value string) string {
		head, query, _ := strings.Cut(good, "?")
		parts := strings.Split(query, "&")
		for i, p := range parts {
			if strings.HasPrefix(p, param+"=") {
				parts[i] = param + "=" + value
			}
		}
		return head + "?" + strings.Join(parts, "&")
	}
	sig := good[strings.Index(good, "Signature=")+len("Signature="):]
	sig = sig[:strings.Index(sig, "&")]
	tampered := []byte(sig)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	cases := map[string]string{
		"unsigned":         w.edge.URL + "/" + w.key(),
		"custom policy":    good + "&Policy=eyJ9",
		"bad expires":      replace("Expires", "soon"),
		"unknown key":      replace("Key-Pair-Id", "NOPE"),
		"bad hash":         good + "&Hash-Algorithm=MD5",
		"not base64":       replace("Signature", "%25%25%25"),
		"tampered":         replace("Signature", string(tampered)),
		"other key's type": replace("Key-Pair-Id", "ED"),
		"wrong ECDSA key":  replace("Key-Pair-Id", "EC"),
		"other path":       strings.Replace(good, w.sha, strings.Repeat("0", 64), 1),
	}
	for name, u := range cases {
		if code, _ := do(t, http.MethodGet, u, nil); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	w.now = w.now.Add(time.Hour)
	if code, body := do(t, http.MethodGet, good, nil); code != http.StatusForbidden ||
		!strings.Contains(body, "expired") {
		t.Fatalf("expired: %d %s", code, body)
	}
}

func TestEdgeOriginErrors(t *testing.T) {
	w := newWorld(t, nil)
	if code, _ := do(
		t,
		http.MethodGet,
		w.url(t, "sha256/ab/not-a-digest"),
		nil,
	); code != http.StatusNotFound {
		t.Fatalf("a path that names no blob: %d", code)
	}
	missing := strings.Repeat("ab", 32)
	if code, _ := do(
		t,
		http.MethodGet,
		w.url(t, "sha256/ab/"+missing),
		nil,
	); code != http.StatusNotFound {
		t.Fatalf("a blob the origin lacks: %d", code)
	}
	broken := newWorld(t, func(s blob.Store) blob.Store {
		return failing{Store: s, err: errors.New("disk on fire")}
	})
	if code, _ := do(
		t,
		http.MethodGet,
		broken.url(t, broken.key()),
		nil,
	); code != http.StatusBadGateway {
		t.Fatalf("a failing origin: %d", code)
	}
}

func TestStartNeedsAValidatorAnOriginAndAHost(t *testing.T) {
	if _, err := cdntest.Start(cdntest.Options{}); err == nil {
		t.Fatal("no validator or origin")
	}
	s, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cdntest.Start(cdntest.Options{
		Validator: cdntest.CloudFront{}, Origin: s, Host: "256.0.0.1",
	}); err == nil {
		t.Fatal("an address that cannot listen")
	}
	e, err := cdntest.Start(cdntest.Options{Validator: cdntest.CloudFront{}, Origin: s})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(e.URL, "http://localhost:") {
		t.Fatalf("default host: %s", e.URL)
	}
	_ = e.Close()
}

func TestCloudCDNEdgeRefusesBadSignatures(t *testing.T) {
	s, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := blobtest.Content(4, 1024)
	sha := blobtest.Put(context.Background(), t, s, body).Digests.SHA256
	key := []byte("0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	edge, err := cdntest.Start(cdntest.Options{
		Validator: cdntest.CloudCDN{Keys: map[string][]byte{"key-1": key}},
		Origin:    s,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edge.Close() })
	path := edge.URL + "/sha256/" + sha[:2] + "/" + sha
	good := cdn.SignCloudCDN(path, "key-1", key, now.Add(time.Minute))
	if code, got := do(t, http.MethodGet, good, nil); code != http.StatusOK || got != string(body) {
		t.Fatalf("signed GET: %d", code)
	}
	if code, _ := do(t, http.MethodGet, strings.TrimSuffix(good, "="), nil); code != http.StatusOK {
		t.Fatalf("an unpadded signature: %d", code)
	}
	sig := good[strings.LastIndex(good, "Signature=")+len("Signature="):]
	tampered := []byte(sig)
	tampered[0] ^= 1
	cases := map[string]string{
		"unsigned":           path,
		"url prefix":         good + "&URLPrefix=aHR0cA==",
		"signature not last": good + "&x=1",
		"keyname not before": path + "?KeyName=key-1&Expires=1&Signature=" + sig,
		"bad expires": cdn.SignCloudCDN(
			path+"?Expires=soon&KeyName=key-1",
			"key-1",
			key,
			now,
		),
		"unknown key": cdn.SignCloudCDN(path, "key-2", key, now.Add(time.Minute)),
		"not base64":  strings.Replace(good, "Signature="+sig, "Signature=%25%25", 1),
		"tampered":    strings.Replace(good, sig, string(tampered), 1),
		"other path":  strings.Replace(good, sha, strings.Repeat("0", 64), 1),
		"key from other edge": cdn.SignCloudCDN(
			path,
			"key-1",
			[]byte("fedcba9876543210"),
			now.Add(time.Minute),
		),
	}
	for name, u := range cases {
		if code, _ := do(t, http.MethodGet, u, nil); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	now = now.Add(time.Hour)
	if code, got := do(t, http.MethodGet, good, nil); code != http.StatusForbidden ||
		!strings.Contains(got, "expired") {
		t.Fatalf("expired: %d %s", code, got)
	}
}
