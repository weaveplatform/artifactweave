package reindexauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errReindexDenied = fmt.Errorf(
	"%w: nuget reindex caller no longer authorized; schedule a new reindex",
	work.ErrPermanent,
)

// New returns the check that a queued remote reindex passes before it runs
// and whenever its format rechecks (format.Env.CheckReindex). It trusts only
// the credential reference in the queue payload, never a stored admin flag,
// group or permission: authn's Current restores the credential as it is now
// (a token's scope and expiry, or the password revision), and authz's
// Authorize evaluates the caller's permissions as they are now. The check
// refuses with a permanent work error when authn is nil, when the payload
// does not parse or names a user other than the request's or a registry
// token, when the credential no longer holds, and when the caller lacks
// Manage on the repository. Any other failure is wrapped as "reindex current
// credential: %w" or "reindex current permissions: %w", so the work retries.
func New(
	authn accessapi.Authenticator,
	authz accessapi.Authorizer,
) func(context.Context, repo.Config, format.WorkRequest) error {
	return func(ctx context.Context, c repo.Config, req format.WorkRequest) error {
		var reference kernel.CredentialReference
		if authn == nil || json.Unmarshal(req.Credential, &reference) != nil ||
			reference.Name != req.By ||
			reference.Registry != nil {
			return errReindexDenied
		}
		p, err := authn.Current(ctx, reference.Principal())
		if err != nil {
			if errors.Is(err, kernel.ErrBadCredentials) ||
				errors.Is(err, kernel.ErrPasswordExpired) ||
				errors.Is(err, kernel.ErrNotFound) {
				return errReindexDenied
			}
			return fmt.Errorf("reindex current credential: %w", err)
		}
		_, d, err := authz.Authorize(ctx, p, p.Anonymous)
		if err != nil {
			return fmt.Errorf("reindex current permissions: %w", err)
		}
		if !d.Allows(
			accessapi.ResourceArtifact,
			accessapi.Repo{Key: c.Key, Class: string(c.Class)},
			"",
			accessapi.ActionManage,
		) {
			return errReindexDenied
		}
		return nil
	}
}
