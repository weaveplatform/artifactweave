package access

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

var _ accessapi.Restrictions = restrictions{}

// restrictions checks the stored API restriction on every call, with no
// cache, as the authenticator does (decision 0043).
type restrictions struct{ st store.Store }

func (b restrictions) Check(ctx context.Context, user string, now time.Time) error {
	//nolint:wrapcheck // auth wraps its own, and answers kernel.ErrAPIRestricted while restricted
	return auth.CheckAPIRestriction(ctx, b.st.APIRestrictions(), user, now)
}
