// Package peer is the generated Go client for the Peer REST API: the
// protocol between paired instances, served under /artifactweave/peer/v1.
//
// # Role
//
// Generated client, outside the modules: a public package for programs that
// call artifactweave, and the client the federation module's signed peer
// client wraps (PEER-03). make generate writes client.gen.go from
// api/peer.openapi.yaml with api/oapi-client-peer.yaml; this doc.go is
// hand-written and make generate leaves it alone.
//
// # Inputs
//
//   - api/peer.openapi.yaml, which make bundle builds from api/src/peer.
//   - The server URL and the ClientOption values given to NewClient or
//     NewClientWithResponses, such as WithHTTPClient and
//     WithRequestEditorFn, through which a caller signs each request.
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
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories,
//     PEER-01.
//
// # Status
//
// Implemented P13 (PEER-01). make generate rewrites client.gen.go whenever
// the document changes, and make verify-generate fails while it is stale.
package peer
