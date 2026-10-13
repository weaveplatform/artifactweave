package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/s3"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// syncBuffer is a log sink the repair goroutine and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p) //nolint:wrapcheck // test sink
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRuntimeRunsRepair(t *testing.T) {
	orig := repairEvery
	repairEvery = 10 * time.Millisecond
	t.Cleanup(func() { repairEvery = orig })
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testConfig(t)
	cfg.Blob = mirrorConfig()
	cfg.ColdBlob = config.BlobConfig{BlobBackendConfig: config.BlobBackendConfig{Backend: "fs"}}
	ctx := context.Background()
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if rt.coldBlobs == nil || len(rt.mirrors) != 1 {
		t.Fatalf("runtime %+v", rt)
	}
	blobtest.Put(ctx, t, rt.coldBlobs, []byte("archived"))
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(),
		"blob mirror repair: nothing to do"); {
		if time.Now().After(deadline) {
			t.Fatalf("no repair pass logged:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := rt.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRuntimeErrors(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ghost := func(b *config.BlobConfig) { b.Mirror.Members = append(b.Mirror.Members, "ghost") }
	for name, tune := range map[string]func(*config.Config){
		"bad master key": func(c *config.Config) { c.MasterKey = "not a key" },
		"bad database": func(c *config.Config) {
			c.Database.Driver, c.Database.DSN = "postgres", "postgres://%zz"
		},
		"hot tier":  func(c *config.Config) { c.Blob = mirrorConfig(); ghost(&c.Blob) },
		"cold tier": func(c *config.Config) { c.ColdBlob = mirrorConfig(); ghost(&c.ColdBlob) },
	} {
		cfg := testConfig(t)
		tune(&cfg)
		if _, err := openRuntime(ctx, cfg, log); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	// run reports a failed start without listening.
	cfg := testConfig(t)
	cfg.Blob = mirrorConfig()
	ghost(&cfg.Blob)
	if err := run(ctx, cfg, log, nil); !errors.Is(err, errBlobStore) ||
		!strings.HasPrefix(err.Error(), "start:") {
		t.Fatalf("run = %v", err)
	}
}

func TestRuntimeCloseReportsStoreError(t *testing.T) {
	boom := errors.New("boom")
	rt := &runtime{stopRepair: func() {}, closeStore: func() error { return boom }}
	if err := rt.close(context.Background()); !errors.Is(err, boom) ||
		!strings.HasPrefix(err.Error(), "close:") {
		t.Fatalf("close = %v", err)
	}
}

// TestRuntimeOnPostgresAndMinIO starts the runtime the way a production
// install would: PostgreSQL metadata and an S3 blob store whose credentials
// come from the AWS default chain. It needs Docker and skips without it; CI
// runs it on Linux.
func TestRuntimeOnPostgresAndMinIO(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	minio := containertest.MinIO(t)
	isolateAWS(t)
	t.Setenv("AWS_ACCESS_KEY_ID", minio.User)
	t.Setenv("AWS_SECRET_ACCESS_KEY", minio.Password)
	ctx := context.Background()
	client, _, err := s3.NewClient(ctx, s3.ClientConfig{
		Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket: aws.String("artifacts"),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.Database.Driver, cfg.Database.DSN = "postgres", dsn
	cfg.Blob = config.BlobConfig{BlobBackendConfig: config.BlobBackendConfig{
		Backend: "s3",
		S3: config.BlobS3Config{
			Bucket: "artifacts", Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
		},
	}}
	rt, err := openRuntime(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.close(ctx); err != nil {
			t.Error(err)
		}
	})
	body := blobtest.Content(42, 9<<20)
	info := blobtest.Put(ctx, t, rt.blobs, body)
	r, _, err := rt.blobs.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read back %d bytes, %v", len(got), err)
	}
}
