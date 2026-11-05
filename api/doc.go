// Package api holds the authored OpenAPI documents (decision record 0002) and
// the parity test that checks them against the gitignored cache of JFrog's
// published definitions.
//
// # Role
//
// Public API specification, outside the modules: the documents are the
// source of the generated strict servers in internal/server/gen and of the
// public clients in pkg/client, and the package keeps its path through the
// modular-monolith migration.
//
// # Inputs
//
//   - The fragments under src/<service>/, which make bundle merges into
//     <service>.openapi.yaml. They are inputs and are not embedded.
//   - The oapi-codegen configurations oapi-server-<service>.yaml and
//     oapi-client-<service>.yaml, which make generate reads.
//   - For the parity test: JFrog's published definitions in the gitignored
//     .specoracle cache (make oracle-fetch), and parity-baseline.json, the
//     gaps the gate accepts.
//
// # Outputs
//
//   - Specs and SpecFiles: the bundled documents, embedded so the server can
//     register a 501 route for every hand-written operation that has no
//     handler yet. The generated strict servers get that from stubgen;
//     hand-written routes need the spec itself.
//   - The documents that make generate turns into internal/server/gen and
//     pkg/client; make verify-generate fails while those are out of date.
//
// # References
//
//   - Decision 0002: docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0001: docs/research/decisions/0001-mirror-the-jfrog-api-surface.md.
//   - docs/implementation/api-description-conventions.md.
//   - Research 01: docs/research/01-jfrog-api-landscape.md (the oracle).
//   - Plan: docs/implementation/plan.md#p0-foundations,
//     #p1-data-model-and-api-schemas and
//     #p1b-enterprise-products-specs-and-model.
//
// # Status
//
// Implemented P0 (the bundle pipeline and the embedding), P1 and P1b (the
// in-scope surface of the seven services); every later phase to P9c added
// its operations.
//
// # Testing
//
//   - TestAccSpecParity: the documents keep parity with JFrog's published
//     operations beyond what parity-baseline.json accepts. It skips without
//     the oracle cache; the parity job in CI fetches it.
package api
