package gcs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// fakeCDN is a cdn.Signer that records what it signs.
type fakeCDN struct {
	covers  error
	signErr error
	key     string
	req     cdn.Request
}

func (*fakeCDN) Provider() string      { return "fake" }
func (f *fakeCDN) Covers(string) error { return f.covers }
func (f *fakeCDN) Sign(key string, r cdn.Request) (string, error) {
	f.key, f.req = key, r
	if f.signErr != nil {
		return "", f.signErr
	}
	return "https://cdn.example/" + key, nil
}

func TestUnitCDNSigning(t *testing.T) {
	ctx := context.Background()
	swap(t, &minPartSize, testPart)
	f := newFake()
	signer := &fakeCDN{}
	s, err := New(f, Options{
		Prefix: "art", PartSize: testPart, Sessions: session.NewMemory(), CDN: signer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Signs(blob.ViaCDN) || s.Signs(blob.ViaStorage) {
		t.Fatal("a store with a CDN and no Presign signs CDN URLs only")
	}
	sha := blobtest.Put(ctx, t, s, []byte("cdn")).Digests.SHA256
	u, err := s.SignURL(ctx, sha, blob.SignOptions{
		Via: blob.ViaCDN, TTL: time.Minute, Filename: "x.tgz", ContentType: "application/gzip",
	})
	if err != nil || u != "https://cdn.example/"+s.ObjectKey(sha) {
		t.Fatalf("%q %v", u, err)
	}
	if signer.req != (cdn.Request{TTL: time.Minute, Filename: "x.tgz", ContentType: "application/gzip"}) {
		t.Fatalf("request %+v", signer.req)
	}
	signer.signErr = errInjected
	_, err = s.SignURL(ctx, sha, blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute})
	if !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	_, err = New(f, Options{Sessions: session.NewMemory(), CDN: &fakeCDN{covers: cdn.ErrConfig}})
	if !errors.Is(err, ErrConfig) || !errors.Is(err, cdn.ErrConfig) {
		t.Fatalf("a CDN that cannot reach the store's keys: %v", err)
	}
}
