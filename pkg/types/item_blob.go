package types

import "time"

// Blob is the index record of one stored content blob. Items reference blobs
// by SHA256; Refs counts those references (and those of trash entries and
// bundle artifacts) so garbage collection removes a blob only when nothing
// can serve it again. SHA512, SHA1 and MD5 are recorded so checksum deploy,
// the checksum searches and checksum files resolve without reading content.
type Blob struct {
	Checksums Checksums
	Size      int64
	Created   time.Time
	Refs      int64
	// State is where the row stands in garbage collection (decision 0010);
	// only live rows serve content.
	State BlobState
	// Deleted is when garbage collection marked the content for deletion
	// (deleting) or removed it (deleted). A row revived by a deploy keeps the
	// time until it is pruned, so the deploy knows the content it found may
	// have been removed after it was written and checks it is still there.
	Deleted time.Time
}

// BlobState is a blob index row's stage in garbage collection. A row is
// marked deleting before its content is removed and becomes deleted (a
// tombstone) once it has been, so a deploy of the same content can never
// reference content that is being removed (decision 0010).
type BlobState string

// Blob index row states.
const (
	BlobLive     BlobState = "live"
	BlobDeleting BlobState = "deleting"
	BlobDeleted  BlobState = "deleted"
)
