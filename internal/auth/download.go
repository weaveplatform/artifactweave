package auth

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// RestoreDownload rechecks a verified download envelope's original credential
// against current local state. It deliberately uses Current, preserving its
// scope, expiry, user status, password and anonymous-access fences.
func (a *Authenticator) RestoreDownload(
	ctx context.Context,
	name string,
	reference kernel.DownloadCredential,
	tokens port.TokenStore,
) (kernel.Principal, error) {
	p := kernel.Principal{Name: name}
	zero := [32]byte{}
	switch reference.Kind {
	case "token":
		if reference.TokenID == "" || reference.PasswordRevision != zero {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
		p.TokenID = reference.TokenID
	case "password":
		if reference.TokenID != "" || reference.PasswordRevision == zero {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
		p.Password = true
		p.PasswordRevision = reference.PasswordRevision
	case "anonymous":
		if name != kernel.AnonymousUser || reference.TokenID != "" ||
			reference.PasswordRevision != zero {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
		p = kernel.Anonymous
	default:
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	return a.Current(ctx, p, tokens)
}
