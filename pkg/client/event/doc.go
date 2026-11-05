// Package event is the generated Go client for the Event REST API: the
// webhook subscription surface served under /event.
//
// # Role
//
// Generated client, outside the modules: a public package for programs that
// call artifactweave. make generate writes client.gen.go from
// api/event.openapi.yaml with api/oapi-client-event.yaml; this doc.go is
// hand-written and make generate leaves it alone.
//
// # Inputs
//
//   - api/event.openapi.yaml, which make bundle builds from api/src/event.
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
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Research 07: docs/research/07-webhooks-retention-projects.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas.
//
// # Status
//
// Implemented P1. make generate rewrites client.gen.go whenever the document
// changes, and make verify-generate fails while it is stale.
package event
