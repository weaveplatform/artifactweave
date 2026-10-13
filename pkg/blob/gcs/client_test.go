package gcs

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

func TestNormaliseEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:4443":              "http://localhost:4443/storage/v1/",
		"http://localhost:4443/":             "http://localhost:4443/storage/v1/",
		"https://example.test/storage/v1":    "https://example.test/storage/v1/",
		"https://example.test/storage/v1/":   "https://example.test/storage/v1/",
		"https://example.test/proxy/gcs/api": "https://example.test/proxy/gcs/api/storage/v1/",
	} {
		if got := normaliseEndpoint(in); got != want {
			t.Errorf("normaliseEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewAPIValidates(t *testing.T) {
	ctx := context.Background()
	if _, err := NewAPI(
		ctx,
		"",
		ClientConfig{WithoutAuthentication: true},
	); !errors.Is(
		err,
		ErrConfig,
	) {
		t.Fatalf("empty bucket: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := NewAPI(ctx, "b", ClientConfig{CredentialsFile: missing}); err == nil {
		t.Fatal("a missing credentials file must be an error")
	}
}

// testKey returns a PEM RSA key for local URL signing.
func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// TestSignedURLNeedsNoNetwork signs locally with a configured key: nothing
// listens on the endpoint, so a request would fail the test.
func TestSignedURLNeedsNoNetwork(t *testing.T) {
	ctx := context.Background()
	api, err := NewAPI(ctx, "bucket", ClientConfig{
		Endpoint:              "http://127.0.0.1:1",
		WithoutAuthentication: true,
		GoogleAccessID:        "signer@project.iam.gserviceaccount.com",
		PrivateKey:            testKey(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(api, Options{Sessions: newMemorySessions(), Presign: true})
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("ab", 32)
	raw, err := s.PresignGet(ctx, sha, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// The SDK truncates the remaining seconds, so a minute signs as 59 or 60.
	expires, _ := strconv.Atoi(q.Get("X-Goog-Expires"))
	if q.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || expires < 59 || expires > 60 ||
		!strings.HasPrefix(q.Get("X-Goog-Credential"), "signer@project.iam.gserviceaccount.com/") ||
		q.Get("X-Goog-Signature") == "" {
		t.Fatalf("signed URL query: %v", q)
	}
	if !strings.HasSuffix(u.Path, "/bucket/sha256/ab/"+sha) {
		t.Fatalf("signed URL path %q", u.Path)
	}
}

func TestTranslate(t *testing.T) {
	if translate(nil) != nil {
		t.Fatal("nil stays nil")
	}
	cases := []struct {
		err  error
		want error
	}{
		{storage.ErrObjectNotExist, blob.ErrNotFound},
		{&googleapi.Error{Code: http.StatusNotFound}, blob.ErrNotFound},
		{
			fmt.Errorf("wrapped: %w", &googleapi.Error{Code: http.StatusPreconditionFailed}),
			ErrPrecondition,
		},
	}
	for _, tc := range cases {
		if got := translate(tc.err); !errors.Is(got, tc.want) {
			t.Errorf("translate(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	for _, other := range []error{&googleapi.Error{Code: http.StatusInternalServerError}, errInjected} {
		got := translate(other)
		if errors.Is(got, blob.ErrNotFound) || errors.Is(got, ErrPrecondition) ||
			!errors.Is(got, other) {
			t.Errorf("translate(%v) = %v", other, got)
		}
	}
}
