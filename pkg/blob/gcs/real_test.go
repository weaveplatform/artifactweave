package gcs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
)

// The real-cloud conformance run (issue #12) is opt-in: it needs a bucket
// and credentials, so it runs only from the manually dispatched
// cloud-conformance workflow or by hand. Credentials are Application Default
// Credentials: Workload Identity Federation in the workflow, an impersonated
// service account locally (deploy/platform/terraform/gcp/single-bucket), or a key where an
// organisation still allows them.
const (
	envRealBucket = "ARTIFACTWEAVE_TEST_GCS_BUCKET"
	// envRealAccessID names the service account that signs URLs; needed
	// when the credentials carry no private key.
	envRealAccessID = "ARTIFACTWEAVE_TEST_GCS_ACCESS_ID"
	// realPartSize is the backend's smallest part size, so the contract's
	// large blob folds (more than 32 parts) without uploading gigabytes.
	realPartSize = 1 << 20
)

// realBucket returns the configured bucket and a prefix unique to this run,
// and removes everything under the prefix when the test ends.
func realBucket(t *testing.T) (*storage.Client, string, string) {
	t.Helper()
	bucket := os.Getenv(envRealBucket)
	if bucket == "" {
		t.Skipf("%s is not set; the real-cloud run is opt-in", envRealBucket)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx, storage.WithJSONReads())
	if err != nil {
		t.Fatalf("storage client: %v", err)
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	prefix := "conformance/" + hex.EncodeToString(id[:]) + "/"
	t.Cleanup(func() {
		defer func() { _ = client.Close() }()
		it := client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				return
			}
			if err != nil {
				t.Logf("cleanup list %s: %v", prefix, err)
				return
			}
			_ = client.Bucket(bucket).Object(attrs.Name).Delete(ctx)
		}
	})
	return client, bucket, prefix
}

// TestAccContractOnRealGCS runs the blob contract against a real bucket,
// including signed URLs fetched over HTTPS, which no emulator validates.
func TestAccContractOnRealGCS(t *testing.T) {
	_, bucket, prefix := realBucket(t)
	ctx := context.Background()
	swap(t, &minPartSize, realPartSize)
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		api, err := NewAPI(ctx, bucket, ClientConfig{
			// Application Default Credentials: a key file, an impersonated
			// credential or Workload Identity Federation alike. Keys may be
			// forbidden by organisation policy, so signing names the account
			// and goes through IAM signBlob when no key is present.
			GoogleAccessID: os.Getenv(envRealAccessID),
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(api, Options{
			Prefix:   prefix + strconv.FormatInt(n.Add(1), 10),
			PartSize: realPartSize,
			Sessions: session.NewMemory(),
			Presign:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 34*realPartSize + 123, FetchPresigned: true})
}

// TestAccRealGCSProbes answers research 18's open GCS questions and logs each
// answer as a PROBE line for the workflow summary. A probe fails the test
// only when the request itself could not be made.
func TestAccRealGCSProbes(t *testing.T) {
	client, bucket, prefix := realBucket(t)
	ctx := context.Background()
	b := client.Bucket(bucket)
	write := func(name string, data []byte) *storage.ObjectAttrs {
		t.Helper()
		w := b.Object(prefix + name).NewWriter(ctx)
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return w.Attrs()
	}
	a := write("a", []byte("first part "))
	c := write("b", []byte("second part"))

	// Does compose reject a CRC32C that does not match the result? The docs
	// say compose ignores a supplied CRC32C; the SDK says it is rejected.
	comp := b.Object(prefix+"crc").ComposerFrom(b.Object(a.Name), b.Object(c.Name))
	comp.CRC32C, comp.SendCRC32C = crc32.Checksum([]byte("wrong"), castagnoli), true
	_, err := comp.Run(ctx)
	t.Logf("PROBE gcs.compose.wrong_crc32c rejected=%v error=%v", err != nil, err)

	// Is a source generation precondition enforced?
	stale := b.Object(a.Name).Generation(a.Generation + 1)
	_, err = b.Object(prefix+"gen").ComposerFrom(stale, b.Object(c.Name)).Run(ctx)
	t.Logf("PROBE gcs.compose.stale_source_generation rejected=%v error=%v", err != nil, err)

	// Does a destination must-not-exist precondition return 412 on compose?
	_ = write("dst", []byte("exists"))
	_, err = b.Object(prefix+"dst").If(storage.Conditions{DoesNotExist: true}).
		ComposerFrom(b.Object(a.Name), b.Object(c.Name)).Run(ctx)
	t.Logf("PROBE gcs.compose.destination_exists precondition_failed=%v error=%v",
		isPrecondition(err), err)

	// Can these credentials sign a URL (a key, or IAM signBlob)?
	url, err := b.SignedURL(prefix+"a", &storage.SignedURLOptions{
		GoogleAccessID: os.Getenv(envRealAccessID),
		Method:         "GET", Scheme: storage.SigningSchemeV4, Expires: nowPlusMinute(),
	})
	t.Logf("PROBE gcs.signed_url ok=%v error=%v", err == nil && url != "", err)

	// A composite of the two parts reads back as their concatenation.
	out, err := b.Object(prefix+"concat").ComposerFrom(b.Object(a.Name), b.Object(c.Name)).Run(ctx)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	r, err := b.Object(out.Name).NewReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(r)
	_ = r.Close()
	t.Logf("PROBE gcs.compose.md5_present=%v component_count=%d",
		len(out.MD5) > 0, out.ComponentCount)
	if got.String() != "first part second part" {
		t.Fatalf("composite = %q", got.String())
	}
}

// isPrecondition reports a 412 from the SDK.
func isPrecondition(err error) bool { return errors.Is(translate(err), ErrPrecondition) }

// nowPlusMinute is a short signed-URL expiry for the probe.
func nowPlusMinute() time.Time { return time.Now().Add(time.Minute) }
