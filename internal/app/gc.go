package app

import (
	"log/slog"
	"path"
	"strings"

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

// location describes the tier's blob store for the storage summary.
func (t blobTier) location() usage.Location {
	b := t.cfg
	switch b.Backend {
	case "fs":
		return usage.Location{Type: "file-system", Directory: t.root, Root: t.root}
	case "s3":
		return usage.Location{Type: "s3", Directory: "s3://" + path.Join(b.S3.Bucket, b.S3.Prefix)}
	case "gcs":
		return usage.Location{
			Type:      "google-cloud-storage",
			Directory: "gs://" + path.Join(b.GCS.Bucket, b.GCS.Prefix),
		}
	case "azure":
		return usage.Location{
			Type:      "azure-blob-storage",
			Directory: path.Join(b.Azure.Account, b.Azure.Container, b.Azure.Prefix),
		}
	case "mirror":
		return usage.Location{Type: "mirror", Directory: strings.Join(b.Mirror.Members, ", ")}
	}
	return usage.Location{}
}
