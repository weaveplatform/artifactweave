// Package artifactory is the generated Go client for the Artifact repository
// REST API: the management surface served under /artifactory.
//
// # Role
//
// Generated client, outside the modules: a public package for programs that
// call artifactweave. make generate writes client.gen.go from
// api/artifactory.openapi.yaml with api/oapi-client-artifactory.yaml; this
// doc.go is hand-written and make generate leaves it alone.
//
// # Inputs
//
//   - api/artifactory.openapi.yaml, which make bundle builds from
//     api/src/artifactory.
//   - The server URL and the ClientOption values given to NewClient or
//     NewClientWithResponses, such as WithHTTPClient.
//   - internal/artifacts/api/repo and internal/kernel/wire, which the
//     document names through x-go-type-import, so repository configurations
//     and wire models are the server's own types.
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
//   - Decision 0003: docs/research/decisions/0003-repository-model.md.
//   - Research 02: docs/research/02-artifactory-core-api-inventory.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas.
//
// # Status
//
// Implemented P1. make generate rewrites client.gen.go whenever the document
// changes, and make verify-generate fails while it is stale.
//
// # Testing
//
//   - ExampleClient_AppendBuildWithBody: a retry that protects only build
//     append. The client does not infer retry safety from an HTTP method or
//     retry every failed write (decision 0043).
//   - ExampleClientWithResponses_GetSystemVersionWithResponse_rateLimited:
//     reading a typed 429's Retry-After, RateLimit and RateLimit-Policy,
//     which every operation admission charges declares (decision 0043).
package artifactory
