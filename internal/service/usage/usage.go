// Package usage computes the storage summary (GET /api/storageinfo): blob
// totals, item totals per repository, and the capacity of the volume a local
// blob store lives on. It is computed on every request rather than cached,
// so it never lags writes.
package usage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Location describes the hot blob store for the summary.
type Location struct {
	// Type is the backend kind: file-system, s3, google-cloud-storage,
	// azure-blob-storage or mirror.
	Type string
	// Directory is the root directory, or the bucket or container with its
	// prefix, or the mirror's members.
	Directory string
	// Root is the local directory whose volume reports capacity; empty for
	// object storage, whose capacity is unknown.
	Root string
}

// Space is the capacity of a volume in bytes.
type Space struct {
	Total, Free int64
}

// Row is one repository's line, or a remote repository's cache under its
// "<key>-cache" alias.
type Row struct {
	Key         string
	Type        string
	PackageType repo.PackageType
	Files       int64
	Folders     int64
	Bytes       int64
}

// Summary is the storage summary.
type Summary struct {
	BinariesCount, BinariesSize int64
	// ArtifactsCount and ArtifactsSize count every file as stored before
	// deduplication; ItemsCount adds the folders.
	ArtifactsCount, ArtifactsSize, ItemsCount int64
	Location                                  Location
	// Space is nil when the capacity is unknown.
	Space *Space
	// Rows are sorted by key.
	Rows []Row
}

// Summarize computes the summary of st over the blob store at loc.
func Summarize(ctx context.Context, st store.Store, loc Location) (Summary, error) {
	s := Summary{Location: loc}
	var err error
	if s.BinariesCount, s.BinariesSize, err = st.Blobs().Totals(ctx); err != nil {
		return s, fmt.Errorf("usage: blob totals: %w", err)
	}
	usage, err := st.Items().Usage(ctx)
	if err != nil {
		return s, fmt.Errorf("usage: item totals: %w", err)
	}
	type key struct {
		repo   string
		cached bool
	}
	byRepo := map[key]store.RepoUsage{}
	for _, u := range usage {
		byRepo[key{u.RepoKey, u.Cached}] = u
		s.ArtifactsCount += u.Files
		s.ArtifactsSize += u.Bytes
		s.ItemsCount += u.Files + u.Folders
	}
	repos, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return s, fmt.Errorf("usage: repositories: %w", err)
	}
	for _, c := range repos {
		row := Row{Key: c.Key, Type: strings.ToUpper(string(c.Class)), PackageType: c.PackageType}
		u := byRepo[key{c.Key, false}]
		if c.Class == repo.Remote {
			row.Key, row.Type = c.Key+types.CacheSuffix, "CACHE"
			u = byRepo[key{c.Key, true}]
		}
		row.Files, row.Folders, row.Bytes = u.Files, u.Folders, u.Bytes
		s.Rows = append(s.Rows, row)
	}
	slices.SortFunc(s.Rows, func(a, b Row) int { return strings.Compare(a.Key, b.Key) })
	if loc.Root != "" {
		if total, free, ok := diskSpace(loc.Root); ok {
			s.Space = &Space{Total: total, Free: free}
		}
	}
	return s, nil
}
