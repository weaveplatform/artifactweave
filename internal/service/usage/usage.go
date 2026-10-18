// Package usage computes the storage summary (GET /api/storageinfo): blob
// totals, item totals per repository and for the trash can, and the capacity
// of the volume a local blob store lives on. It is computed on every request
// rather than cached, so it never lags writes.
package usage

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// DefaultProject is the project key of a repository in no project.
const DefaultProject = "default"

// Location describes the hot blob store for the summary.
type Location struct {
	// Type names the store as Artifactory names a binary provider chain:
	// its template, such as file-system or s3-storage-v3-direct, or
	// user-chain for a chain no template describes.
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

// Row is one repository's line, a remote repository's cache under its
// "<key>-cache" alias, or the trash can's.
type Row struct {
	Key string
	// Type is LOCAL, CACHE, VIRTUAL or FEDERATED, or NA for the trash can.
	Type string
	// PackageType is empty for the trash can.
	PackageType repo.PackageType
	// ProjectKey is DefaultProject for a repository in no project and for
	// the trash can.
	ProjectKey string
	Files      int64
	Folders    int64
	Bytes      int64
}

// Summary is the storage summary.
type Summary struct {
	BinariesCount, BinariesSize int64
	// ArtifactsCount and ArtifactsSize count every file as stored before
	// deduplication, the trash can's included; ItemsCount adds the folders.
	ArtifactsCount, ArtifactsSize, ItemsCount int64
	Location                                  Location
	// Space is nil when the capacity is unknown.
	Space *Space
	// Rows are sorted by key, the trash can's among them.
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
		row := Row{
			Key: c.Key, Type: strings.ToUpper(string(c.Class)), PackageType: c.PackageType,
			ProjectKey: c.ProjectKey,
		}
		if row.ProjectKey == "" {
			row.ProjectKey = DefaultProject
		}
		u := byRepo[key{c.Key, false}]
		if c.Class == repo.Remote {
			row.Key, row.Type = c.Key+types.CacheSuffix, "CACHE"
			u = byRepo[key{c.Key, true}]
		}
		row.Files, row.Folders, row.Bytes = u.Files, u.Folders, u.Bytes
		s.Rows = append(s.Rows, row)
	}
	// 7.x counts the trash can as a repository of its own, in its row and in
	// every total: the TOTAL rows of its captures include the auto-trashcan
	// row (research 15).
	bin, err := trashUsage(ctx, st)
	if err != nil {
		return s, err
	}
	s.ArtifactsCount += bin.Files
	s.ArtifactsSize += bin.Bytes
	s.ItemsCount += bin.Files + bin.Folders
	s.Rows = append(s.Rows, bin)
	slices.SortFunc(s.Rows, func(a, b Row) int { return strings.Compare(a.Key, b.Key) })
	if loc.Root != "" {
		if total, free, ok := diskSpace(loc.Root); ok {
			s.Space = &Space{Total: total, Free: free}
		}
	}
	return s, nil
}

// trashUsage counts the trash can as its storage listing shows it
// (trash.Service.Children). Artifactory keeps deleted items in a repository,
// so a path trashed twice is one file there, and the folders above a trashed
// item, its repository's among them, are folders of that repository. A count
// of trash entries would differ on both.
func trashUsage(ctx context.Context, st store.Store) (Row, error) {
	row := Row{Key: trash.Repository, Type: "NA", ProjectKey: DefaultProject}
	sizes := map[string]int64{}
	folders := map[string]bool{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := st.Trash().List(ctx, "", "", page)
		if err != nil {
			return row, fmt.Errorf("usage: trash: %w", err)
		}
		for _, e := range batch {
			it := e.Record.Item
			p := path.Join(it.RepoKey, it.FullPath())
			for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
				folders[dir] = true
			}
			if it.Folder {
				folders[p] = true
				continue
			}
			// List orders a path's entries by ID, so the newest, which a
			// restore brings back, is assigned last.
			sizes[p] = it.Size
		}
		if next == "" {
			break
		}
		page.After = next
	}
	row.Files, row.Folders = int64(len(sizes)), int64(len(folders))
	for _, n := range sizes {
		row.Bytes += n
	}
	return row, nil
}
