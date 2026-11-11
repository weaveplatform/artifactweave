// Package distribution is the generated strict chi server for the
// Distribution REST API: the release bundle distribution surface (bundles
// v1, distribution rules and jobs, signing keys and edge targets) served
// under /distribution.
//
// # Role
//
// platform layer, in platform/gen, where make generate writes the strict
// server of every specification. oapi-codegen v2.8.0 writes server.gen.go,
// the server with its models and embedded document, from
// api/distribution.openapi.yaml with api/oapi-server-distribution.yaml;
// tools/stubgen writes unimplemented.gen.go from its strict interface. This
// doc.go is the one hand-written file, and make generate leaves it alone.
// In the target layout the releases module's HTTP adapters implement the
// interface (P15), as a partial strict server composed over
// UnimplementedStrict (decision 0044). Of this repository's packages it
// imports only platform/httpkit/apierror.
//
// # Inputs
//
//   - api/distribution.openapi.yaml, which make bundle builds from the
//     distribution, release bundle, signing key, security and system
//     fragments in api/src/distribution. No operation in it is tagged
//     handwritten, so the generated mux serves every one.
//   - At run time: an implementation of StrictServerInterface and the
//     StrictHTTPServerOptions error handlers, for
//     NewStrictHandlerWithOptions; the ChiServerOptions (BaseURL,
//     BaseRouter, ErrorHandlerFunc), for HandlerWithOptions.
//
// # Outputs
//
//   - StrictServerInterface, one method per operation with its request and
//     response objects: release bundles v1 (create, update, sign, read,
//     delete, export and import), their distribution to edge nodes with
//     its status and trackers, the distribution of release bundles v2, GPG
//     signing keys, distribution permissions and tokens, and the service's
//     system, maintenance and support bundle operations. NewStrictHandler
//     and NewStrictHandlerWithOptions adapt an implementation to the
//     generated chi ServerInterface, and Handler, HandlerFromMux,
//     HandlerFromMuxWithBaseURL and HandlerWithOptions mount that on a chi
//     router.
//   - The models of the request and response bodies, with Valid on each
//     enum, and the embedded document (GetSpec, GetSpecJSON and
//     PathToRawSpec).
//   - UnimplementedStrict: every method returns apierror.ErrNotImplemented,
//     which the server renders as a 501 in the envelope JFrog Distribution
//     documents, with status_code, message and errors. A type that embeds
//     it satisfies StrictServerInterface, and each operation the type
//     implements shadows its stub.
//
// # Invariants
//
//   - The *.gen.go files are never edited by hand: make generate rewrites
//     both, and make verify-generate fails while either differs from what
//     the document gives (decision 0002). A change goes into
//     api/src/distribution or api/oapi-server-distribution.yaml.
//   - Servers embed UnimplementedStrict, not oapi-codegen's Unimplemented,
//     which serves the non-strict ServerInterface with a bare 501 and no
//     error envelope.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     ("Composition and HTTP").
//   - Research 16: docs/research/16-enterprise-products.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 164 and 165 (platform/gen), line 813 (the distribution row of
//     the generated code table), lines 819 to 823 (composition by
//     embedding) and line 1020 (the mapping row).
//   - Plan:
//     docs/implementation/plan.md#p1b-enterprise-products-specs-and-model,
//     tasks 1 and 3; #p15-release-bundles-v1-and-distribution;
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1001
//
// # Status
//
// Implemented P1b, which brought Distribution and release bundles v1 into
// scope with this document and its generated server (plan P1b, tasks 1 and
// 3); the files arrived in the baseline commit of phases P0 to P2. Since
// then only descriptions have changed, in the 7.x API audit (#430) and the
// rewording of the served contract (#434). The server's distribution
// service embeds UnimplementedStrict and implements nothing, so every
// operation answers 501 until P15 builds it. Staged in P9d.
//
// # Testing
//
// The package has no tests of its own, and .testcoverage.yml leaves it out
// of the coverage gate as generated code. The server's tests cover it:
//
//   - TestAccHandwrittenRoutesNotGenerated: the generated mux holds every
//     operation of the document.
//   - TestAccUnimplementedOperationsAnswer501: every operation answers 501
//     with the Distribution body the envelope table gives.
//   - TestAccAPIReliabilityLedger: the API reliability ledger, which names
//     this package's unimplemented.gen.go as the handler of every
//     operation, is current; make api-ledger rewrites it.
package distribution
