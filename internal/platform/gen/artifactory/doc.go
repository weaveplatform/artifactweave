// Package artifactory is the generated strict chi server for the Artifact
// repository REST API: the management surface served under /artifactory.
//
// # Role
//
// platform layer, in platform/gen, where make generate writes the strict
// server of every specification. oapi-codegen v2.8.0 writes server.gen.go,
// the server with its models and embedded document, from
// api/artifactory.openapi.yaml with api/oapi-server-artifactory.yaml;
// tools/stubgen writes unimplemented.gen.go from its strict interface. This
// doc.go is the one hand-written file, and make generate leaves it alone.
// In the target layout the interface spans six modules, each with a
// partial strict server: artifacts (storage, repositories, builds,
// searches, package types and trash), access (security), configuration
// (system and administration), federation (federation and replication),
// retention (cleanup and archive) and releases (release bundles). The app
// embeds the partials over UnimplementedStrict (decision 0044).
//
// # Inputs
//
//   - api/artifactory.openapi.yaml, which make bundle builds from the
//     fragments in api/src/artifactory, one per family.
//     api/oapi-server-artifactory.yaml leaves out the operations tagged
//     handwritten, which hand-written handlers serve because their paths
//     span segments or their responses change shape (decision 0002).
//   - internal/artifacts/api/repo and internal/kernel/wire, which the
//     document names through x-go-type-import: the repository
//     configuration schemas are repo.Config, and the blob mirror members
//     of the storage summary are []wire.BlobMirror, so the server decodes
//     into its own types rather than into generated copies.
//   - At run time: an implementation of StrictServerInterface and the
//     StrictHTTPServerOptions error handlers, for
//     NewStrictHandlerWithOptions; the ChiServerOptions (BaseURL,
//     BaseRouter, ErrorHandlerFunc), for HandlerWithOptions.
//
// # Outputs
//
//   - StrictServerInterface, one method per generated operation with its
//     request and response objects, across every family of the document,
//     from repositories and security to the package-type operations.
//     NewStrictHandler and NewStrictHandlerWithOptions adapt an
//     implementation to the generated chi ServerInterface, and Handler,
//     HandlerFromMux, HandlerFromMuxWithBaseURL and HandlerWithOptions
//     mount that on a chi router.
//   - The models of the request and response bodies, with Valid on each
//     enum, and the embedded document (GetSpec, GetSpecJSON and
//     PathToRawSpec).
//   - UnimplementedStrict: every method returns apierror.ErrNotImplemented,
//     which the server renders as a 501 in the envelope its path selects:
//     Artifactory's for most families, and the documented variants for
//     cold-storage retention and for import and export. A type that embeds
//     it satisfies StrictServerInterface, and each operation the type
//     implements shadows its stub.
//
// # Invariants
//
//   - The *.gen.go files are never edited by hand: make generate rewrites
//     both, and make verify-generate fails while either differs from what
//     the document gives (decision 0002). A change goes into
//     api/src/artifactory or api/oapi-server-artifactory.yaml.
//   - The generated mux never claims an operation tagged handwritten, so it
//     cannot shadow a route that a hand-written handler serves (decision
//     0002).
//   - Of this repository's packages it imports platform/httpkit/apierror
//     and, through x-go-type-import, artifacts/api/repo and kernel/wire.
//     The platform imports no module, so artifacts/api/repo is allowed
//     only as decision 0044's named exception for platform/gen (rule 2,
//     row 6 of TestUnitImportTiers); an x-go-type-import that names any
//     other module package fails that test.
//   - Servers embed UnimplementedStrict, not oapi-codegen's Unimplemented,
//     which serves the non-strict ServerInterface with a bare 501 and no
//     error envelope.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0003: docs/research/decisions/0003-repository-model.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     ("Composition and HTTP"; "Dependency rules", rule 2).
//   - Research 02: docs/research/02-artifactory-core-api-inventory.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 164 and 165 (platform/gen), line 810 (the artifactory row of
//     the generated code table), lines 819 to 835 (composition by
//     embedding) and line 1020 (the mapping row).
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas,
//     tasks 3 to 5; #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1001
//
// # Status
//
// Implemented P1, which authored the Artifactory document family by family
// and generated its server with the Access and Event servers (plan P1,
// tasks 3 and 4); the files arrived in the baseline commit of phases P0 to
// P2. Every phase since has added its families and operations, from P3's
// generic deploy and download (#60) to P9c's native package formats (for
// example #811 and #892); the 7.x audits (#423 to #430) and their fixes
// aligned the document with Artifactory 7.x, and decision 0043 added the
// temporary API restrictions and build append's idempotency key (#905).
// In P9d the x-go-type-import paths followed the repository and wire types
// to artifacts/api/repo and kernel/wire (#1131, #1143). The server
// implements the operations its handlers cover; the families later phases
// own answer 501, among them federation and replication (P11, P13),
// cleanup and archive (P11, P16), release bundles (P15) and most of system
// administration. Staged in P9d.
//
// # Testing
//
// The package has no tests of its own, and .testcoverage.yml leaves it out
// of the coverage gate as generated code. The server's tests cover it:
//
//   - TestAccHandwrittenRoutesNotGenerated: the generated mux holds every
//     operation of the document except those tagged handwritten.
//   - TestAccUnimplementedOperationsAnswer501: every operation no handler
//     implements answers 501 with the body the envelope table gives.
//   - TestAccAPIReliabilityLedger: the API reliability ledger, which names
//     this package's unimplemented.gen.go as the handler of each of those
//     operations, is current; make api-ledger rewrites it.
package artifactory
