package s3

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// TestContractOnMinIO runs the contract against a real S3 implementation with
// S3's real 5 MiB part floor, so multipart, copy and presigned GETs are
// exercised end to end. It needs Docker and skips without it (decision
// record 0015); CI runs it on Linux.
func TestContractOnMinIO(t *testing.T) {
	minio := containertest.MinIO(t)
	ctx := context.Background()
	client, presign, err := NewClient(ctx, ClientConfig{
		Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
		AccessKey: minio.User, SecretKey: minio.Password,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	blobtest.Run(t, func(t *testing.T) blob.Store {
		bucket := "contract-" + strconv.FormatInt(n.Add(1), 10)
		if _, err := client.CreateBucket(
			ctx,
			&s3.CreateBucketInput{Bucket: aws.String(bucket)},
		); err != nil {
			t.Fatal(err)
		}
		s, err := New(
			client,
			Options{Bucket: bucket, Prefix: "art", Sessions: session.NewMemory(), Presign: presign},
		)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}, blobtest.Options{LargeSize: 2*DefaultPartSize + 1234, FetchPresigned: true})
}

func TestNewClientBuildsWithoutNetwork(t *testing.T) {
	ctx := context.Background()
	client, presign, err := NewClient(ctx, ClientConfig{
		Endpoint:  "http://127.0.0.1:1",
		Region:    "eu-west-2",
		PathStyle: true,
		AccessKey: "a",
		SecretKey: "b",
	})
	if err != nil || client == nil || presign == nil {
		t.Fatalf("%v", err)
	}
	if client.Options().Region != "eu-west-2" || !client.Options().UsePathStyle ||
		aws.ToString(client.Options().BaseEndpoint) != "http://127.0.0.1:1" {
		t.Fatalf("options not applied: %+v", client.Options())
	}
	s, err := New(client, Options{Bucket: "b", Sessions: session.NewMemory(), Presign: presign})
	if err != nil {
		t.Fatal(err)
	}
	sha := blobtest.Digest([]byte("x")).SHA256
	url, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if err != nil || url == "" {
		t.Fatalf("presign needs no network: %q %v", url, err)
	}
	// Default credential chain path: no static keys.
	if _, _, err := NewClient(ctx, ClientConfig{Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentCommitsOnMinIO commits one blob from several sessions at
// once on each write path. MinIO refuses a taken key with 412 on PutObject
// and CompleteMultipartUpload and ignores If-None-Match on CopyObject
// (probed 2026-10-16); every commit must succeed either way, and the blob
// must read back whole.
func TestConcurrentCommitsOnMinIO(t *testing.T) {
	minio := containertest.MinIO(t)
	ctx := context.Background()
	client, presign, err := NewClient(ctx, ClientConfig{
		Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
		AccessKey: minio.User, SecretKey: minio.Password,
	})
	if err != nil {
		t.Fatal(err)
	}
	big := blobtest.Content(11, DefaultPartSize+1234)
	for i, tc := range []struct {
		name    string
		body    []byte
		declare bool
	}{
		{"small put", []byte("concurrent small"), false},
		{"multipart to the content key", big, true},
		{"multipart then copy", big, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bucket := "concurrent-" + strconv.Itoa(i)
			if _, err := client.CreateBucket(
				ctx,
				&s3.CreateBucketInput{Bucket: aws.String(bucket)},
			); err != nil {
				t.Fatal(err)
			}
			s, err := New(
				client,
				Options{Bucket: bucket, Sessions: session.NewMemory(), Presign: presign},
			)
			if err != nil {
				t.Fatal(err)
			}
			want := blobtest.Digest(tc.body)
			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for range 4 {
				wg.Go(func() {
					var opts []blob.UploadOption
					if tc.declare {
						opts = append(opts, blob.WithExpectedSHA256(want.SHA256))
					}
					u, err := s.BeginUpload(ctx, opts...)
					if err == nil {
						_, err = u.Append(ctx, bytes.NewReader(tc.body))
					}
					if err == nil {
						_, err = u.Commit(ctx, blob.Digests{})
					}
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			r, _, err := s.Open(ctx, want.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, tc.body) {
				t.Fatalf(
					"read back %d bytes (%v), want the %d committed",
					len(got),
					err,
					len(tc.body),
				)
			}
		})
	}
}
