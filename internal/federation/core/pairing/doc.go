// Package pairing pairs instances through the documented service_trust
// pairing endpoints and records each as the other's peer (decision 0021).
//
// # Role
//
// federation module, core layer. Service is the one pairing service the
// Artifactory and Access service_trust handlers and the peer API's /pair
// handler call. It imports the federation model and ports, the kernel's
// error sentinels and the platform's instance identity, outbox and unit of
// work, as decision 0044's dependency rules allow; the signed transport
// between instances is the Remote port, which the peer adapter implements.
//
// # Inputs
//
//   - For Issue, a use case and this instance's base URL; for Redeem, a use
//     case, a pairing token and this instance's base URL; for Pairings, a
//     use case; for Unpair, a use case, a peer's base URL and force.
//   - For Accept, a redeeming instance's nonce, instance id, base URL,
//     Ed25519 public key and protocol version, whose signature the peer
//     middleware has checked against that key; for Drop, a verified peer's
//     instance id and a use case; for Describe, a verified peer's record
//     and this instance's release version.
//   - For Probe, the peer records and the StatusRemote transport (Prober).
//   - The store (peers, pairing nonces, outbox cursors, instance identity),
//     the Remote transport, the administrator check, a clock and a random
//     source.
//
// # Outputs
//
//   - Pairing tokens: compact JWS (EdDSA) tokens carrying the issuer's base
//     URL, instance id and key fingerprint, the use case and a 256-bit
//     nonce, expiring after model.PairingTokenTTL.
//   - Peer records on both instances, with base URL, public key, protocol
//     version, use cases, status and last-seen time; their removal, with
//     their outbox cursors, when the last use case goes.
//   - Describe's PeerStatus, the peer API's GET /status answer; Probe's
//     liveness on each peer record: status, last-seen time and clock skew
//     (#1949).
//   - Refusal, 7.x's trust or state error, which the endpoints answer with
//     400 after 7.x's message pattern; ErrInvalidRequest and
//     ErrNonceRefused for the peer API's 400 and 403.
//
// # Invariants
//
//   - Only a nonce's SHA-256 is stored; tokens, nonces and private keys are
//     never stored, logged or returned beyond the token's single answer.
//   - A nonce pairs once: the issuer redeems it for one instance and key
//     (PeerStore.RedeemNonce), and the redeemer claims it before calling
//     the issuer, so a reused token, or one of two concurrent redemptions,
//     is refused. A redeemer's claim is released when its pairing fails.
//   - A redeem leaves both peer records or neither: the issuer records the
//     redeemer in the transaction that redeems the nonce, and a redeemer that
//     refuses the issuer's answer tells it to drop the record.
//   - An unredeemed token is never cancelled; it expires, and Prune clears
//     the expired nonces.
//   - P13 issues and accepts federated-repo/* and federated-repo/r only
//     (model.PairingFor); the handlers answer the other use cases.
//   - A peer is seen only when its signed status answer verifies, or, for
//     a peer this instance cannot reach (model.DeliveryPulled), when its
//     own status read does; UNAUTHORIZED, once recorded, lasts until an
//     answer verifies. Liveness writes touch only the liveness columns
//     (PeerStore.SetLiveness).
//
// # References
//
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Pairing", "Use cases", "Wire contract").
//   - Research 40: docs/research/40-federated-repositories-7x.md#5-service-trust-pairing.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the instance key is a sealed column).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules").
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 335.
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories.
//   - https://github.com/weaveplatform/artifactweave/issues/1926
//   - https://github.com/weaveplatform/artifactweave/issues/1949 (liveness).
//
// # Status
//
// Implemented P13 (PEER-02); liveness P13 (#1949).
//
// # Testing
//
//   - service_test.go: two in-memory instances joined by a Remote that
//     routes by base URL. TestUnitPairBothUseCases pairs for both use cases
//     and lists each side's pairings; TestUnitRedeemRefusals refuses
//     expired, reused, concurrent, tampered, wrong-fingerprint, other use
//     case, self and unreachable redeems with 7.x's messages and no peer
//     record; TestUnitRedeemChecksTheAnswer refuses each wrong issuer
//     answer; TestUnitAccept covers the issuer's validation, single use and
//     repeat; TestUnitUnpair drops trust on both sides, with force when the
//     peer is down, with the cursors of a peer left with none;
//     TestUnitStatusAndPrune, TestUnitAuthorityAndStoreFaults,
//     TestUnitStoreFaults, TestUnitDefaults, TestUnitVersions and
//     TestUnitTokenParse cover status derivation, pruning, refused callers,
//     store faults, defaults and the token encoding.
//   - liveness_test.go: TestUnitProbe records verified, unverified,
//     unreachable and refused reads, the clock skew and the stale window;
//     TestUnitProbePulledPeer, TestUnitDescribe, TestUnitProbeIntervals and
//     TestUnitProbeStoreFaults cover peers that pull, the status answer, the
//     intervals and store faults.
package pairing
