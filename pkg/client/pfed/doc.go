// Package pfed is the generated Go client for the Platform federation REST
// API: federations of peer instances and their membership, served under
// /pfed.
//
// # Role
//
// Generated client, outside the modules: a public package for programs that
// call artifactweave. make generate writes client.gen.go from
// api/pfed.openapi.yaml with api/oapi-client-pfed.yaml; this doc.go is
// hand-written and make generate leaves it alone.
//
// # Inputs
//
//   - api/pfed.openapi.yaml, which make bundle builds from api/src/pfed.
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
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Research 16: docs/research/16-enterprise-products.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan:
//     docs/implementation/plan.md#p1b-enterprise-products-specs-and-model.
//
// # Status
//
// Implemented P1b. make generate rewrites client.gen.go whenever the document
// changes, and make verify-generate fails while it is stale.
package pfed
