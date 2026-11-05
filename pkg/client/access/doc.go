// Package access is the generated Go client for the Access REST API: the
// identity, token and project surface served under /access.
//
// # Role
//
// Generated client, outside the modules: a public package for programs that
// call artifactweave. make generate writes client.gen.go from
// api/access.openapi.yaml with api/oapi-client-access.yaml; this doc.go is
// hand-written and make generate leaves it alone.
//
// # Inputs
//
//   - api/access.openapi.yaml, which make bundle builds from api/src/access.
//   - The server URL and the ClientOption values given to NewClient or
//     NewClientWithResponses, such as WithHTTPClient.
//
// # Outputs
//
//   - Client, with one method per operation; ClientWithResponses, which also
//     decodes the responses; and the request and response models. Operations
//     tagged handwritten are left out (decision 0002).
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas.
//
// # Status
//
// Implemented P1. make generate rewrites client.gen.go whenever the document
// changes, and make verify-generate fails while it is stale.
package access
