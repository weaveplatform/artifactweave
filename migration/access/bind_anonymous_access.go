package access

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var _ accessapi.AnonymousAccess = anonymousAccess{}

// anonymousAccess reads and writes the security.anonymous_access setting, a
// JSON boolean. Errors are the store's, unwrapped: a setting never written
// matches kernel.ErrNotFound, which each caller reads as off in its own way.
type anonymousAccess struct {
	st interface{ Settings() settings.SettingsStore }
}

func (b anonymousAccess) Enabled(ctx context.Context) (bool, error) {
	var on bool
	if err := b.st.Settings().Get(ctx, types.SettingAnonymousAccess, &on); err != nil {
		return false, err //nolint:wrapcheck // the store's errors are the contract's
	}
	return on, nil
}

func (b anonymousAccess) SetEnabled(ctx context.Context, on bool) error {
	//nolint:wrapcheck // the store's errors are the contract's
	return b.st.Settings().Put(ctx, types.SettingAnonymousAccess, on)
}
