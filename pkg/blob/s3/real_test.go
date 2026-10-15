package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
)

// The real-cloud conformance run (issue #128) is opt-in: it needs a bucket
// and credentials, so it runs only from the manually dispatched
// cloud-conformance workflow or by hand. Credentials come from the AWS
// default chain: the role the workflow assumes through GitHub's OIDC token,
// or your own `aws login` session locally
// (deploy/platform/terraform/aws/single-bucket).
const (
	envRealBucket = "ARTIFACTWEAVE_TEST_S3_BUCKET"
	envRealRegion = "ARTIFACTWEAVE_TEST_S3_REGION"
	// realPartSize is S3's floor for every part but the last, so the
	// contract's large blob is multipart without uploading much.
	realPartSize = 5 << 20
)

// realBucket returns a client for the configured bucket and a prefix unique
// to this run, and removes everything under the prefix when the test ends.
func realBucket(t *testing.T) (*s3.Client, *s3.PresignClient, string, string) {
	t.Helper()
	bucket := os.Getenv(envRealBucket)
	if bucket == "" {
		t.Skipf("%s is not set; the real-cloud run is opt-in", envRealBucket)
	}
	region := os.Getenv(envRealRegion)
	if region == "" {
		region = "eu-west-2"
	}
	ctx := context.Background()
	client, presign, err := NewClient(ctx, ClientConfig{Region: region})
	if err != nil {
		t.Fatalf("s3 client: %v", err)
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	prefix := "conformance/" + hex.EncodeToString(id[:]) + "/"
	t.Cleanup(func() {
		p := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
			Bucket: aws.String(bucket), Prefix: aws.String(prefix),
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				t.Logf("cleanup list %s: %v", prefix, err)
				return
			}
			for _, o := range page.Contents {
				_, _ = client.DeleteObject(
					ctx,
					&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key},
				)
			}
		}
		// Abandoned multipart uploads are billed until aborted; the bucket's
		// lifecycle rule is only the backstop.
		mp := s3.NewListMultipartUploadsPaginator(client, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), Prefix: aws.String(prefix),
		})
		for mp.HasMorePages() {
			page, err := mp.NextPage(ctx)
			if err != nil {
				return
			}
			for _, u := range page.Uploads {
				_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
					Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId,
				})
			}
		}
	})
	return client, presign, bucket, prefix
}

// TestContractOnRealS3 runs the blob contract against a real bucket,
// including presigned URLs fetched over HTTPS. The copy limits are lowered
// so committing the large blob takes the UploadPartCopy path that real
// objects over 5 GiB take, without uploading gigabytes; MinIO accepts copies
// S3 refuses, so only this run checks that path.
func TestContractOnRealS3(t *testing.T) {
	client, presign, bucket, prefix := realBucket(t)
	swap(t, &copyLimit, 6<<20)
	swap(t, &copyChunk, realPartSize)
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		s, err := New(client, Options{
			Bucket:   bucket,
			Prefix:   prefix + strconv.FormatInt(n.Add(1), 10),
			PartSize: realPartSize,
			Sessions: session.NewMemory(),
			Presign:  presign,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 2*realPartSize + 1234, FetchPresigned: true})
}

// TestRealS3Probes answers questions about S3 that MinIO cannot, and logs
// each answer as a PROBE line for the workflow summary. A probe fails the
// test only when the request itself could not be made.
func TestRealS3Probes(t *testing.T) {
	client, presign, bucket, prefix := realBucket(t)
	ctx := context.Background()
	key := func(name string) *string { return aws.String(prefix + name) }
	put := func(name string, data []byte) {
		t.Helper()
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: key(name), Body: bytes.NewReader(data),
		}); err != nil {
			t.Fatalf("put %s: %v", name, err)
		}
	}
	put("exists", []byte("already here"))

	// Conditional writes: would a commit that must not overwrite get 412
	// from S3 itself, as GCS's DoesNotExist and Azure's If-None-Match do?
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: key("exists"), Body: bytes.NewReader([]byte("again")),
		IfNoneMatch: aws.String("*"),
	})
	t.Logf(
		"PROBE s3.put.if_none_match_existing precondition_failed=%v error=%v",
		isPrecondition(err),
		err,
	)

	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(bucket), Key: key("exists"),
		CopySource:  aws.String(bucket + "/" + prefix + "exists"),
		IfNoneMatch: aws.String("*"),
	})
	t.Logf(
		"PROBE s3.copy.if_none_match_existing precondition_failed=%v error=%v",
		isPrecondition(err),
		err,
	)

	// The same check on completing a multipart upload onto an existing key.
	mpu, err := client.CreateMultipartUpload(
		ctx,
		&s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: key("exists")},
	)
	if err != nil {
		t.Fatalf("create multipart: %v", err)
	}
	part, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: key("exists"), UploadId: mpu.UploadId,
		PartNumber: aws.Int32(1), Body: bytes.NewReader([]byte("only part")),
	})
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: key("exists"), UploadId: mpu.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{
			{ETag: part.ETag, PartNumber: aws.Int32(1)},
		}},
		IfNoneMatch: aws.String("*"),
	})
	t.Logf(
		"PROBE s3.complete_multipart.if_none_match_existing precondition_failed=%v error=%v",
		isPrecondition(err),
		err,
	)
	_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: key("exists"), UploadId: mpu.UploadId,
	})

	// Default integrity protection: which checksum does an object written
	// with the SDK's defaults carry, and of which type?
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: key("exists"), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	t.Logf("PROBE s3.default_checksum type=%s crc32=%v crc64nvme=%v",
		head.ChecksumType, head.ChecksumCRC32 != nil, head.ChecksumCRC64NVME != nil)

	// A URL presigned for GET: does S3 refuse it for HEAD? The JFrog CLI
	// sends a HEAD before a split download and follows redirects (#117).
	signed, err := presign.PresignGetObject(
		ctx,
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: key("exists")},
		func(o *s3.PresignOptions) { o.Expires = time.Minute },
	)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	headStatus := httpStatus(ctx, t, http.MethodHead, signed.URL)
	getStatus := httpStatus(ctx, t, http.MethodGet, signed.URL)
	t.Logf("PROBE s3.presigned_get head_status=%d get_status=%d", headStatus, getStatus)
}

// httpStatus sends one request to url and returns its status.
func httpStatus(ctx context.Context, t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// isPrecondition reports S3's 412 PreconditionFailed.
func isPrecondition(err error) bool {
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && status.HTTPStatusCode() == http.StatusPreconditionFailed {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed"
}
