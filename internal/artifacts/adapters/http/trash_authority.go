package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// trashRestoreAuthority shares the native restore restrictions between initial
// selection and each current-authority batch, keeping their refusal messages.
// Each request owns its instance; its disposition is never shared across callers.
type trashRestoreAuthority struct {
	store                                     Store
	from, to                                  string
	destination                               repo.Config
	nativeMetadataRelocation, modelRelocation bool
}

func (a *trashRestoreAuthority) permit(ctx context.Context, p kernel.Principal, acc itemkit.Access) (func(model.Item) bool, error) {
	fromKey, fromPath := cutRepoPath(a.from)
	toKey, toPath := cutRepoPath(a.to)
	from := a.from

	if !acc.File(a.destination, toPath, api.ActionManage) {
		return nil, trashRefusalf(p, "Manage permission on '%s' is required", toKey)
	}
	var permit func(model.Item) bool
	if !p.Admin {
		origin, err := a.store.Repos().Get(ctx, fromKey)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return nil, trashRefusalf(
				p,
				"Only an administrator can restore trash of a deleted repository",
			)
		case err != nil:
			return nil, fmt.Errorf("current trash origin: %w", err)
		case !removes(acc, origin, fromPath):
			return nil, trashRefusalf(p, "Delete or manage permission on '%s' is required",
				strings.Trim(from, "/"))
		}
		// An exclude pattern, or an include that admits from but not what
		// lies below it, can still refuse an item the check above allowed.
		relocated := toKey != fromKey || toPath != "" && toPath != fromPath
		permit = func(it model.Item) bool {
			if !removes(acc, origin, it.FullPath()) {
				return false
			}
			// Restored native metadata bytes must remain behind the native filtering route.
			// Restore preflights the entire selection before its first batch.
			if (protocolPrivateItem(origin, it.FullPath()) || chefMetadataItem(origin, it.FullPath())) &&
				relocated {
				a.nativeMetadataRelocation = true
				return false
			}
			if machineLearningPrivate(origin, it.FullPath()) && relocated {
				a.modelRelocation = true
				return false
			}
			return true
		}
	}
	return permit, nil
}
