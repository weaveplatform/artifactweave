// Package optable writes the operation table: every operation of the
// bundled specs with the chi route pattern the router serves it under and
// the classes it declares (x-cost, x-cost-expensive-query, x-idempotency,
// x-pagination, and whether it declares the shared IdempotencyKey). Admission
// (RATE-01) and the Idempotency-Key gate (IDEM-01) look a request up in it
// before routing, so the spec stays the one place the classes are declared.
//
// # Role
//
// devtools layer: the library behind tools/optable, which make generate runs
// after the strict servers are generated; never linked into the server
// binary. It imports only the standard library and gopkg.in/yaml.v3.
//
// # Inputs
//
//   - The bundled specs, api/<service>.openapi.yaml: each one's
//     servers[0].url, the base path router.go mounts its generated server at,
//     and every operation of its paths with its extensions and parameters.
//
// # Outputs
//
//   - Load: one Row per operation, sorted by pattern and method.
//   - ChiPattern: the pattern a generated chi server registers for a spec
//     path under its base.
//   - Render: the formatted Go source of the table, a var Table of
//     operations.Operation literals that sets only the fields a spec
//     declares.
//   - Run: that source written to the given file, in the package its
//     directory names, and the number of operations.
//   - An error for a directory with no spec, a spec that does not parse, is
//     not a mapping or has no server URL, two operations with one method and
//     pattern, and a file that cannot be written.
//
// # Invariants
//
//   - The table is generated and committed: make generate rewrites it, and
//     make verify-generate fails when it differs from what the bundles give
//     (decision 0002).
//   - A pattern is the base path and oapi-codegen's own conversion of the
//     spec path (SwaggerUriToChiUri), so it equals what the generated server
//     registers; the operations package's tests compare the two for every
//     generated operation.
//   - IdempotencyKey is true only for a reference to the shared parameter,
//     #/components/parameters/IdempotencyKey, on the operation or its path
//     item.
//
// # References
//
//   - Decision 0043, "Cost classes and how admission finds them" (the table
//     and the chi lookup) and "Replay for keyed operations" (the keyed
//     operations): docs/research/decisions/0043-api-reliability.md.
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1405
//
// # Status
//
// Implemented #1405 (CON-02). RATE-01 (#1407) and IDEM-01 (#1406) read the
// table at run time.
//
// # Testing
//
//   - TestUnitLoadAndRender: a fixture spec's operations become rows with
//     their patterns, classes and key flag, sorted, and render as Go source
//     that sets only the declared fields.
//   - TestUnitRun: the table is written in the package of its directory.
//   - TestUnitErrors: each error above.
package optable
