package azure

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

func newFrontDoorStore(
	t *testing.T,
	f *fakeAzure,
	fd cdn.FrontDoorConfig,
	now func() time.Time,
) *Store {
	t.Helper()
	door, err := cdn.NewFrontDoor(fd)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(f, Options{
		Prefix: "art", PartSize: testPart, Sessions: session.NewMemory(), Presign: true,
		FrontDoor: door, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAccFrontDoorURLs checks a CDN URL is a SAS on the endpoint, fixed to its
// window: every request in one window gets the same string, so the edge's
// cache, keyed on it, serves them all, and the next window gets a new one.
func TestAccFrontDoorURLs(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC)
	now := func() time.Time { return at }
	f := newFake()
	s := newFrontDoorStore(t, f, cdn.FrontDoorConfig{Endpoint: "fd.azurefd.net"}, now)
	_, info := put(t, s, []byte("front door"))
	sha := info.Digests.SHA256
	if !s.Signs(blob.ViaCDN) || !s.Signs(blob.ViaStorage) {
		t.Fatal("a signing store with a Front Door signs both routes")
	}
	o := blob.SignOptions{
		Via: blob.ViaCDN, TTL: 5 * time.Minute, Filename: "fd.bin", ContentType: "text/plain",
	}
	first, err := s.SignURL(ctx, sha, o)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "fd.azurefd.net" ||
		u.Path != "/c/"+s.ObjectKey(sha) {
		t.Fatalf("url %s", first)
	}
	q := u.Query()
	if q.Get("st") != "2026-10-14T12:00:00Z" || q.Get("se") != "2026-10-14T12:10:00Z" ||
		q.Get("rscd") != blob.Disposition("fd.bin") || q.Get("rsct") != "text/plain" ||
		q.Get("rscc") != "public, max-age=300" {
		t.Fatalf("SAS %v", q)
	}
	at = at.Add(3 * time.Minute)
	if again, err := s.SignURL(ctx, sha, o); err != nil || again != first {
		t.Fatalf("same window gave %q, %v; want %q", again, err, first)
	}
	at = at.Add(time.Minute)
	if next, err := s.SignURL(ctx, sha, o); err != nil || next == first {
		t.Fatalf("next window gave %q, %v", next, err)
	}
	if _, err := s.SignURL(
		ctx,
		sha,
		blob.SignOptions{Via: blob.ViaCDN, TTL: time.Millisecond},
	); !errors.Is(
		err,
		cdn.ErrConfig,
	) {
		t.Fatalf("sub-second TTL: %v", err)
	}
	f.failOn("SignRead", 0, errInjected)
	if _, err := s.SignURL(ctx, sha, o); !errors.Is(err, errInjected) {
		t.Fatalf("signing failure: %v", err)
	}
}

func TestAccFrontDoorOriginPath(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newFrontDoorStore(t, f, cdn.FrontDoorConfig{
		Endpoint: "http://localhost:9", PathPrefix: "c",
	}, nil)
	_, info := put(t, s, []byte("x"))
	signed, err := s.SignURL(ctx, info.Digests.SHA256, blob.SignOptions{
		Via: blob.ViaCDN, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(signed)
	if u.Host != "localhost:9" || u.Path != "/"+s.ObjectKey(info.Digests.SHA256) {
		t.Fatalf("url %s", signed)
	}
	// An origin path outside the store's container and prefix reaches none
	// of its blobs, which is a configuration error at start.
	door, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{
		Endpoint: "fd.azurefd.net", PathPrefix: "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(
		f,
		Options{Sessions: session.NewMemory(), Presign: true, FrontDoor: door},
	); !errors.Is(
		err,
		ErrConfig,
	) {
		t.Fatalf("uncovered store: %v", err)
	}
}

// TestAccFrontDoorNeedsASigner checks a store that cannot mint a SAS, as on a
// pre-issued token, has no CDN route: Front Door has no signature of its
// own to fall back on.
func TestAccFrontDoorNeedsASigner(t *testing.T) {
	f := newFake()
	s := newFrontDoorStore(t, f, cdn.FrontDoorConfig{Endpoint: "fd.azurefd.net"}, nil)
	s.presign = false
	if s.Signs(blob.ViaCDN) {
		t.Fatal("a store that cannot sign has no CDN route")
	}
	_, info := put(t, s, []byte("y"))
	_, err := s.SignURL(context.Background(), info.Digests.SHA256, blob.SignOptions{
		Via: blob.ViaCDN, TTL: time.Minute,
	})
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatal(err)
	}
}

// TestAccServiceSASWindow checks a shared-key SAS fixed to a window is the same
// string whenever it is signed in that window.
func TestAccServiceSASWindow(t *testing.T) {
	api, err := NewAPI(ClientConfig{Account: "acct", Container: "blobs", AccountKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	a := api.(*sdkAPI) //nolint:forcetypeassert // the concrete adapter under test
	if a.Container() != "blobs" {
		t.Fatalf("container %q", a.Container())
	}
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	o := ReadSAS{TTL: 5 * time.Minute, Start: start, Expiry: start.Add(10 * time.Minute)}
	var urls []string
	for _, at := range []time.Duration{time.Minute, 4 * time.Minute} {
		a.now = func() time.Time { return start.Add(at) }
		raw, err := a.SignRead(context.Background(), "art/k", o)
		if err != nil {
			t.Fatal(err)
		}
		urls = append(urls, raw)
	}
	if urls[0] != urls[1] {
		t.Fatalf("one window, two SAS:\n%s\n%s", urls[0], urls[1])
	}
	u, _ := url.Parse(urls[0])
	if q := u.Query(); q.Get("st") != "2026-10-14T11:45:00Z" ||
		q.Get("se") != "2026-10-14T12:10:00Z" {
		t.Fatalf("a fixed start is still backdated against skew: %v", q)
	}
}

// TestAccDelegationSASWindow checks a user delegation key is kept across a
// window, so the window's URLs stay identical, and that a SAS the longest
// key could not cover is refused rather than signed to fail at storage.
func TestAccDelegationSASWindow(t *testing.T) {
	var keys atomic.Int64
	r := chi.NewRouter()
	r.Post("/", func(w http.ResponseWriter, _ *http.Request) {
		keys.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><UserDelegationKey>` +
			`<SignedOid>oid</SignedOid><SignedTid>tid</SignedTid>` +
			`<SignedStart>2026-10-14T00:00:00Z</SignedStart><SignedExpiry>2026-10-21T00:00:00Z</SignedExpiry>` +
			`<SignedService>b</SignedService><SignedVersion>2026-12-06</SignedVersion>` +
			`<Value>` + testKey + `</Value></UserDelegationKey>`))
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	opts := &container.ClientOptions{}
	opts.InsecureAllowCredentialWithHTTP = true
	opts.Retry = policy.RetryOptions{MaxRetries: -1}
	api, err := NewAPI(ClientConfig{
		ServiceURL: srv.URL, Container: "c", Credential: &staticCred{}, Options: opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := api.(*sdkAPI) //nolint:forcetypeassert // the concrete adapter under test
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	o := ReadSAS{TTL: 5 * time.Minute, Start: start, Expiry: start.Add(10 * time.Minute)}
	ctx := context.Background()
	var urls []string
	for _, at := range []time.Duration{time.Minute, 4 * time.Minute} {
		a.now = func() time.Time { return start.Add(at) }
		raw, err := a.SignRead(ctx, "k", o)
		if err != nil {
			t.Fatal(err)
		}
		urls = append(urls, raw)
	}
	if urls[0] != urls[1] || keys.Load() != 1 {
		t.Fatalf("%d keys; URLs differ: %v", keys.Load(), urls[0] != urls[1])
	}
	long := ReadSAS{TTL: 4 * 24 * time.Hour, Start: start, Expiry: start.Add(8 * 24 * time.Hour)}
	if _, err := a.SignRead(ctx, "k", long); !errors.Is(err, ErrConfig) {
		t.Fatalf("a SAS outliving any key: %v", err)
	}
}
