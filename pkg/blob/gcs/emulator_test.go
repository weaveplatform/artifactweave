package gcs

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/fakegcsserver"
	"google.golang.org/api/option"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// fakeGCSImage is pinned by digest so an emulator release cannot change
// behaviour under the suite; bump it deliberately.
const fakeGCSImage = "fsouza/fake-gcs-server:1.56.1@sha256:797ce226d62f947c009dc40246b30cfb456b8473d8241407f9d6f2c04e4d69ef"

func newMemorySessions() session.Store { return session.NewMemory() }

// TestContractOnEmulator runs the contract through the SDK adapter against
// fake-gcs-server, which checks the wiring: requests, preconditions on
// writes, compose, ranged JSON reads and listings. fake-gcs-server ignores
// compose preconditions and DeleteSourceObjects (research 18), so those
// semantics are proven by the fake in fake_test.go; the store does not rely
// on DeleteSourceObjects anyway, because it removes what is left under the
// session's prefix after every commit. It does not validate signatures, so
// the presign case signs but does not fetch.
func TestContractOnEmulator(t *testing.T) {
	containertest.RequireLinux(t)
	ctx := context.Background()
	ctr, err := fakegcsserver.Run(ctx, fakeGCSImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start fake-gcs-server: %v", err)
	}
	endpoint, err := ctr.StorageURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := storage.NewClient(ctx, option.WithoutAuthentication(),
		option.WithEndpoint(normaliseEndpoint(endpoint)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	key := testKey(t)
	swap(t, &minPartSize, testPart)
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		bucket := "contract-" + strconv.FormatInt(n.Add(1), 10)
		if err := admin.Bucket(bucket).Create(ctx, "artifactweave", nil); err != nil {
			t.Fatal(err)
		}
		api, err := NewAPI(ctx, bucket, ClientConfig{
			Endpoint: endpoint, WithoutAuthentication: true,
			GoogleAccessID: "signer@project.iam.gserviceaccount.com", PrivateKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(
			api,
			Options{
				Prefix:   "art",
				PartSize: testPart,
				Sessions: session.NewMemory(),
				Presign:  true,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 5*testPart + 1234})
}
