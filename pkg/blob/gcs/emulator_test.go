package gcs

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

func newMemorySessions() session.Store { return session.NewMemory() }

// TestAccContractOnEmulator runs the contract through the SDK adapter against
// fake-gcs-server, which checks the wiring: requests, preconditions on
// writes, compose, ranged JSON reads and listings. fake-gcs-server ignores
// compose preconditions and DeleteSourceObjects (research 18), so those
// semantics are proven by the fake in fake_test.go; the store does not rely
// on DeleteSourceObjects anyway, because it removes what is left under the
// session's prefix after every commit. It does not validate signatures, so
// the presign case signs but does not fetch.
func TestAccContractOnEmulator(t *testing.T) {
	endpoint := containertest.FakeGCS(t)
	ctx := context.Background()
	admin, err := storage.NewClient(ctx, option.WithoutAuthentication(),
		option.WithEndpoint(endpoint))
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
