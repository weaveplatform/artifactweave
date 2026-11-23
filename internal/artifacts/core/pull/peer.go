package pull

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Entry describes an upstream storage item, not an admitted cache item. Path is
// upstream-relative; the owner maps and authorizes it before fetching content.
type Entry struct {
	Path      string
	Folder    bool
	Size      int64
	Checksums kernel.Checksums
	Modified  time.Time
	Updated   time.Time
	// Listed marks facts taken from the storage file list rather than file
	// info. The list has no lastUpdated (Updated holds its property time, or
	// Modified) and no MD5 or SHA-512, so such an entry is compared with a
	// later inspection only on the facts both carry (#1895).
	Listed bool
}

// Peer exposes only ordinary, read-only storage operations on one configured
// repository and selection root. It grants no arbitrary URL or upstream write.
type Peer interface {
	// Binding is a hash of the selected source/configuration/scope authority,
	// not a credential or a grant that may be reused without Revalidate.
	Binding() string
	CheckRead(context.Context, string) error
	Inspect(context.Context, string) (Entry, error)
	// Absent proves an exact item-info miss under an existing namespace.
	// Offline, denied, filtered omission and missing namespaces are not absence.
	Absent(context.Context, string) (bool, error)
	Files(context.Context, kernel.Page) ([]Entry, string, error)
	Properties(context.Context, string) (model.Properties, error)
	Revalidate(context.Context) error
}
