package debianhttp

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// snapshotAuthority keeps the concrete member paths that contributed to a
// snapshot. Re-evaluating only the virtual root could publish metadata from a
// member whose permission was revoked while its upstream was streaming.
type snapshotAuthority struct {
	reads []snapshotReadAuthority
}

type snapshotReadAuthority struct {
	repo       repo.Config
	path       string
	permission func(context.Context, string) error
}

func (s *snapshotAuthority) add(c repo.Config, path string, permission func(context.Context, string) error) {
	if s != nil {
		s.reads = append(s.reads, snapshotReadAuthority{repo: c, path: path, permission: permission})
	}
}

func (s snapshotAuthority) validate(ctx context.Context, p kernel.Principal, access itemkit.Access) error {
	for _, read := range s.reads {
		if !access.File(read.repo, read.path, accessapi.ActionRead) {
			return httpkit.Denied(p)
		}
		if read.permission != nil {
			if err := read.permission(ctx, read.path); err != nil {
				return fmt.Errorf("snapshot current source authority: %w", snapshotReadError(err, p))
			}
		}
	}
	return nil
}
