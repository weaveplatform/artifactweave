package azure

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	azblobpkg "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
)

// The real-cloud conformance run (issue #12) is opt-in: it needs a storage
// account and a container, so it runs only from the manually dispatched
// cloud-conformance workflow or by hand. Without an account key the backend
// authenticates with DefaultAzureCredential.
const (
	envRealAccount   = "ARTIFACTWEAVE_TEST_AZURE_ACCOUNT"
	envRealContainer = "ARTIFACTWEAVE_TEST_AZURE_CONTAINER"
	envRealKey       = "ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY"
)

type realAccount struct {
	account, key, container, prefix string
	cc                              *container.Client
}

// realContainer returns the configured account and a prefix unique to this
// run, and removes everything under the prefix (uncommitted blobs included)
// when the test ends.
func realContainer(t *testing.T) realAccount {
	t.Helper()
	ra := realAccount{
		account:   os.Getenv(envRealAccount),
		container: os.Getenv(envRealContainer),
		key:       os.Getenv(envRealKey),
	}
	if ra.account == "" || ra.container == "" {
		t.Skipf(
			"%s and %s are not set; the real-cloud run is opt-in",
			envRealAccount,
			envRealContainer,
		)
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	ra.prefix = "conformance/" + hex.EncodeToString(id[:]) + "/"
	containerURL := "https://" + ra.account + ".blob.core.windows.net/" + ra.container
	var err error
	if ra.key != "" {
		cred, kerr := container.NewSharedKeyCredential(ra.account, ra.key)
		if kerr != nil {
			t.Fatal(kerr)
		}
		ra.cc, err = container.NewClientWithSharedKeyCredential(containerURL, cred, nil)
	} else {
		cred, cerr := entraCredential("")
		if cerr != nil {
			t.Fatal(cerr)
		}
		ra.cc, err = container.NewClient(containerURL, cred, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		pager := ra.cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
			Prefix:  &ra.prefix,
			Include: container.ListBlobsInclude{UncommittedBlobs: true},
		})
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				t.Logf("cleanup list %s: %v", ra.prefix, err)
				return
			}
			for _, item := range page.Segment.BlobItems {
				_, _ = ra.cc.NewBlobClient(*item.Name).Delete(ctx, nil)
			}
		}
	})
	return ra
}

// TestContractOnRealAzure runs the blob contract against a real container:
// the Put Block From URL copy with real copy-source authorisation and SAS
// URLs fetched over HTTPS, which Azurite does not check.
func TestContractOnRealAzure(t *testing.T) {
	ra := realContainer(t)
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		api, err := NewAPI(ClientConfig{
			Account: ra.account, AccountKey: ra.key, Container: ra.container,
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(api, Options{
			Prefix: ra.prefix + strconv.FormatInt(n.Add(1), 10),
			// Small parts and copy ranges so the large blob takes several
			// blocks and several server-side copy calls.
			PartSize: 1 << 20, CopyRangeSize: 3 << 20, Concurrency: 4,
			Sessions: session.NewMemory(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 10<<20 + 123, FetchPresigned: true})
}

// TestRealAzureProbes answers research 18's open Azure questions and logs
// each answer as a PROBE line for the workflow summary. They need the
// account key, because a copy source must carry a SAS.
func TestRealAzureProbes(t *testing.T) {
	ra := realContainer(t)
	if ra.key == "" {
		t.Skipf("%s is not set; the probes sign copy sources with the account key", envRealKey)
	}
	ctx := context.Background()
	cred, err := container.NewSharedKeyCredential(ra.account, ra.key)
	if err != nil {
		t.Fatal(err)
	}
	signed := func(name string) string {
		qp, err := sas.BlobSignatureValues{
			Protocol:      sas.ProtocolHTTPS,
			StartTime:     time.Now().Add(-15 * time.Minute),
			ExpiryTime:    time.Now().Add(time.Hour),
			Permissions:   (&sas.BlobPermissions{Read: true}).String(),
			ContainerName: ra.container,
			BlobName:      name,
		}.SignWithSharedKey(cred)
		if err != nil {
			t.Fatal(err)
		}
		return ra.cc.NewBlobClient(name).URL() + "?" + qp.Encode()
	}
	upload := func(name string, data []byte) {
		t.Helper()
		if _, err := ra.cc.NewBlockBlobClient(name).Upload(ctx, body(data), nil); err != nil {
			t.Fatalf("upload %s: %v", name, err)
		}
	}
	id := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	// Research 18: Put Block From URL's maximum range is stated as both
	// 100 MiB and 4,000 MiB. Copy a 101 MiB range.
	big := ra.prefix + "big"
	upload(big, bytes.Repeat([]byte("a"), 101<<20))
	_, err = ra.cc.NewBlockBlobClient(ra.prefix+"range").
		StageBlockFromURL(ctx, id("r-000001"), signed(big),
			&blockblob.StageBlockFromURLOptions{
				Range: azblobpkg.HTTPRange{Offset: 0, Count: 101 << 20},
			})
	t.Logf(
		"PROBE azure.put_block_from_url.101MiB_range accepted=%v error=%v",
		err == nil,
		short(err),
	)

	// Research 18: does Copy Blob From URL accept Shared Key authorisation of
	// a same-account source, with no SAS on the source?
	small := ra.prefix + "small"
	upload(small, []byte("copy me"))
	_, err = ra.cc.NewBlobClient(ra.prefix+"copied").
		CopyFromURL(ctx, ra.cc.NewBlobClient(small).URL(), nil)
	t.Logf(
		"PROBE azure.copy_from_url.shared_key_source_without_sas accepted=%v error=%v",
		err == nil,
		short(err),
	)

	// Research 18: does Put Blob with If-None-Match: * succeed on a name that
	// holds only uncommitted blocks? Azurite answers 409.
	staged := ra.prefix + "staged"
	if _, err := ra.cc.NewBlockBlobClient(staged).
		StageBlock(ctx, id("s-000001"), body([]byte("x")), nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	_, err = ra.cc.NewBlockBlobClient(staged).
		Upload(ctx, body([]byte("y")), &blockblob.UploadOptions{
			AccessConditions: &azblobpkg.AccessConditions{
				ModifiedAccessConditions: &azblobpkg.ModifiedAccessConditions{
					IfNoneMatch: to.Ptr(azcore.ETagAny),
				},
			},
		})
	t.Logf(
		"PROBE azure.put_blob_if_none_match_over_uncommitted accepted=%v error=%v",
		err == nil,
		short(err),
	)
}

// short keeps a probe line readable: the service error code, not the whole
// response dump.
func short(err error) string {
	if err == nil {
		return "none"
	}
	s := err.Error()
	if i := strings.Index(s, "ERROR CODE:"); i >= 0 {
		line, _, _ := strings.Cut(s[i:], "\n")
		return strings.TrimSpace(line)
	}
	line, _, _ := strings.Cut(s, "\n")
	return line
}
