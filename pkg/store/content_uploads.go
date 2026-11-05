package store

import (
	"context"
	"time"
)

// Upload is a blob upload session that outlives one request: a Docker
// chunked upload, the CLI's multipart upload, or an S3 multipart session.
// State is the blob backend's own session record, opaque here.
type Upload struct {
	ID      string
	Backend string
	RepoKey string
	Path    string
	State   []byte
	Bytes   int64
	Started time.Time
	Updated time.Time
}

// UploadStore persists upload sessions.
type UploadStore interface {
	// Put creates or replaces the session with u.ID.
	Put(ctx context.Context, u Upload) error
	// Get returns the session.
	Get(ctx context.Context, id string) (Upload, error)
	// Delete removes the session.
	Delete(ctx context.Context, id string) error
	// Stale lists sessions last updated (or started, when never updated)
	// before t, oldest first, at most limit (DefaultLimit when zero), for
	// the reaper.
	Stale(ctx context.Context, before time.Time, limit int) ([]Upload, error)
}
