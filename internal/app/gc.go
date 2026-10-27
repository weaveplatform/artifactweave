package app

import (
	"log/slog"
	"path/filepath"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
)

// logGC logs each scheduled garbage collection pass: one that reclaimed
// something at Info, a failed one at Warn, and idle or skipped ones at Debug.
func logGC(log *slog.Logger) func(gc.Report, error) {
	return func(r gc.Report, err error) {
		switch {
		case err != nil:
			log.Warn("garbage collection failed", "error", err)
		case r.Skipped:
			log.Debug("garbage collection skipped: another instance holds the lease")
		case r.TrashPurged+r.UploadsReaped+r.BlobsDeleted+r.OrphansDeleted == 0 &&
			r.TokensExpired == 0:
			log.Debug("garbage collection: nothing to do")
		default:
			log.Info("garbage collection",
				"trash_purged", r.TrashPurged, "uploads_reaped", r.UploadsReaped,
				"blobs_deleted", r.BlobsDeleted, "orphans_deleted", r.OrphansDeleted,
				"bytes_freed", r.BytesFreed, "kept", r.Kept, "tokens_expired", r.TokensExpired)
		}
	}
}

// location describes the tier's blob store for the storage summary. Its type
// is the name of the Artifactory binary provider template that stores blobs
// the way the backend does. The object stores are the -direct templates,
// which write to the bucket with no eventual upload in between, as these
// backends do; JFrog's S3 SaaS reports s3-storage-v3-direct. A mirror
// spreads copies over stores of any kind, which in Artifactory is a sharding
// chain written out in binarystore.xml, and 7.x reports such a chain as
// user-chain (research 15). An object store keeps no local cache, so it
// reports the directory Artifactory gives a chain without one, not its
// bucket.
//
// A mirror reports its first fs member's directory, and that directory's
// volume, as 6.20 reports the first file-system provider of a chain and
// measures it (BinaryServiceImpl.getBinariesDir, FileStoreStorageSummary);
// with no fs member it is a chain without one.
func (t blobTier) location() usage.Location {
	b := t.cfg
	switch b.Backend {
	case "fs":
		return usage.Location{Type: "file-system", Directory: t.root, Root: t.root}
	case "s3":
		return usage.Location{Type: "s3-storage-v3-direct", Directory: usage.NoFilesystem}
	case "gcs":
		return usage.Location{Type: "google-storage-v2-direct", Directory: usage.NoFilesystem}
	case "azure":
		return usage.Location{Type: "azure-blob-storage-v2-direct", Directory: usage.NoFilesystem}
	case "mirror":
		for _, name := range b.Mirror.Members {
			if sc, ok := b.Stores[name]; ok && sc.Backend == "fs" {
				dir := t.memberRoot(name, sc)
				return usage.Location{Type: "user-chain", Directory: dir, Root: dir}
			}
		}
		return usage.Location{Type: "user-chain", Directory: usage.NoFilesystem}
	}
	return usage.Location{}
}

// memberRoot is the directory the mirror member name writes when it is an
// fs store: its own root, or one under storesDir named for it. The other
// backends ignore it.
func (t blobTier) memberRoot(name string, sc config.BlobStore) string {
	if sc.FS.Root != "" {
		return sc.FS.Root
	}
	return filepath.Join(t.storesDir, name)
}
