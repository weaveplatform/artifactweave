// Package peer signs and verifies the messages peers exchange: HTTP Message
// Signatures (RFC 9421) with Ed25519 (RFC 8032) over the method, target URI,
// Date, Content-Digest (RFC 9530) and a nonce, the protocol version, and the
// verified caller a peer operation authorizes against.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
//   - Signer: this instance's instance id and Ed25519 private key, a clock
//     and a nonce source.
//   - Verifier: the peer records (PeerKeys, which port.PeerStore satisfies)
//     whose keys verify requests and on which a protocol mismatch is
//     recorded; the replay cache (port.RequestNonceStore); a clock.
//   - A received request's method, reconstructed target URI, fields and
//     whole body, and for POST /pair the identity its body presents.
//
// # Outputs
//
//   - SignRequest and SignResponse set Artifactweave-Peer-Protocol, Date,
//     Content-Digest, Signature-Input and Signature. A request covers
//     "@method", "@target-uri", "date" and, with a body, "content-digest";
//     an answer covers "@status", "date", "content-digest" and the request's
//     "@method", "@target-uri" and "signature-input", which binds it to the
//     request's nonce. Every signature carries created, keyid (the instance
//     id), alg="ed25519" and a random nonce.
//   - VerifyRequest returns the calling peer or a refusal: ErrUnsigned,
//     ErrCoverage, ErrSkew (created or Date more than Skew, five minutes,
//     from the clock), ErrUnknownKey, ErrBadSignature, ErrDigest or
//     ErrReplay (the nonce was accepted within the window). The nonce is
//     remembered last, so a refused request never fills the cache.
//   - VerifyResponse does the same for an answer, against the key of the
//     peer called.
//   - ObserveVersion records the major version a verified caller sent when
//     its record holds another.
//   - WithCaller and Caller carry the verified peer in a request context.
//
// No error or message carries a signature, key or nonce.
//
// # References
//
//   - Decision 0021, Transport, Wire contract and Versioning:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - RFC 9421: https://www.rfc-editor.org/rfc/rfc9421; RFC 8941 (structured
//     fields): https://www.rfc-editor.org/rfc/rfc8941; RFC 9530:
//     https://www.rfc-editor.org/rfc/rfc9530.
//   - Ticket: https://github.com/weaveplatform/artifactweave/issues/1927.
//
// # Status
//
// Implemented P13 (PEER-03): request and answer signing, verification,
// replay and skew checks, and the protocol version.
//
// # Testing
//
// RFC 9421's Ed25519 test vector (appendix B.2.6) is reproduced exactly and
// verified; RFC 9530's sha-256 example is checked. Every refusal is tested
// with an injected clock, and concurrent verifications of one request
// accept exactly one under the race detector.
package peer
