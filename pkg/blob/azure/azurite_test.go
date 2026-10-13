package azure

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// azuriteImage is pinned by digest so an Azurite release cannot change
// behaviour under the suite; bump it deliberately. Azurite 3.37.0 is the
// first release with Put Block From URL. Its baseline API version is older
// than the one the SDK sends, hence --skipApiVersionCheck (research 18).
const azuriteImage = "mcr.microsoft.com/azure-storage/azurite@sha256:" +
	"830430c1da1a2d537e08f3e6764dd1f5ae00cf0346bcaf625b968ec3f0971fd5"

// TestContractOnAzurite runs the contract against Azurite through the real
// SDK adapter, with small parts and copy ranges so blocks, the temporary
// commit and the parallel Put Block From URL copy all run, and fetches
// presigned URLs over HTTP. Azurite does not enforce several Azure rules (no
// limits, no copy-source check for Copy Blob From URL), so the fake carries
// those; this run checks the wiring.
func TestContractOnAzurite(t *testing.T) {
	containertest.RequireLinux(t)
	ctx := context.Background()
	ctr, err := azurite.Run(ctx, azuriteImage,
		azurite.WithEnabledServices(azurite.BlobService),
		testcontainers.WithCmdArgs("--skipApiVersionCheck"),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start azurite: %v", err)
	}
	endpoint, err := ctr.BlobServiceURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The SDK treats a non-IP host as virtual-hosted style; Azurite is path
	// style, so address it by IP.
	serviceURL := strings.Replace(
		endpoint,
		"localhost",
		"127.0.0.1",
		1,
	) + "/" + azurite.AccountName + "/"
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		name := "contract-" + strconv.FormatInt(n.Add(1), 10)
		cred, err := container.NewSharedKeyCredential(azurite.AccountName, azurite.AccountKey)
		if err != nil {
			t.Fatal(err)
		}
		cc, err := container.NewClientWithSharedKeyCredential(serviceURL+name, cred, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cc.Create(ctx, nil); err != nil {
			t.Fatal(err)
		}
		api, err := NewAPI(ClientConfig{
			ServiceURL: serviceURL, Container: name,
			Account: azurite.AccountName, AccountKey: azurite.AccountKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(api, Options{
			Prefix: "art", PartSize: 256 << 10, CopyRangeSize: 300 << 10, Concurrency: 4,
			Sessions: session.NewMemory(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 3*(256<<10) + 1234, FetchPresigned: true})
}
