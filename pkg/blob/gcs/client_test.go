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
	"os"
	"path/filepath"
	"runtime"
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
	raw, err := s.SignURL(ctx, sha, blob.SignOptions{
		TTL: time.Minute, Filename: "a.tgz", ContentType: "application/x-gzip",
	})
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
		q.Get("X-Goog-Signature") == "" ||
		q.Get("response-content-disposition") != blob.Disposition("a.tgz") ||
		q.Get("response-content-type") != "application/x-gzip" {
		t.Fatalf("signed URL query: %v", q)
	}
	if !strings.HasSuffix(u.Path, "/bucket/sha256/ab/"+sha) {
		t.Fatalf("signed URL path %q", u.Path)
	}
}

func TestCanSign(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// The environment and home directory are the test's own, so neither the
	// developer's gcloud login nor the runner's decides the outcome.
	home := filepath.Join(dir, "home")
	env := map[string]string{"APPDATA": filepath.Join(dir, "appdata")}
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	userHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { lookupEnv, userHome = os.LookupEnv, os.UserHomeDir })

	for _, c := range []struct {
		name string
		cfg  ClientConfig
		adc  string // GOOGLE_APPLICATION_CREDENTIALS contents, "" for none
		want bool
	}{
		{"emulator with a key", ClientConfig{WithoutAuthentication: true, GoogleAccessID: "a", PrivateKey: []byte("k")}, "", true},
		{"emulator without a key", ClientConfig{WithoutAuthentication: true, GoogleAccessID: "a"}, "", false},
		{"named account", ClientConfig{GoogleAccessID: "a@p.iam.gserviceaccount.com"}, `{"type":"authorized_user"}`, true},
		{"service-account key file", ClientConfig{CredentialsFile: "/k.json"}, "", true},
		{"ADC service account", ClientConfig{}, `{"type":"service_account"}`, true},
		{"ADC impersonation", ClientConfig{}, `{"type":"impersonated_service_account"}`, true},
		{"ADC federation naming an account", ClientConfig{}, `{"type":"external_account","service_account_impersonation_url":"https://x"}`, true},
		{"ADC federation naming none", ClientConfig{}, `{"type":"external_account"}`, false},
		{"ADC user login", ClientConfig{}, `{"type":"authorized_user"}`, false},
		{"ADC unreadable JSON", ClientConfig{}, `{`, false},
		{"metadata server", ClientConfig{}, "", true},
	} {
		delete(env, "GOOGLE_APPLICATION_CREDENTIALS")
		if c.adc != "" {
			env["GOOGLE_APPLICATION_CREDENTIALS"] = write("adc.json", c.adc)
		}
		if got := c.cfg.CanSign(); got != c.want {
			t.Errorf("%s: CanSign = %v, want %v", c.name, got, c.want)
		}
	}

	// gcloud's well-known file is where the Google auth library looks for it:
	// under CLOUDSDK_CONFIG when set, else %APPDATA%\gcloud on Windows and
	// ~/.config/gcloud elsewhere.
	delete(env, "GOOGLE_APPLICATION_CREDENTIALS")
	const file = "application_default_credentials.json"
	want := filepath.Join(home, ".config", "gcloud", file)
	if runtime.GOOS == "windows" {
		want = filepath.Join(env["APPDATA"], "gcloud", file)
	}
	if got := wellKnownADC(); got != want {
		t.Fatalf("well-known file %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if (ClientConfig{}).CanSign() {
		t.Error("a gcloud user login cannot sign")
	}
	env["CLOUDSDK_CONFIG"] = dir
	write(file, `{"type":"service_account"}`)
	if got := wellKnownADC(); got != filepath.Join(dir, file) {
		t.Fatalf("CLOUDSDK_CONFIG's file %q", got)
	}
	if !(ClientConfig{}).CanSign() {
		t.Error("CLOUDSDK_CONFIG's service account can sign")
	}
	// No CLOUDSDK_CONFIG, no APPDATA and no home: no file to read, so the
	// metadata server is assumed.
	delete(env, "CLOUDSDK_CONFIG")
	delete(env, "APPDATA")
	userHome = func() (string, error) { return "", errInjected }
	if !(ClientConfig{}).CanSign() {
		t.Error("no home and no file falls back to the metadata server")
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
