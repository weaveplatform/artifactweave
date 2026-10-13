package s3

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// minioImage is pinned by digest so a MinIO release cannot change S3
// behaviour under the suite; bump it deliberately. MinIO stopped publishing
// minio/minio on Docker Hub and its quay.io images need credentials, so the
// suite uses Chainguard's public build (the server binary, entrypoint
// minio).
const minioImage = "cgr.dev/chainguard/minio@sha256:dc455cdd4db6e390b1b26514f15ff37681a777bf8dab41aac445a0259f751e56"

// TestContractOnMinIO runs the contract against a real S3 implementation with
// S3's real 5 MiB part floor, so multipart, copy and presigned GETs are
// exercised end to end. It needs Docker and skips without it (decision
// record 0015); CI runs it on Linux.
func TestContractOnMinIO(t *testing.T) {
	containertest.RequireLinux(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, minioImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	endpoint, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, presign, err := NewClient(ctx, ClientConfig{
		Endpoint: "http://" + endpoint, Region: "us-east-1", PathStyle: true,
		AccessKey: ctr.Username, SecretKey: ctr.Password,
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
	url, err := s.PresignGet(ctx, sha, 60e9)
	if err != nil || url == "" {
		t.Fatalf("presign needs no network: %q %v", url, err)
	}
	// Default credential chain path: no static keys.
	if _, _, err := NewClient(ctx, ClientConfig{Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
}
