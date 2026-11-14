// Package operations is the generated table of every operation in the seven
// bundled specs, with the chi route pattern the router serves it under and
// the classes it declares, and an Index that finds the operation a request
// addresses before routing.
//
// # Role
//
// platform layer, in platform/gen, where make generate writes what it
// derives from the specs. tools/optable (internal/devtools/optable) writes
// operations.gen.go, the Table, from api/*.openapi.yaml after oapi-codegen
// has generated the servers. This doc.go and operation.go, the Operation
// type and the Index, are hand-written, and make generate leaves them alone.
// Admission (internal/app, limits_cost.go) takes each request's cost class
// from it (RATE-01, #1407), and the Idempotency-Key gate (IDEM-01, #1406)
// will take the operations that accept a key (decision 0043).
//
// # Inputs
//
//   - api/<service>.openapi.yaml, through tools/optable: each operation's
//     method, path, operationId, x-handwritten, x-cost,
//     x-cost-expensive-query, x-idempotency, x-pagination and a reference
//     to the shared IdempotencyKey parameter, and its spec's server URL.
//   - At run time: the table for NewIndex, and a request's method and
//     escaped path for Find and FindRequest.
//
// # Outputs
//
//   - Table: one Operation per operation, sorted by pattern and method,
//     each with only the classes its spec declares set.
//   - Index: Find returns the operation a method and escaped path address
//     through chi's own tree (chi.Mux.Find), so a static segment beats a
//     parameter as it does in the router, and reports false for a request
//     no spec pattern matches.
//   - NewIndex's errors: ErrDuplicate for a method and pattern listed twice,
//     ErrPattern for a pattern chi cannot route.
//
// # Invariants
//
//   - operations.gen.go is never edited by hand: make generate rewrites it,
//     and make verify-generate fails while it differs from what the bundles
//     give (decision 0002). A class changes in api/src.
//   - Every pattern a generated server registers is a row's pattern for the
//     same method and operationId, and every row of an operation not marked
//     handwritten is registered by its generated server. A hand-written
//     operation keeps its spec pattern; where its hand-written route uses
//     another, TestAccAdmissionCostAgreesWithServedRoutes (internal/app)
//     checks that the two admit its requests under one class.
//   - A pattern is the spec's server URL, the BaseURL router.go mounts each
//     generated server under, followed by the spec path.
//   - A class is the spec's text as written: one of decision 0043's values,
//     or TODO(author) on an x-draft operation make scaffold wrote, which
//     TestAccAPIPrinciples allows nowhere else. A reader that meets a value
//     it does not know treats the operation as unclassified.
//
// # References
//
//   - Decision 0043, "Cost classes and how admission finds them" and "Replay
//     for keyed operations": docs/research/decisions/0043-api-reliability.md.
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1405
//
// # Status
//
// Implemented #1405 (CON-02): the table and the Index. RATE-01 (#1407)
// wires them into admission; IDEM-01 wires them into the key gate.
//
// # Testing
//
//   - TestUnitTableMatchesGeneratedServers: for each spec, the routes its
//     generated server registers under its base URL are exactly the table's
//     rows not marked handwritten.
//   - TestUnitIndexFindsEveryOperation: an Index over the Table finds each
//     row from a request path built from its pattern.
//   - TestUnitIndexFind: a static segment beats a parameter, an escaped
//     slash stays in its segment through FindRequest, and an unknown path
//     or method finds nothing.
//   - TestUnitNewIndexErrors: a row listed twice and a pattern chi refuses
//     fail.
package operations
