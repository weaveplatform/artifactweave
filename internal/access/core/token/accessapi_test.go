package token

import accessapi "github.com/weaveplatform/artifactweave/internal/access/api"

// The access/api requests carry every field of the service's requests, in
// the same order and of the same types, so the access module binds one to
// the other by conversion and issuance keeps every rule. A field added to
// one and not the other stops these conversions compiling.
var (
	_ = Request(accessapi.TokenRequest{})
	_ = RegistryRequest(accessapi.RegistryTokenRequest{})
)
