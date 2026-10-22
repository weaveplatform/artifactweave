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
			Verified: p.token,
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
		p.Registry, p.Scope, p.token = &access, scope, v.Verified && err == nil
		p.tokenSubject, p.TokenExpires, p.TokenParent, p.Identity = v.Subject, v.Expires, v.Parent, v.Identity
	}
	return p
}
