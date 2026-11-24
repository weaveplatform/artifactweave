// Package peer is the generated strict chi server for the Peer REST API:
// the protocol between paired instances (pairing, event streams with pull
// mode and snapshots, blobs, federated repository configuration and the
// liveness read) served under /artifactweave/peer/v1.
//
// # Role
//
// platform layer, in platform/gen, where make generate writes the strict
// server of every specification. oapi-codegen v2.8.0 writes server.gen.go,
// the server with its models and embedded document, from
// api/peer.openapi.yaml with api/oapi-server-peer.yaml; tools/stubgen
// writes unimplemented.gen.go from its strict interface. This doc.go is the
// one hand-written file, and make generate leaves it alone. The federation
// module's peer adapter (federation/adapters/peer) implements the interface
// as a partial strict server composed over UnimplementedStrict (decision
// 0044), operation by operation from PEER-02 on. Of this repository's
// packages it imports only platform/httpkit/apierror.
//
// # Inputs
//
//   - api/peer.openapi.yaml, which make bundle builds from the fragments in
//     api/src/peer. No operation in it is tagged handwritten, so the
//     generated mux serves every one.
//   - At run time: an implementation of StrictServerInterface and the
//     StrictHTTPServerOptions error handlers, for
//     NewStrictHandlerWithOptions; the ChiServerOptions (BaseURL,
//     BaseRouter, ErrorHandlerFunc), for HandlerWithOptions.
//
// # Outputs
//
//   - StrictServerInterface, one method per operation with its request and
//     response objects: redeem a pairing nonce and cancel a trust; push and
//     pull a stream's events and read its snapshot; fetch a blob; put a
//     federated repository's configuration; and read the liveness and
//     version. Every request object carries the signed headers
//     (Artifactweave-Peer-Protocol, Date, Signature, Signature-Input and,
//     with a body, Content-Digest) in its Params; the binder answers 400 when
//     one is missing. NewStrictHandler and NewStrictHandlerWithOptions adapt
//     an implementation to the generated chi ServerInterface, and Handler,
//     HandlerFromMux, HandlerFromMuxWithBaseURL and HandlerWithOptions mount
//     that on a chi router.
//   - The models of the request and response bodies (the event, batch,
//     acknowledgement, page, snapshot entry and error schemas among them),
//     UseCase with Valid, and the embedded document (GetSpec, GetSpecJSON
//     and PathToRawSpec).
//   - UnimplementedStrict: every method returns apierror.ErrNotImplemented,
//     which the server renders as a 501 in the flat envelope router.go
//     gives /artifactweave/peer/, a symbolic code and a message. A type that
//     embeds it satisfies StrictServerInterface, and each operation the type
//     implements shadows its stub.
//
// # Invariants
//
//   - The *.gen.go files are never edited by hand: make generate rewrites
//     both, and make verify-generate fails while either differs from what
//     the document gives (decision 0002). A change goes into api/src/peer or
//     api/oapi-server-peer.yaml.
//   - Servers embed UnimplementedStrict, not oapi-codegen's Unimplemented,
//     which serves the non-strict ServerInterface with a bare 501 and no
//     error envelope.
//   - The document has no published counterpart and stays outside the parity
//     oracle: its info carries x-artifactweave-extension, and no operation
//     carries x-oracle-source (TestAccPeerServiceOutsideParityOracle).
//   - Request verification and response signing are the peer middleware's
//     (internal/federation/adapters/peer), which runs before this server's
//     mux: the operations declare the signed request headers for clients,
//     and no response object declares the signed answer's.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md (the
//     operations' x-idempotency, x-cost and x-pagination classes).
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     ("Composition and HTTP").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 816 (the peer row of the generated code table).
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories,
//     PEER-01.
//   - https://github.com/weaveplatform/artifactweave/issues/1925
//
// # Status
//
// Implemented P13 (PEER-01): the document and its generated server. The
// router's peer service embeds UnimplementedStrict and serves POST and
// DELETE /pair (PEER-02); every other operation a paired peer signs answers
// 501, signed, until PEER-04, PEER-05 and FED-01 build them. An unsigned
// request is refused before it reaches this server (PEER-03).
//
// # Testing
//
// The package has no tests of its own, and .testcoverage.yml leaves it out
// of the coverage gate as generated code. The server's tests cover it:
//
//   - TestAccHandwrittenRoutesNotGenerated: the generated mux holds every
//     operation of the document.
//   - TestAccPeerOperationsSignedBetweenInstances: every operation, signed
//     by a paired instance's peer client, answers signed in the flat
//     envelope over SQLite and PostgreSQL: 501, or /pair's own answer.
//   - TestAccPeerOperationsRefuseUnsignedAtStartUp: unsigned, every
//     operation is refused 401 through the server run starts.
//   - TestAccPeerServiceOutsideParityOracle: the document stays outside the
//     parity oracle and inside the generated-mux check.
//   - TestAccAPIReliabilityLedger: the API reliability ledger, which names
//     this package's unimplemented.gen.go as the handler of every
//     operation, is current; make api-ledger rewrites it.
package peer
