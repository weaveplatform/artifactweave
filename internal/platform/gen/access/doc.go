// Package access is the generated strict chi server for the Access REST API:
// the identity, token and project surface served under /access.
//
// # Role
//
// platform layer, in platform/gen, where make generate writes the strict
// server of every specification. oapi-codegen v2.8.0 writes server.gen.go,
// the server with its models and embedded document, from
// api/access.openapi.yaml with api/oapi-server-access.yaml; tools/stubgen
// writes unimplemented.gen.go from its strict interface. This doc.go is the
// one hand-written file, and make generate leaves it alone. In the target
// layout the access module's HTTP adapters implement the interface, and the
// federation module's adapters its federation paths (P14), as partial
// strict servers composed over UnimplementedStrict (decision 0044). Of this
// repository's packages it imports only platform/httpkit/apierror.
//
// # Inputs
//
//   - api/access.openapi.yaml, which make bundle builds from the identity,
//     projects, platform configuration, federation and system fragments in
//     api/src/access. api/oapi-server-access.yaml leaves out the three
//     operations tagged handwritten: the ping, readiness and liveness
//     probes, which hand-written handlers serve.
//   - At run time: an implementation of StrictServerInterface and the
//     StrictHTTPServerOptions error handlers, for
//     NewStrictHandlerWithOptions; the ChiServerOptions (BaseURL,
//     BaseRouter, ErrorHandlerFunc), for HandlerWithOptions.
//
// # Outputs
//
//   - StrictServerInterface, one method per operation with its request and
//     response objects: users, groups, permissions, tokens, roles and
//     environments; the OIDC, LDAP, SAML, Crowd and SCIM integrations;
//     projects; platform configuration; federation; and the system
//     version. NewStrictHandler and NewStrictHandlerWithOptions adapt an
//     implementation to the generated chi ServerInterface, and Handler,
//     HandlerFromMux, HandlerFromMuxWithBaseURL and HandlerWithOptions
//     mount that on a chi router.
//   - The models of the request and response bodies, with Valid on each
//     enum, and the embedded document (GetSpec, GetSpecJSON and
//     PathToRawSpec).
//   - UnimplementedStrict: every method returns apierror.ErrNotImplemented,
//     which the server renders as a 501 in the Access envelope, the one
//     JFrog's clients read under /access. A type that embeds it satisfies
//     StrictServerInterface, and each operation the type implements
//     shadows its stub.
//
// # Invariants
//
//   - The *.gen.go files are never edited by hand: make generate rewrites
//     both, and make verify-generate fails while either differs from what
//     the document gives (decision 0002). A change goes into api/src/access
//     or api/oapi-server-access.yaml.
//   - The generated mux never claims an operation tagged handwritten, so it
//     cannot shadow the hand-written probes (decision 0002).
//   - Servers embed UnimplementedStrict, not oapi-codegen's Unimplemented,
//     which serves the non-strict ServerInterface with a bare 501 and no
//     error envelope.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     ("Composition and HTTP").
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 164 and 165 (platform/gen), line 811 (the access row of the
//     generated code table), lines 819 to 823 (composition by embedding)
//     and line 1020 (the mapping row).
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas,
//     tasks 3 and 4; #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1001
//
// # Status
//
// Implemented P1, which authored the Access document and generated its
// server with the Artifactory and Event servers (plan P1, tasks 3 and 4);
// the files arrived in the baseline commit of phases P0 to P2. The document
// grew with P4's editable built-in roles (#103) and OIDC token exchange
// (#145) and with the user lock policy (#242); the 7.x audit (#425, #430)
// and its fixes aligned it with Artifactory 7.x, and decision 0043 capped
// the identity pages (#905). The server implements the users, groups,
// permissions, tokens, roles, environments, OIDC, token and security
// configuration and version operations; the others answer 501, such as
// projects (P11), LDAP (P12), federation (P14), SAML (P21) and SCIM (P22).
// Moved here in P9d.
//
// # Testing
//
// The package has no tests of its own, and .testcoverage.yml leaves it out
// of the coverage gate as generated code. The server's tests cover it:
//
//   - TestAccHandwrittenRoutesNotGenerated: the generated mux holds every
//     operation of the document except the three hand-written probes.
//   - TestAccUnimplementedOperationsAnswer501: every operation no handler
//     implements answers 501 with the body the envelope table gives.
//   - TestAccAPIReliabilityLedger: the API reliability ledger, which names
//     this package's unimplemented.gen.go as the handler of each of those
//     operations, is current; make api-ledger rewrites it.
package access
