package auth

import (
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// CredentialReference records which already-authenticated identity scheduled
// bounded background work. It carries no credential or group membership. A
// registry reference retains its original narrowing grant for CurrentRegistry;
// it must never become an ordinary HTTP credential.
// The password revision is an opaque fence; it cannot authenticate a new request.
// References are internal persisted data and must never be accepted from clients.
type CredentialReference struct {
	Registry         *RegistryReference `json:"registry,omitempty"`
	Name             string             `json:"name"`
	TokenID          string             `json:"tokenid"`
	Anonymous        bool               `json:"anonymous"`
	Password         bool               `json:"password"`
	PasswordRevision [32]byte           `json:"passwordrevision"`
}

type RegistryReference struct {
	Verified bool                 `json:"verified"`
	Access   types.RegistryAccess `json:"access"`
	Scope    string               `json:"scope"`
	Subject  string               `json:"subject"`
	Expires  time.Time            `json:"expires"`
	Parent   string               `json:"parent"`
	Identity types.TokenIdentity  `json:"identity"`
}

func (p Principal) CredentialReference() CredentialReference {
	r := CredentialReference{
		Name:             p.Name,
		TokenID:          p.TokenID,
		Anonymous:        p.Anonymous,
		Password:         p.Password,
		PasswordRevision: p.passwordRevision,
	}
	if p.Registry != nil {
		r.Registry = &RegistryReference{
			Verified: p.isTokenAuthenticated,
			Access:   store.Clone(*p.Registry),
			Scope:    p.Scope.String(),
			Subject:  p.tokenSubject,
			Expires:  p.TokenExpires,
			Parent:   p.TokenParent,
			Identity: p.Identity,
		}
	}
	return r
}

// Principal must pass Authenticator.Current (CurrentRegistry for registry
// references) and Authorize before use. It restores no cached admin status;
// the retained registry scope can only narrow current permissions.
func (r CredentialReference) Principal() Principal {
	p := Principal{
		Name:             r.Name,
		TokenID:          r.TokenID,
		Anonymous:        r.Anonymous,
		Password:         r.Password,
		passwordRevision: r.PasswordRevision,
	}
	if v := r.Registry; v != nil {
		access := store.Clone(v.Access)
		scope, err := types.ParseScope(v.Scope)
		p.Registry, p.Scope, p.isTokenAuthenticated = &access, scope, v.Verified && err == nil
		p.tokenSubject, p.TokenExpires, p.TokenParent, p.Identity = v.Subject, v.Expires, v.Parent, v.Identity
	}
	return p
}

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
