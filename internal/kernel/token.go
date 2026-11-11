package kernel

// TokenIdentity says how a token's subject resolves to a user. Access issues
// tokens from an OIDC token exchange without checking that the mapped user
// exists (research 21), so such a token may name a user who is not stored.
type TokenIdentity string

// Token identities.
const (
	// IdentityStored: the subject must be a stored, enabled user. Every
	// token whose subject was a stored user when it was issued, other than
	// an OIDC exchange's.
	IdentityStored TokenIdentity = ""
	// IdentityTransient: a stored user of that name if there is one;
	// otherwise a transient user for the request, a member of the groups new
	// users join automatically. An OIDC exchange issues it, and so does an
	// administrator minting a token for a name that is no stored user.
	IdentityTransient TokenIdentity = "transient"
	// IdentitySynthetic: never a stored user and in no group but those the
	// scope names. An identity mapping without a user name issues it, so a
	// group-scoped token acts through exactly its groups.
	IdentitySynthetic TokenIdentity = "synthetic"
)

// RegistryAccess is what a registry token grants: the scopes the client
// asked the token endpoint for, each with only the actions the caller held.
type RegistryAccess struct {
	// Realm is the repository key the token endpoint was addressed by, the
	// <repo> of .../api/docker/<repo>/v2/token or a subdomain's repository;
	// empty for null and for /v2/token on the repository path, where each
	// name begins with its repository key.
	Realm  string          `json:"realm,omitempty"`
	Grants []RegistryGrant `json:"access"`
}

// RegistryGrant is one granted scope in the Docker token specification's
// form: type repository with an image name as the client addresses it and
// the actions pull, push and delete, or type registry with name catalog.
type RegistryGrant struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}
