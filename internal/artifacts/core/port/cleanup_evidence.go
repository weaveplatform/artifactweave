package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// CleanupMemberReader projects only the bounded member list, without opening
// repository passwords, headers or unrelated configuration documents.
type CleanupMemberReader interface {
	CleanupMembers(context.Context, string) ([]repo.Member, error)
}

// PackageDownloadEvidence reads at most the requested logical version rows
// under their current repository identity. It traverses index metadata only.
type PackageDownloadEvidence interface {
	VersionDownloads(context.Context, string, []api.DownloadVersion) ([]api.VersionDownload, error)
}
