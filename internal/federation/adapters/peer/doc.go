// Package peer serves and calls artifactweave's own peer API,
// /artifactweave/peer/v1, over signed HTTP (decision 0021, Transport).
//
// # Role
//
// federation module, adapter layer (peer protocol).
//
// # Inputs
//
//   - Server: a core/peer Verifier, this instance's Signer and a request
//     body bound.
//   - Factory and Client: this instance's Signer, the outbound proxy
//     (pushhttp.PlatformProxy), extra TLS roots, private origins granted to
//     peers, timeouts, body bounds and retry settings; a peer record's
//     instance id, base URL and public key.
//   - PairServer: the pairing service's issuing side (PairService); PairRemote:
//     a Factory.
//
// # Outputs
//
//   - Server.Middleware refuses every unverified request before the
//     handler: 401 in the flat envelope for a missing or failing signature,
//     an unknown key, a body that does not match Content-Digest, a time more
//     than five minutes off or a replayed nonce; 400 for a protocol major
//     version other than this build's, recorded on a verified caller's
//     record; 413 for a body over its bound. A verified request reaches the
//     handler with the caller in its context, and its answer is signed:
//     buffered, or streamed when the handler sets Content-Digest first.
//     POST /pair is verified with the key its body presents.
//   - TargetURI reconstructs the URI a peer signed from the request.
//   - Client.Do signs each request, verifies each answer's signature and
//     digest before returning it, bounds both bodies, and retries only safe
//     and natural operations (decision 0043) after a connection failure or
//     a 429, 502, 503 or 504. An answer that does not verify is an
//     *UnverifiedAnswerError. Client.API wraps the generated client
//     pkg/client/peer; WithAnswerKey and PairAnswerKey verify the answer to
//     POST /pair with the key it presents.
//   - PairServer serves POST /pair, redeeming a pairing nonce for the
//     instance whose identity the verified body presents (400 malformed, 401
//     not signed by that identity, 403 a nonce that is unknown, expired or
//     spent), and DELETE /pair, dropping a use case from the verified
//     caller's record (204). PairRemote is the pairing service's transport
//     (pairing.Remote): it calls both and sorts failures into unreachable,
//     unverified and a status the issuer refused with.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Tickets: https://github.com/weaveplatform/artifactweave/issues/1927,
//     https://github.com/weaveplatform/artifactweave/issues/1926 (pairing).
//
// # Status
//
// Implemented P13 (PEER-03): the server middleware and the signing client.
// Implemented P13 (PEER-02): /pair and the pairing transport. The other
// operations behind the middleware are PEER-04's, PEER-05's and FED-01's.
//
// # Testing
//
// Middleware tests send each refusal and check the handler never ran;
// client tests run against TLS servers with verified certificates, tamper
// with answers and count retries; internal/app sends signed requests
// between two running instances. pair_test.go pairs and unpairs two
// instances through their signed peer APIs and refuses spent nonces,
// unreachable and forged issuers and requests not signed by the instance
// they name.
package peer
