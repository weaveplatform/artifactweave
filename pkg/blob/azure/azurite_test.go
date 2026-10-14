package azure

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// TestContractOnAzurite runs the contract against Azurite through the real
// SDK adapter, with small parts and copy ranges so blocks, the temporary
// commit and the parallel Put Block From URL copy all run, and fetches
// presigned URLs over HTTP. Azurite does not enforce several Azure rules (no
// limits, no copy-source check for Copy Blob From URL), so the fake carries
// those; this run checks the wiring.
func TestContractOnAzurite(t *testing.T) {
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
}
