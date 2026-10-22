package auth

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// DownloadCredential is a credential reference for an authenticated, signed
// download envelope. It contains no password or bearer token. Possession of this
// reference is not authentication: callers must verify their envelope's signature,
// subject, path, digest and expiry before RestoreDownload is called.
type DownloadCredential struct {
	Kind             string   `json:"kind"`
	TokenID          string   `json:"token_id,omitempty"`
	PasswordRevision [32]byte `json:"password_revision,omitzero"`
}

// DownloadReference retains only the identity needed to revalidate the original
// credential. The stored token remains authoritative for identity, scope and expiry.
func (p Principal) DownloadReference() (DownloadCredential, error) {
	if p.Registry != nil || p.PasswordExpired || p.Name == "" {
		return DownloadCredential{}, ErrBadCredentials
	}
	switch {
	case p.TokenID != "":
		return DownloadCredential{Kind: "token", TokenID: p.TokenID}, nil
	case p.Anonymous:
		return DownloadCredential{Kind: "anonymous"}, nil
	case p.Password:
		return DownloadCredential{Kind: "password", PasswordRevision: p.passwordRevision}, nil
	default:
		return DownloadCredential{}, ErrBadCredentials
	}
}

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
		p.passwordRevision = reference.PasswordRevision
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
