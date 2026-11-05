package api

// API is the access module's published surface: one implementation of each
// contract, which the module builds and app hands to the other modules. A
// field is nil, never a typed nil, when the module has no such service, as
// Tokens is without a token service; a caller tests the field and answers as
// it does when that service is absent.
type API struct {
	Tokens          Tokens
	Keys            Keys
	Identity        Identity
	Restrictions    Restrictions
	Projects        Projects
	Grants          Grants
	AnonymousAccess AnonymousAccess
}
