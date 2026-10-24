package azure

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// TestAccContractOnAzurite runs the contract against Azurite through the real
// SDK adapter, with small parts and copy ranges so blocks, the temporary
// commit and the parallel Put Block From URL copy all run, and fetches
// presigned URLs over HTTP. Azurite does not enforce several Azure rules (no
// limits, no copy-source check for Copy Blob From URL), so the fake carries
// those; this run checks the wiring.
func TestAccContractOnAzurite(t *testing.T) {
	az := containertest.Azurite(t)
	ctx := context.Background()
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		name := "contract-" + strconv.FormatInt(n.Add(1), 10)
		cred, err := container.NewSharedKeyCredential(az.Account, az.Key)
		if err != nil {
			t.Fatal(err)
		}
		cc, err := container.NewClientWithSharedKeyCredential(az.ServiceURL+name, cred, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cc.Create(ctx, nil); err != nil {
			t.Fatal(err)
		}
		api, err := NewAPI(ClientConfig{
			ServiceURL: az.ServiceURL, Container: name,
			Account: az.Account, AccountKey: az.Key,
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(api, Options{
			Prefix: "art", PartSize: 256 << 10, CopyRangeSize: 300 << 10, Concurrency: 4,
			Sessions: session.NewMemory(), Presign: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 3*(256<<10) + 1234, FetchPresigned: true})
	t.Run("front door", func(t *testing.T) { frontDoorOnAzurite(t, az) })
}

// frontDoorOnAzurite serves CDN URLs through cdntest's Front Door edge with
// Azurite as its origin: the edge passes the SAS through and Azurite checks
// it, applies the rscd and rsct overrides and refuses a missing, tampered or
// expired SAS. Azurite cannot model the route's cache cap or Private Link;
// the azure/cdn scenario (#134) covers those against the real service.
func frontDoorOnAzurite(t *testing.T, az containertest.AzuriteEndpoint) {
	ctx := context.Background()
	const name = "frontdoor"
	cred, err := container.NewSharedKeyCredential(az.Account, az.Key)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := container.NewClientWithSharedKeyCredential(az.ServiceURL+name, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.Create(ctx, nil); err != nil {
		t.Fatal(err)
	}
	edge, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{
		Origin: strings.TrimSuffix(az.ServiceURL, "/"), CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edge.Close() })
	api, err := NewAPI(ClientConfig{
		ServiceURL: az.ServiceURL, Container: name, Account: az.Account, AccountKey: az.Key,
	})
	if err != nil {
		t.Fatal(err)
	}
	door, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{Endpoint: edge.URL})
	if err != nil {
		t.Fatal(err)
	}
	var clock atomic.Pointer[time.Time]
	now := time.Now()
	clock.Store(&now)
	s, err := New(api, Options{
		Prefix: "art", Sessions: session.NewMemory(), Presign: true, FrontDoor: door,
		Now: func() time.Time { return *clock.Load() },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.Repeat("front door through azurite ", 100))
	_, info := put(t, s, body)
	o := blob.SignOptions{
		Via: blob.ViaCDN, TTL: 5 * time.Minute, Filename: "fd file.bin", ContentType: "text/plain",
	}
	signed, err := s.SignURL(ctx, info.Digests.SHA256, o)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.SignURL(ctx, info.Digests.SHA256, o); again != signed {
		t.Fatal("two signings in one window differ")
	}
	resp, got := fetch(t, signed, "")
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) ||
		resp.Header.Get("Content-Disposition") != blob.Disposition("fd file.bin") ||
		resp.Header.Get("Content-Type") != "text/plain" ||
		resp.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("GET through the edge = %d %v", resp.StatusCode, resp.Header)
	}
	if resp, got := fetch(t, signed, "bytes=0-9"); resp.StatusCode != http.StatusPartialContent ||
		string(got) != string(body[:10]) {
		t.Fatalf("ranged GET through the edge = %d %q", resp.StatusCode, got)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := *u
	unsigned.RawQuery = ""
	if resp, _ := fetch(t, unsigned.String(), ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no SAS = %d, want 403", resp.StatusCode)
	}
	q := u.Query()
	q.Set("sig", "AAAA"+q.Get("sig")[4:])
	tampered := *u
	tampered.RawQuery = q.Encode()
	if resp, _ := fetch(t, tampered.String(), ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered SAS = %d, want 403", resp.StatusCode)
	}
	past := time.Now().Add(-time.Hour)
	clock.Store(&past)
	expired, err := s.SignURL(ctx, info.Digests.SHA256, o)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := fetch(t, expired, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expired SAS = %d, want 403", resp.StatusCode)
	}
}

func fetch(t *testing.T, u, rng string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, got
}
