package access

import (
	"context"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
)

var _ accessapi.Restrictions = restrictions{}

// restrictions checks the stored API restriction on every call, with no
// cache, as the authenticator does (decision 0043).
type restrictions struct{ st Store }

func (b restrictions) Check(ctx context.Context, user string, now time.Time) error {
	//nolint:wrapcheck // auth wraps its own, and answers kernel.ErrAPIRestricted while restricted
	return authn.CheckAPIRestriction(ctx, b.st.APIRestrictions(), user, now)
}
