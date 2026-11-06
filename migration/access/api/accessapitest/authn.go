package accessapitest

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var _ accessapi.Authenticator = (*Authenticator)(nil)

// Authenticator is an Authenticator whose function fields set its answers,
// each with the signature of the method it answers. Where a field is nil it
// answers as an authenticator that verifies no credential and finds nothing
// changed since it verified one: Basic, Token and RestoreDownload answer
// kernel.ErrBadCredentials, AnonymousAllowed answers false, as a setting that
// was never written does, and Current and CurrentRegistry return the
// principal they are given. It holds no state, so it may be used from
// several goroutines as long as its functions may.
type Authenticator struct {
	// BasicFunc answers Basic.
	BasicFunc func(ctx context.Context, name, password string) (kernel.Principal, error)
	// TokenFunc answers Token.
	TokenFunc func(ctx context.Context, raw, user string) (kernel.Principal, error)
	// AnonymousAllowedFunc answers AnonymousAllowed.
	AnonymousAllowedFunc func(ctx context.Context) (bool, error)
	// CurrentFunc answers Current.
	CurrentFunc func(ctx context.Context, p kernel.Principal) (kernel.Principal, error)
	// CurrentRegistryFunc answers CurrentRegistry.
	CurrentRegistryFunc func(ctx context.Context, p kernel.Principal) (kernel.Principal, error)
	// RestoreDownloadFunc answers RestoreDownload.
	RestoreDownloadFunc func(
		ctx context.Context,
		name string,
		ref kernel.DownloadCredential,
	) (kernel.Principal, error)
}

// Basic answers with BasicFunc, or kernel.ErrBadCredentials when it is nil.
func (a *Authenticator) Basic(
	ctx context.Context,
	name, password string,
) (kernel.Principal, error) {
	if a.BasicFunc == nil {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return a.BasicFunc(ctx, name, password)
}

// Token answers with TokenFunc, or kernel.ErrBadCredentials when it is nil.
func (a *Authenticator) Token(ctx context.Context, raw, user string) (kernel.Principal, error) {
	if a.TokenFunc == nil {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return a.TokenFunc(ctx, raw, user)
}

// AnonymousAllowed answers with AnonymousAllowedFunc, or false when it is
// nil.
func (a *Authenticator) AnonymousAllowed(ctx context.Context) (bool, error) {
	if a.AnonymousAllowedFunc == nil {
		return false, nil
	}
	return a.AnonymousAllowedFunc(ctx)
}

// Current answers with CurrentFunc, or returns p when it is nil.
func (a *Authenticator) Current(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
	if a.CurrentFunc == nil {
		return p, nil
	}
	return a.CurrentFunc(ctx, p)
}

// CurrentRegistry answers with CurrentRegistryFunc, or returns p when it is
// nil.
func (a *Authenticator) CurrentRegistry(
	ctx context.Context,
	p kernel.Principal,
) (kernel.Principal, error) {
	if a.CurrentRegistryFunc == nil {
		return p, nil
	}
	return a.CurrentRegistryFunc(ctx, p)
}

// RestoreDownload answers with RestoreDownloadFunc, or
// kernel.ErrBadCredentials when it is nil.
func (a *Authenticator) RestoreDownload(
	ctx context.Context,
	name string,
	ref kernel.DownloadCredential,
) (kernel.Principal, error) {
	if a.RestoreDownloadFunc == nil {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return a.RestoreDownloadFunc(ctx, name, ref)
}
