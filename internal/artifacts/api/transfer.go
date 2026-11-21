package api

import (
	"context"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TransferRepository identifies one local repository lifetime and configuration
// generation. These descriptive values grant no content or management access.
type TransferRepository struct {
	Key         string           `json:"key"`
	Incarnation string           `json:"incarnation"`
	Generation  string           `json:"generation"`
	PackageType repo.PackageType `json:"package_type"`
}

// TransferSelection bounds source traversal. Root is an exact path or folder;
// include/exclude are repository-relative ANT patterns, with empty include
// selecting all paths. Properties and Statistics select optional metadata.
type TransferSelection struct {
	Root, Include, Exclude string
	Properties, Statistics bool
}

// TransferStatistics contains download facts supported by ordinary item APIs.
// It deliberately excludes remote-origin URLs and remote-cache credentials.
type TransferStatistics struct {
	DownloadCount    int64     `json:"download_count"`
	LastDownloaded   time.Time `json:"last_downloaded"`
	LastDownloadedBy string    `json:"last_downloaded_by"`
}

// TransferItem is an immutable source snapshot, never an authorization token.
// Version binds the represented identity, bytes and selected metadata. IDs and
// timestamps alone cannot distinguish every overwrite or property change.
type TransferItem struct {
	Repository         TransferRepository  `json:"repository"`
	ID                 int64               `json:"id"`
	Path               string              `json:"path"`
	Folder             bool                `json:"folder"`
	Size               int64               `json:"size"`
	Actual             kernel.Checksums    `json:"actual"`
	Created            time.Time           `json:"created"`
	Modified           time.Time           `json:"modified"`
	Updated            time.Time           `json:"updated"`
	PropertiesModified time.Time           `json:"properties_modified"`
	Properties         map[string][]string `json:"properties"`
	Statistics         *TransferStatistics `json:"statistics"`
	PropertiesIncluded bool                `json:"properties_included"`
	Version            string              `json:"version"`
}

// TransferSource publishes bounded local content without exposing artifacts'
// core models or blob layout. Each operation refreshes the original caller's
// credential and Read permission under the current project guard. An error
// returns no partial metadata page; a denied selected path is not an absence.
// Item pages may be empty with a continuation: callers must follow Next until
// empty before treating enumeration as complete. Concurrent changes do not
// create a repository-wide frozen snapshot.
type TransferSource interface {
	Repository(context.Context, string) (TransferRepository, error)
	Items(context.Context, TransferRepository, TransferSelection, kernel.Page) ([]TransferItem, string, error)
	// Open validates the snapshot before opening immutable content outside the
	// metadata transaction. It refuses an already-transactional context. The
	// caller closes the body and uses WithCurrent after transfer before claiming
	// that this source version succeeded; bytes already sent cannot be recalled.
	Open(context.Context, TransferItem) (io.ReadSeekCloser, error)
	// WithCurrent joins a short metadata transaction, checks the complete source
	// snapshot, then invokes local checkpoint work. The callback must perform
	// no network I/O or client streaming and propagate every failure.
	WithCurrent(context.Context, TransferItem, func(context.Context) error) error
	// WithAbsent separately authorizes Read at the exact canonical path before
	// testing absence. Missing/denied repositories and changed lifetimes cannot
	// authorize deletion. The callback has the same local-only contract.
	WithAbsent(context.Context, TransferRepository, string, func(context.Context) error) error
}
