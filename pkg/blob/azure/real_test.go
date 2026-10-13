package azure

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	// envRealLargeRanges turns on the Put Block From URL probes beyond
	// 101 MiB. Off by default: they stage about 4 GiB in the account.
	envRealLargeRanges = "ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES"
)

type realAccount struct {
	account, key, container, prefix string
	cc                              *container.Client
	// cred is the Entra ID credential when no account key is set; the
	// probes sign copy sources with a user delegation SAS from it.
	cred azcore.TokenCredential
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
		ra.cred = cred
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
// each answer as a PROBE line for the workflow summary. A copy source must
// carry a SAS: signed with the account key when one is set, otherwise with
// a user delegation key from the Entra ID credential, so the probes also run
// against accounts that allow no Shared Key.
func TestRealAzureProbes(t *testing.T) {
	ra := realContainer(t)
	ctx := context.Background()
	signed := realSigner(ctx, t, ra)
	var err error
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
	// A 100 MiB range from the same source and SAS first, as a control: it
	// tells a size limit apart from a source the service cannot verify.
	_, err = ra.cc.NewBlockBlobClient(ra.prefix+"range").
		StageBlockFromURL(ctx, id("r-000000"), signed(big),
			&blockblob.StageBlockFromURLOptions{
				Range: azblobpkg.HTTPRange{Offset: 0, Count: 100 << 20},
			})
	t.Logf(
		"PROBE azure.put_block_from_url.100MiB_range accepted=%v error=%v",
		err == nil,
		short(err),
	)
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
	largeRangeProbes(ctx, t, ra, signed, big)

	// Research 18: does Copy Blob From URL accept Shared Key authorisation of
	// a same-account source, with no SAS on the source? Only an account that
	// allows Shared Key can answer.
	if ra.key == "" {
		t.Logf("PROBE azure.copy_from_url.shared_key_source_without_sas skipped=true " +
			"error=the account allows no Shared Key")
	} else {
		small := ra.prefix + "small"
		upload(small, []byte("copy me"))
		_, err = ra.cc.NewBlobClient(ra.prefix+"copied").
			CopyFromURL(ctx, ra.cc.NewBlobClient(small).URL(), nil)
		t.Logf(
			"PROBE azure.copy_from_url.shared_key_source_without_sas accepted=%v error=%v",
			err == nil,
			short(err),
		)
	}

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

// largeRangeProbes copies ever larger single ranges with Put Block From URL,
// up to the documented 4,000 MiB and one MiB past it as a control. The
// source is assembled server-side from 100 MiB ranges of big, so the client
// uploads nothing more.
func largeRangeProbes(
	ctx context.Context,
	t *testing.T,
	ra realAccount,
	signed func(string) string,
	big string,
) {
	t.Helper()
	if os.Getenv(envRealLargeRanges) == "" {
		t.Logf("PROBE azure.put_block_from_url.large_ranges skipped=true error=%s is not set",
			envRealLargeRanges)
		return
	}
	huge := ra.prefix + "huge"
	const parts, part = 41, 100 << 20 // 4,100 MiB
	ids := make([]string, parts)
	sem := make(chan struct{}, 8)
	errs := make(chan error, parts)
	for i := range parts {
		ids[i] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("h-%06d", i)))
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			_, err := ra.cc.NewBlockBlobClient(huge).StageBlockFromURL(ctx, ids[i], signed(big),
				&blockblob.StageBlockFromURLOptions{
					Range: azblobpkg.HTTPRange{Offset: 0, Count: part},
				})
			errs <- err
		}()
	}
	for range parts {
		if err := <-errs; err != nil {
			t.Fatalf("assemble the large source: %v", err)
		}
	}
	if _, err := ra.cc.NewBlockBlobClient(huge).CommitBlockList(ctx, ids, nil); err != nil {
		t.Fatalf("commit the large source: %v", err)
	}
	for _, mib := range []int64{256, 1024, 4000, 4001} {
		start := time.Now()
		_, err := ra.cc.NewBlockBlobClient(ra.prefix+"range-large").StageBlockFromURL(ctx,
			base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("l-%06d", mib))), signed(huge),
			&blockblob.StageBlockFromURLOptions{
				Range: azblobpkg.HTTPRange{Offset: 0, Count: mib << 20},
			})
		t.Logf("PROBE azure.put_block_from_url.%dMiB_range accepted=%v error=%v seconds=%.1f",
			mib, err == nil, short(err), time.Since(start).Seconds())
	}
}

// realSigner returns a function that adds a one-hour read SAS to a blob's
// URL, signed the way the backend presigns: a service SAS from the account
// key, or a user delegation SAS from the Entra ID credential (Storage Blob
// Data Contributor includes the generateUserDelegationKey action).
func realSigner(ctx context.Context, t *testing.T, ra realAccount) func(string) string {
	t.Helper()
	api, err := NewAPI(ClientConfig{
		Account: ra.account, AccountKey: ra.key, Container: ra.container, Credential: ra.cred,
	})
	if err != nil {
		t.Fatal(err)
	}
	return func(name string) string {
		u, err := api.SignRead(ctx, name, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
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
