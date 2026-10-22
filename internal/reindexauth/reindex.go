package reindexauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errReindexDenied = fmt.Errorf(
	"%w: NuGet reindex caller no longer authorized; schedule a new reindex",
	work.ErrPermanent,
)

// New restores only a trusted queue payload's identity reference.
// Current restores token scope/expiry or the password revision; Authorize reads
// current permissions. No persisted admin/group/permission snapshot is trusted.
func New(
	st store.Store,
	a *auth.Authenticator,
) func(context.Context, repo.Config, format.WorkRequest) error {
	return func(ctx context.Context, c repo.Config, req format.WorkRequest) error {
		var reference auth.CredentialReference
		if a == nil || json.Unmarshal(req.Credential, &reference) != nil ||
			reference.Name != req.By ||
			reference.Registry != nil {
			return errReindexDenied
		}
		p, err := a.Current(ctx, reference.Principal(), st.Tokens())
		if err != nil {
			if errors.Is(err, auth.ErrBadCredentials) || errors.Is(err, auth.ErrPasswordExpired) ||
				errors.Is(err, store.ErrNotFound) {
				return errReindexDenied
			}
			return fmt.Errorf("reindex current credential: %w", err)
		}
		p, err = auth.Authorize(
			ctx,
			authz.Sources{
				Permissions:      st.Permissions(),
				Groups:           st.Groups(),
				AnonymousEnabled: p.Anonymous,
			},
			p,
		)
		if err != nil {
			return fmt.Errorf("reindex current permissions: %w", err)
		}
		if !p.Access().
			Allows(types.ResourceArtifact, authz.Repo{Key: c.Key, Class: string(c.Class)}, "", types.ActionManage) {
			return errReindexDenied
		}
		return nil
	}
}
