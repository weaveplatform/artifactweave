package api

// API is the access module's published surface: one implementation of each
// contract, which the module builds and app hands to the other modules, and
// the access settings they read. A contract field is nil, never a typed nil,
// when the module has no such service, as Authenticator is without an
// authenticator and Tokens without a token service; a caller tests the field
// and answers as it does when that service is absent.
type API struct {
	Authenticator   Authenticator
	Authorizer      Authorizer
	Tokens          Tokens
	Keys            Keys
	Identity        Identity
	Restrictions    Restrictions
	Projects        Projects
	Grants          Grants
	AnonymousAccess AnonymousAccess
	Settings        Settings
}
