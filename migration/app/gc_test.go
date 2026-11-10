package app

import (
	"context"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/app/config"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/usage"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestAccLocation(t *testing.T) {
	cfg := testConfig(t)
	nearDir := filepath.Join(cfg.DataDir, "blob-stores", "near")
	be := func(b config.BlobBackend) config.Config {
		c := cfg
		c.Blob = config.Blob{BlobBackend: b}
		return c
	}
	for name, tc := range map[string]struct {
		cfg              config.Config
		kind, dir, local string
	}{
		"fs": {
			cfg:  be(config.BlobBackend{Backend: "fs"}),
			kind: "file-system", dir: cfg.BlobRoot(), local: cfg.BlobRoot(),
		},
		"s3": {cfg: be(config.BlobBackend{
			Backend: "s3",
			S3:      config.BlobS3{Bucket: "b", Prefix: "p"},
		}), kind: "s3-storage-v3-direct", dir: usage.NoFilesystem},
		"gcs": {cfg: be(config.BlobBackend{
			Backend: "gcs",
			GCS:     config.BlobGCS{Bucket: "b"},
		}), kind: "google-storage-v2-direct", dir: usage.NoFilesystem},
		"azure": {
			cfg: be(config.BlobBackend{
				Backend: "azure",
				Azure:   config.BlobAzure{Account: "a", Container: "c", Prefix: "p"},
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
				c.Blob.Stores["cloud"] = config.BlobStore{BlobBackend: config.BlobBackend{
					Backend: "s3", S3: config.BlobS3{Bucket: "b"},
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
				c.Blob = config.Blob{
					BlobBackend: config.BlobBackend{Backend: "mirror"},
					Stores: map[string]config.BlobStore{"cloud": {
						BlobBackend: config.BlobBackend{
							Backend: "s3", S3: config.BlobS3{Bucket: "b"},
						},
					}},
					Mirror: config.BlobMirror{Members: []string{"cloud", "unknown"}},
				}
				return c
			}(),
			kind: "user-chain", dir: usage.NoFilesystem,
		},
		"none": {cfg: be(config.BlobBackend{})},
	} {
		hot, _ := tiers(tc.cfg, inmem.New())
		got := hot.location()
		if got.Type != tc.kind || got.Directory != tc.dir || got.Root != tc.local {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestAccRuntimeRunsGC(t *testing.T) {
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testConfig(t)
	cfg.GC.Interval = 10 * time.Millisecond
	ctx := context.Background()
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if rt.modules.artifacts.GC() == nil || rt.location.Type != "file-system" {
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
