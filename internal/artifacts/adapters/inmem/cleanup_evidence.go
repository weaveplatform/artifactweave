package inmem

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var (
	_ port.CleanupMemberReader     = repos{}
	_ port.PackageDownloadEvidence = packages{}
)

func (r repos) CleanupMembers(ctx context.Context, key string) ([]repo.Member, error) {
	var out []repo.Member
	err := r.s.Do(ctx, func() error {
		value, found := r.t().Get(key)
		if !found {
			return kernel.ErrNotFound
		}
		if len(value.Members) > repo.MaxMembers {
			return fmt.Errorf("cleanup repository membership exceeds its metadata bound: %w", kernel.ErrStore)
		}
		for _, member := range value.Members {
			if len(member.URL) > 8192 {
				return fmt.Errorf("cleanup repository member exceeds its metadata bound: %w", kernel.ErrStore)
			}
		}
		out = kernel.Clone(value.Members)
		return nil
	})
	return out, err
}

func (p packages) downloadRevisions() *memtx.Table[int64, string] {
	return memtx.TableOf[int64, string](p.s.Store, "package_version_download_revisions")
}

func (p packages) downloadChanged(id int64) { p.downloadRevisions().Put(id, rand.Text()) }

func (i items) versionsDownloaded(id int64) {
	i.versionsChanged(id)
	p := packages(i)
	for link := range p.files().Rows {
		if link.ItemID == id {
			p.downloadChanged(link.VersionID)
		}
	}
}

func (p packages) VersionDownloads(ctx context.Context, key string, versions []api.DownloadVersion) ([]api.VersionDownload, error) {
	if len(versions) == 0 || len(versions) > api.MaxDownloadVersions {
		return nil, fmt.Errorf("version download query exceeds its bound: %w", kernel.ErrInvalid)
	}
	var out []api.VersionDownload
	err := p.s.Do(ctx, func() error {
		repository, found := repos(p).t().Get(key)
		if !found {
			return kernel.ErrNotFound
		}
		for _, wanted := range versions {
			var packageID int64
			for _, candidate := range p.t().Rows {
				if candidate.RepoKey == key && candidate.Type == repository.PackageType && candidate.Name == wanted.Package {
					packageID = candidate.ID
					break
				}
			}
			if packageID == 0 {
				continue
			}
			for _, version := range p.versions().Rows {
				if version.PackageID != packageID || version.Version != wanted.Version {
					continue
				}
				revision, exists := p.downloadRevisions().Get(version.ID)
				if !exists {
					revision = "initial"
				}
				value := api.VersionDownload{DownloadVersion: wanted, ID: version.ID, Revision: revision, Created: version.Created, LastDownloaded: version.LastDownloaded}
				activity, exists := cleanupRecords(p).activity().Get(version.ID)
				if !exists {
					activity.Revision = "initial"
				}
				repositoryActivity, exists := cleanupRecords(p).repositoryActivity().Get(key)
				if !exists {
					repositoryActivity.Revision = "initial"
				}
				value.AdmittedAt = activity.At
				if repositoryActivity.At.After(value.AdmittedAt) {
					value.AdmittedAt = repositoryActivity.At
				}
				value.AdmissionRevision = activity.Revision + "/" + repositoryActivity.Revision
				linked := false
				for link := range p.files().Rows {
					if link.VersionID != version.ID {
						continue
					}
					item, present := items(p).t().Get(link.ItemID)
					if !present || item.RepoKey != key || item.Cached || item.Folder {
						continue
					}
					linked = true
					stats, _ := items(p).stats().Get(item.ID)
					if stats.LastDownloaded.After(value.LastDownloaded) {
						value.LastDownloaded = stats.LastDownloaded
					}
					if stats.RemoteLastDownloaded.After(value.LastDownloaded) {
						value.LastDownloaded = stats.RemoteLastDownloaded
					}
				}
				if linked {
					out = append(out, value)
				}
			}
		}
		return nil
	})
	slices.SortFunc(out, func(a, b api.VersionDownload) int {
		if a.Package != b.Package {
			return cmp.Compare(a.Package, b.Package)
		}
		return cmp.Compare(a.Version, b.Version)
	})
	return out, err
}
