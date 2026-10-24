package cdn_test

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

func TestUnitFrontDoorURL(t *testing.T) {
	fd, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{Endpoint: "artifacts-x1.z01.azurefd.net"})
	if err != nil {
		t.Fatal(err)
	}
	if fd.Provider() != "front_door" {
		t.Fatalf("provider %q", fd.Provider())
	}
	// The SAS travels unchanged: storage checks it over the storage path,
	// which the edge forwards as it is.
	got, err := fd.URL("blobs/art/sha256/ab/abc", "sv=2026-12-06&sig=a%2Bb")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://artifacts-x1.z01.azurefd.net/blobs/art/sha256/ab/abc?sv=2026-12-06&sig=a%2Bb"; got != want {
		t.Fatalf("url\n got %s\nwant %s", got, want)
	}
	if err := fd.Covers("blobs/art/"); err != nil {
		t.Fatalf("an endpoint on the account root covers every container: %v", err)
	}
}

func TestUnitFrontDoorOriginPath(t *testing.T) {
	fd, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{
		Endpoint: "http://localhost:8080", PathPrefix: "/blobs/",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fd.URL("blobs/art/k", "sig=x")
	if err != nil || got != "http://localhost:8080/art/k?sig=x" {
		t.Fatalf("url %q, %v", got, err)
	}
	if err := fd.Covers("blobs/art/"); err != nil {
		t.Fatal(err)
	}
	if err := fd.Covers("other/art/"); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("another container is unreachable: %v", err)
	}
	if _, err := fd.URL("other/art/k", "sig=x"); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("a path outside the origin path: %v", err)
	}
	if _, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{Endpoint: "ftp://x"}); !errors.Is(
		err,
		cdn.ErrConfig,
	) {
		t.Fatalf("bad endpoint: %v", err)
	}
}

func TestUnitWindow(t *testing.T) {
	ttl := 5 * time.Minute
	first := time.Date(2026, 10, 14, 12, 1, 0, 0, time.UTC)
	start, expiry, err := cdn.Window(first, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start %s, want %s", start, want)
	}
	if want := time.Date(2026, 10, 14, 12, 10, 0, 0, time.UTC); !expiry.Equal(want) {
		t.Fatalf("expiry %s, want %s", expiry, want)
	}
	// Anywhere in the window gives the same pair; the next window moves on.
	s2, e2, _ := cdn.Window(first.Add(3*time.Minute+59*time.Second), ttl)
	if !s2.Equal(start) || !e2.Equal(expiry) {
		t.Fatalf("same window gave %s..%s", s2, e2)
	}
	s3, _, _ := cdn.Window(first.Add(4*time.Minute), ttl)
	if !s3.Equal(start.Add(ttl)) {
		t.Fatalf("next window starts %s", s3)
	}
	if _, _, err := cdn.Window(first, time.Millisecond); !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("sub-second ttl: %v", err)
	}
}
