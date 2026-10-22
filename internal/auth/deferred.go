package auth

// CredentialReference records which already-authenticated identity scheduled
// bounded background work. It carries no credential, grants or group membership.
// The password revision is an opaque fence; it cannot authenticate a new request.
// References are internal persisted data and must never be accepted from clients.
type CredentialReference struct {
	Name             string   `json:"name"`
	TokenID          string   `json:"tokenid"`
	Anonymous        bool     `json:"anonymous"`
	Password         bool     `json:"password"`
	PasswordRevision [32]byte `json:"passwordrevision"`
}

func (p Principal) CredentialReference() CredentialReference {
	return CredentialReference{
		Name:             p.Name,
		TokenID:          p.TokenID,
		Anonymous:        p.Anonymous,
		Password:         p.Password,
		PasswordRevision: p.passwordRevision,
	}
}

// Principal must pass Authenticator.Current and Authorize before use. In
// particular, this deliberately restores no cached token scope or admin status.
func (r CredentialReference) Principal() Principal {
	return Principal{
		Name:             r.Name,
		TokenID:          r.TokenID,
		Anonymous:        r.Anonymous,
		Password:         r.Password,
		passwordRevision: r.PasswordRevision,
	}
}
