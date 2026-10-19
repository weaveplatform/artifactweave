package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestLogGC(t *testing.T) {
	var buf bytes.Buffer
	f := logGC(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	f(gc.Report{}, errors.New("boom"))
	f(gc.Report{Skipped: true}, nil)
	f(gc.Report{}, nil)
	f(gc.Report{BlobsDeleted: 2, BytesFreed: 10}, nil)
	for _, want := range []string{
		`level=WARN msg="garbage collection failed"`,
		"another instance holds the lease",
		"nothing to do",
		`level=INFO msg="garbage collection" trash_purged=0 uploads_reaped=0 blobs_deleted=2`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestLocation(t *testing.T) {
	cfg := testConfig(t)
	nearDir := filepath.Join(cfg.DataDir, "blob-stores", "near")
	be := func(b config.BlobBackendConfig) config.Config {
		c := cfg
		c.Blob = config.BlobConfig{BlobBackendConfig: b}
		return c
	}
	for name, tc := range map[string]struct {
		cfg              config.Config
		kind, dir, local string
	}{
		"fs": {
			cfg:  be(config.BlobBackendConfig{Backend: "fs"}),
			kind: "file-system", dir: cfg.BlobRoot(), local: cfg.BlobRoot(),
		},
		"s3": {cfg: be(config.BlobBackendConfig{
			Backend: "s3",
			S3:      config.BlobS3Config{Bucket: "b", Prefix: "p"},
		}), kind: "s3-storage-v3-direct", dir: usage.NoFilesystem},
		"gcs": {cfg: be(config.BlobBackendConfig{
			Backend: "gcs",
			GCS:     config.BlobGCSConfig{Bucket: "b"},
		}), kind: "google-storage-v2-direct", dir: usage.NoFilesystem},
		"azure": {
			cfg: be(config.BlobBackendConfig{
				Backend: "azure",
				Azure:   config.BlobAzureConfig{Account: "a", Container: "c", Prefix: "p"},
			}),
			kind: "azure-blob-storage-v2-direct", dir: usage.NoFilesystem,
		},
		// A mirror reports its first fs member's directory and volume, as
		// 6.20 does a chain's first file-system provider (#490).
		"mirror": {
			cfg:  func() config.Config { c := cfg; c.Blob = mirrorConfig(); return c }(),
			kind: "user-chain", dir: nearDir, local: nearDir,
		},
		"mirror after an object store": {
			cfg: func() config.Config {
				c := cfg
				c.Blob = mirrorConfig()
				c.Blob.Stores = maps.Clone(c.Blob.Stores)
				c.Blob.Stores["cloud"] = config.BlobStoreConfig{BlobBackendConfig: config.BlobBackendConfig{
					Backend: "s3", S3: config.BlobS3Config{Bucket: "b"},
				}}
				far := c.Blob.Stores["far"]
				far.FS.Root = "/srv/far"
				c.Blob.Stores["far"] = far
				c.Blob.Mirror.Members = []string{"cloud", "far", "near"}
				return c
			}(),
			kind: "user-chain", dir: "/srv/far", local: "/srv/far",
		},
		"mirror of object stores": {
			cfg: func() config.Config {
				c := cfg
				c.Blob = config.BlobConfig{
					BlobBackendConfig: config.BlobBackendConfig{Backend: "mirror"},
					Stores: map[string]config.BlobStoreConfig{"cloud": {
						BlobBackendConfig: config.BlobBackendConfig{
							Backend: "s3", S3: config.BlobS3Config{Bucket: "b"},
						},
					}},
					Mirror: config.BlobMirrorConfig{Members: []string{"cloud", "unknown"}},
				}
				return c
			}(),
			kind: "user-chain", dir: usage.NoFilesystem,
		},
		"none": {cfg: be(config.BlobBackendConfig{})},
	} {
		hot, _ := tiers(tc.cfg, inmem.New())
		got := hot.location()
		if got.Type != tc.kind || got.Directory != tc.dir || got.Root != tc.local {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestRuntimeRunsGC(t *testing.T) {
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testConfig(t)
	cfg.GC.Interval = 10 * time.Millisecond
	ctx := context.Background()
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if rt.gc == nil || rt.location.Type != "file-system" {
		t.Fatalf("runtime %+v", rt)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(),
		"garbage collection: nothing to do"); {
		if time.Now().After(deadline) {
			t.Fatalf("no garbage collection pass logged:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := rt.close(ctx); err != nil {
		t.Fatal(err)
	}
}
