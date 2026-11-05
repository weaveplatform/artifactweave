package access

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/auth"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

var _ accessapi.Authenticator = authenticator{}

// authenticator binds the store's tokens to the authenticator's rechecks,
// which take them as an argument, so callers no longer pass a store. Errors
// pass through as the authenticator gave them: it wraps its own, and callers
// test its sentinels with errors.Is.
type authenticator struct {
	a  *auth.Authenticator
	st store.Store
}

func (b authenticator) Basic(ctx context.Context, name, password string) (kernel.Principal, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.Basic(ctx, name, password)
}

func (b authenticator) Token(ctx context.Context, raw, user string) (kernel.Principal, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.Token(ctx, raw, user)
}

func (b authenticator) AnonymousAllowed(ctx context.Context) (bool, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.AnonymousAllowed(ctx)
}

func (b authenticator) Current(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.Current(ctx, p, b.st.Tokens())
}

func (b authenticator) CurrentRegistry(
	ctx context.Context,
	p kernel.Principal,
) (kernel.Principal, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.CurrentRegistry(ctx, p, b.st.Tokens())
}

func (b authenticator) RestoreDownload(
	ctx context.Context,
	name string,
	ref kernel.DownloadCredential,
) (kernel.Principal, error) {
	//nolint:wrapcheck // the authenticator's errors are the contract's
	return b.a.RestoreDownload(ctx, name, ref, b.st.Tokens())
}
