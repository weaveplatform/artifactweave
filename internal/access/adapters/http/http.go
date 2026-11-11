package accesshttp

import (
	"log/slog"
	"maps"
	"net/http"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/idtoken"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// Store is what the access handlers read and write: the access ports, the
// settings and OIDC configuration documents they serve, and the transaction
// a key pair delete runs in. The application passes its composite store,
// and every call joins the transaction ctx carries. The adapter declares
// its own rather than taking the module root's access.Store, because a
// module package never imports a module root, and because the handlers also
// serve the OIDC configuration documents, which no binding reads.
type Store interface {
	port.Store
	// Settings holds the security policies, the basic authentication
	// switch and the other access settings the handlers serve.
	Settings() settings.SettingsStore
	// Configs holds the OIDC providers and identity mappings.
	Configs() configdoc.ConfigStore
}

// Deps is what the access handlers need, which the composition root
// supplies. A nil Store leaves every operation that reads one answering 501,
// as a router built to test routing alone answers.
type Deps struct {
	// API is the access module's API. The handlers evaluate a principal the
	// middleware did not authorise through its Authorizer.
	API accessapi.API
	// Store holds the users, groups, permissions, key pairs, certificates,
	// tokens, API restrictions, settings and OIDC documents the handlers
	// read and write. The router's store is an interface, so a nil one
	// converts to a nil Store; a typed nil pointer would not, and the
	// operations would call it instead of answering 501.
	Store Store
	// Authenticator forgets its cached password policies when the security
	// API changes them, and recognises the expired access token a legacy
	// refresh presents. Nil skips both.
	Authenticator *authn.Authenticator
	// Tokens issues, refreshes and revokes access tokens; nil answers the
	// token APIs and the OIDC exchange 501.
	Tokens *token.Service
	// IDTokens verifies the subject tokens of the OIDC token exchange and
	// caches each provider's signing keys. Nil makes New build one.
	IDTokens *idtoken.Verifier
	// Settings holds token.allow_basic_auth, which lets a password sent with
	// Basic create tokens, and the global environment limit of the roles API.
	Settings accessapi.Settings
	// PasswordHash is the cost of the passwords the security API sets; the
	// zero value is production's.
	PasswordHash authn.HashParams
	// CompatVersion is the Artifactory version Access reports as its own
	// name and version (GetAccessVersion).
	CompatVersion string
	// Log receives the handlers' internal errors; nil means slog.Default.
	Log *slog.Logger
	// Repositories is the artifacts module's binding over Store. A key pair
	// delete clears the repositories' references to the pair through it in
	// the delete's own transaction, so it must take part in the transaction
	// its context carries, as the binding over the same store does. The
	// permission and role services read the repositories a section names.
	Repositories artifactsapi.Repositories
}

// Server holds the access module's two partial strict servers over one
// shared state: the composition root embeds Access in the access spec's
// strict server and Security in the artifactory spec's.
type Server struct {
	Access   *AccessService
	Security *SecurityAPI
}

// New returns the access partials over d. Without d.IDTokens it builds
// one verifier, which both partials share, so the providers' keys stay
// cached between exchanges for the life of the handler.
func New(d Deps) *Server {
	if d.IDTokens == nil {
		d.IDTokens = idtoken.New(idtoken.Options{})
	}
	st := accessState{deps: d}
	return &Server{Access: &AccessService{st}, Security: &SecurityAPI{st}}
}

// RegisterRoutes mounts the legacy token API's create and revoke
// (legacyTokenPath and legacyTokenRevokePath), whose form-encoded bodies the
// generated server cannot bind. The caller bounds their bodies, as it bounds
// the generated routes' bodies.
func (s *Server) RegisterRoutes(r chi.Router) {
	r.Post(legacyTokenPath, s.Security.createLegacyToken)
	r.Post(legacyTokenRevokePath, s.Security.revokeLegacyToken)
}

// PublicRoutes returns "METHOD path" of Access's routes that answer without
// credentials while anonymous access is off (accessPublicRoutes), in a new
// map to which the router adds the routes of its other modules
// (AuthnOptions.PublicRoutes).
func PublicRoutes() map[string]bool { return maps.Clone(accessPublicRoutes) }

// SlashCollections returns the access collection that the Terraform provider
// calls with a trailing slash, mapped to its path without one, for the
// router's rewrite of collection paths: the provider creates key pairs with
// POST /api/security/keypair/ (its KeypairEndPoint), which chi would
// otherwise read as a key pair with an empty name.
func SlashCollections() map[string]string {
	return map[string]string{
		"/artifactory/api/security/keypair/": "/artifactory/api/security/keypair",
	}
}

// RequestDetails are the access handlers' contributions to the request
// details the router keeps before a generated server decodes the body.
type RequestDetails struct {
	// Rewrite each rewrite a request's body in place, or leave it unchanged.
	Rewrite []func(*http.Request) error
	// RawBody each report requests whose raw body a handler reads.
	RawBody []func(*http.Request) bool
}

// DetailRules returns the access contributions: the policy writes, whose
// string-spelled booleans and numbers become JSON types (rewritePolicyBody),
// and the raw bodies of Access v2 permission creates (keepsPermissionBody)
// and group updates (keepsGroupUpdateBody).
func DetailRules() RequestDetails {
	return RequestDetails{
		Rewrite: []func(*http.Request) error{rewritePolicyBody},
		RawBody: []func(*http.Request) bool{keepsPermissionBody, keepsGroupUpdateBody},
	}
}
