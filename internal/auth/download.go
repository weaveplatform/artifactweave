package auth

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// RestoreDownload rechecks a verified download envelope's original credential
// against current local state. It deliberately uses Current, preserving its
// scope, expiry, user status, password and anonymous-access fences.
func (a *Authenticator) RestoreDownload(
	ctx context.Context,
	name string,
	reference DownloadCredential,
	tokens store.TokenStore,
) (Principal, error) {
	p := Principal{Name: name}
	zero := [32]byte{}
	switch reference.Kind {
	case "token":
		if reference.TokenID == "" || reference.PasswordRevision != zero {
			return Principal{}, ErrBadCredentials
		}
		p.TokenID = reference.TokenID
	case "password":
		if reference.TokenID != "" || reference.PasswordRevision == zero {
			return Principal{}, ErrBadCredentials
		}
		p.Password = true
		p.PasswordRevision = reference.PasswordRevision
	case "anonymous":
		if name != AnonymousUser || reference.TokenID != "" || reference.PasswordRevision != zero {
			return Principal{}, ErrBadCredentials
		}
		p = Anonymous
	default:
		return Principal{}, ErrBadCredentials
	}
	return a.Current(ctx, p, tokens)
}
