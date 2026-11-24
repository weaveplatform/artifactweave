# 0021: Peer trust and sync protocol

Checked against: Artifactory 7.161.32 and the 7.161.26 source ([research 40](../40-federated-repositories-7x.md))

Status: Accepted

Implementation status (2026-11-24): **planned P13**. The decision below describes
intended behavior; generated API stubs and storage models do not implement it.
P37 owns cross-vendor synchronization and separate cold-instance contracts; P40 owns
Mission Control/bridge/estate integration. [0042](0042-capability-scope-and-supersession.md)
replaces permanent scope cuts; [research 10](../10-scope-and-tiers.md) records current
availability.

## Context

Federated repositories, Distribution to edges, Access Federation and the topology views
all need two or more artifactweave instances to trust each other and exchange changes in
order. JFrog's equivalents (the federation service stream, the Distribution-to-Edge
transfer, Access Federation and the Circle of Trust) are not documented, and the project
owner initially decided on 2026-10-13 that the first implementation peers only with artifactweave
([research 16](../16-enterprise-products.md#what-is-not-reproducible)). The management
APIs that create the relationships (`service_trust/pairing`, federated repository
`members`, Access Federation targets, follower JPDs, distribution targets) keep JFrog's
paths and shapes. What travels between instances is ours to define, and it must be
concrete enough that the stores can model it before any of it is built.

The 7.161.26 source shows the whole service-trust surface: `pairings`, `pairing`, `revoke`
and `exchange`, the use-case patterns, and the bearer master token the redeeming instance
keeps for a year ([research 40 §5](../40-federated-repositories-7x.md#5-service-trust-pairing)).
The federation engine and RTFS stay closed source.

## Decision

**Identity.** Every instance has a stable `instance_id` (the `jfrt@<uuid>` value already
served by `/artifactory/api/system/service_id`) and an Ed25519 key pair generated at first
start. The private key is a sealed column ([0017](0017-sealed-columns-and-master-key.md));
the public key is published to paired peers only. A peer record holds the peer's
`instance_id`, base URL, public key, protocol version, granted use cases, status and
last-seen time.

**Pairing.** Pairing keeps 7.x's endpoints, bodies and messages
([research 40 §5](../40-federated-repositories-7x.md#endpoints)). Only the call between the
two instances is artifactweave's own:

1. An administrator on instance A calls `POST /artifactory/api/v1/service_trust/pairing/{usecase}`
   (or the Access equivalent). A returns `{pairing_token}`: a single-use token that expires
   after 300 seconds, 7.x's five minutes. It encodes A's base URL, `instance_id`,
   public-key fingerprint, the use case and a random nonce, and A signs it. For the
   federated-repository use cases the base URL is `federatedRepoUrlBase` without
   `/artifactory`, or `urlBase` when that is empty, as 7.x does.
2. An administrator on instance B calls `PUT .../pairing/{usecase}` with `{pairing_token}`.
   B refuses an expired token with 400 "Error while token pairing. Attempt to pair instances
   with an expired token", and otherwise calls A's peer endpoint
   `POST /artifactweave/peer/v1/pair` with the nonce, B's identity and B's public key,
   signed with B's key. This call takes the place of the 7.x redeemer's call to the issuer's
   `exchange`.
3. A checks the nonce is unused and unexpired, records B, and answers with its own public
   key and protocol version, signed. B checks the key matches the fingerprint in the token
   and records A. Both now hold each other as peers for that use case. B answers
   `{"message":"Trust with service federated-repo was established successfully"}`, 7.x's
   message, which names the use case.
4. `GET .../pairings/{usecase}` lists the peers holding the use case as `[{"base_url"}]`.
5. `DELETE .../pairing/{usecase}` with `{"base_url"}` tells that peer, in a signed
   `DELETE /artifactweave/peer/v1/pair`, to drop the use case, then drops it locally. With
   `?force=true` the local record goes even when the peer cannot be reached. It answers
   `{"message":"Trust with service federated-repo was canceled successfully"}`. A peer with no
   use cases left is removed and its outbox cursors are dropped.
6. `DELETE .../revoke/{usecase}` revokes the caller's own credential in 7.x when its scope
   names the use case. artifactweave gives peers no bearer credential, so no caller's token
   carries the scope, and it answers 200 `{"message":"Token was successfully revoked"}`
   without a change, as 7.161.26 does for such a caller.
7. `POST .../exchange` is where a 7.x redeemer presents a pairing token, and later its
   master token, to get a token. artifactweave peers sign requests instead
   (**Transport**), so no artifactweave token can be exchanged: it answers 400
   `{"message":"Error while token exchange. <reason>"}`, 7.x's pattern for a refused
   exchange. This is a deviation that PEER-02 registers.

**Use cases.** On the Artifactory endpoint they are 7.x's, as `ServiceTrustUseCase` matches
them:

- `federated-repo/*`: two-way federated-repository trust.
- `federated-repo/r`: receiving-only trust. The issuer (A) sends events on federated
  repository streams to the redeemer (B) and accepts none from it: A refuses B's event
  batches on those streams with 403, and B never sends on them. B still fetches blobs from
  A. This is 7.x's receiving-only master token, which keeps B's administrator from building
  federations that send back to A ([research 40 §6](../40-federated-repositories-7x.md#6-receiving-only-unidirectional-members)).
  When a pair holds both use cases, `federated-repo/*` applies.

`internal/federation/core/model` names the two-way use case `UseFederation = "federation"`;
PEER-02 renames it to `federated-repo/*` and adds `federated-repo/r`. The Access endpoint
keeps `platform-federation` (P14). artifactweave adds its own `distribution` (P15) and
`access-federation` (P14); 7.161.26's `ServiceTrustUseCase` also matches `distribution` on the
Artifactory endpoint, which its documentation does not list. One peer relationship can
carry several use cases.

P13 issues and accepts only `federated-repo/*` and `federated-repo/r`, on both the
Artifactory and the Access pairing endpoints. `platform-federation`, `access-federation`
and `distribution` keep today's answers (501) until P14 and P15 implement them. `mission-control` and `bridge` answer 501 and
`artifactory-cold`/`artifactory-cold/<namespace>` answer 404 `{"message":"Unknown use case"}`,
because P16 first uses an internal tier ([0024](0024-cold-storage-tier.md)). These are planned
transitional responses: P40 implements the Mission Control/bridge pairing contracts, and
P37 the separate cold-instance contract
([pairing](https://docs.jfrog.com/artifactory/reference/generatepairingtoken.md)).

7.x's master token (365 days, refreshed hourly within a 7-day grace) has no counterpart:
trust lasts until either side unpairs, and a peer whose signatures stop verifying shows as
`UNAUTHORIZED`.

**Transport.** Peer traffic is HTTPS on the normal listener under the
`/artifactweave/peer/v1/` prefix, which is artifactweave's own API, specified in its own
OpenAPI document and versioned independently of the JFrog surfaces. Every request and
every response is signed with HTTP Message Signatures (RFC 9421) using the sender's
Ed25519 key over the method, target path, `Content-Digest`, `Date` and a nonce. The
receiver rejects unknown keys, a clock skew above five minutes and replayed nonces
(cached for the skew window). TLS certificates are verified normally; peer signatures are
an additional layer, not a substitute for TLS.

**Ordered outbox.** Each instance appends every change a use case must propagate to an
outbox, per stream: one stream per federated repository (`repo:<key>`), one for Access
Federation (`access`), one per distribution target job. An outbox entry carries the
origin `instance_id`, a stream-local sequence number assigned at commit time in the same
transaction as the change, a hybrid logical clock timestamp, the event type, a JSON
payload and, for content events, the blob sha256 and size.

- The sender pushes batches to each peer subscribed to the stream:
  `POST /artifactweave/peer/v1/streams/{stream}/events` with `{origin, from_seq, events[]}`.
- The receiver applies events strictly in sequence. It records the highest applied
  sequence per `(origin, stream)` in the same transaction as the applied change, so a
  retried batch is idempotent. A batch that does not start at the next expected sequence
  is refused with 409 and the expected sequence, and the sender resumes from there.
- The receiver answers with the highest sequence it has durably applied; the sender
  stores it as the peer's acknowledged cursor.
- Content is pulled, not pushed: when an event references a blob the receiver does not
  hold, it fetches `GET /artifactweave/peer/v1/blobs/{sha256}` (signed) before applying
  the event. A failed fetch does not stop the stream: the item is recorded as a failed
  binary task, the cursor advances past the event and later events still apply. The item
  appears on that member only when a back-off retry or a replay (`binaries/failures/replay`)
  fetches the blob, and not at all if a later event has deleted or replaced it in the
  meantime.
- Outbox entries are pruned once every subscribed peer has acknowledged them or once they
  are older than the retention window (seven days by default). A peer whose cursor falls
  behind the pruned range needs a full sync.

**Full sync.** A receiver that is behind the window, or that an operator asks to resync,
requests a snapshot of the stream: `GET /artifactweave/peer/v1/streams/{stream}/snapshot`
returns a paginated listing of the current state (for a repository: path, sha256, size,
properties, timestamps and tombstones) taken at a stated sequence. The receiver diffs,
fetches missing blobs, applies the differences and then resumes the event stream from the
snapshot's sequence.

**Conflict rule.** Where a stream accepts writes on more than one instance (federated
repositories), each event carries its hybrid logical clock timestamp and origin. For the
same item, the event with the later timestamp wins and the higher `instance_id` breaks a
tie. Deletes are tombstones that win over older writes and are kept for the retention
window so a late event cannot resurrect a deleted item.

**Versioning.** The protocol version is exchanged at pairing and sent on every request as
`Artifactweave-Peer-Protocol: <major>.<minor>`. Minor versions are additive and both sides
ignore unknown fields and event types they do not subscribe to. A major mismatch is
refused with 400 and recorded on the peer record, which surfaces as an unavailable member
or an offline JPD in the status APIs.

## Rationale

Pairing through the existing endpoints keeps the operator workflow and the Terraform
shapes JFrog users already know, while the token carrying a key fingerprint means no
certificate authority is needed between instances. Signed requests on top of TLS give
mutual authentication without asking operators to provision client certificates, and
remove 7.x's long-lived bearer master token, its refresh job and the scoped-token
exchange; that is why `exchange` and `revoke` have nothing to act on. A per-stream outbox
written in the same transaction as the change is the simplest ordering guarantee that
survives crashes: nothing is propagated that did not commit, and nothing that committed is
lost. Pulling blobs separately keeps event batches small and lets checksum deduplication
skip content the receiver already holds.

Enforcing receiving-only trust at the peer record, not only on the repository's member
`mode`, gives the guarantee 7.x documents for `federated-repo/r`: the receiving site cannot
send back even if its administrator configures a two-way member.

Alternatives considered:

- *Reverse-engineering JFrog's protocols.* Rejected by the owner: undocumented, licence
  gated to obtain, and liable to change without notice.
- *Serving `exchange` as the redeem call.* It would keep 7.x's call between instances, but
  its body carries no public key and its answer is a bearer token artifactweave would then
  have to store, refresh and revoke.
- *mTLS with a private CA.* Strong, but every deployment would need a CA and certificate
  rotation; Ed25519 keys exchanged at pairing are enough for a set of peers that an
  administrator pairs by hand.
- *Using the existing replication API between peers.* Replication is one-way, unordered
  and polls; federation needs ordered multi-writer streams with acknowledgements.
- *A message broker.* An extra service contradicts the single-binary goal.

## Constraints

- Peers must reach each other's listener over HTTPS; an instance behind NAT can pull but
  cannot be pushed to. A pull mode (the receiver polls `.../events?after=`) is part of the
  protocol for that case.
- Clock skew above five minutes breaks request verification; operators run NTP.
- The outbox retention window bounds how long a peer can be offline before it needs a
  full sync; the window is configurable.
- The protocol is not interoperable with Artifactory and is not meant to be. A 7.x
  instance cannot redeem an artifactweave pairing token, and the reverse; P37 owns that.

## Verification

- Unit tests for token issue, single use, expiry and signature checks, and for request
  signing with replay and skew rejection.
- Handler tests for `pairings`, `pairing` (all three methods, with and without `force`),
  `revoke` and `exchange` answers and messages, and for both use cases, including a
  receiving-only peer's event batch refused with 403.
- Store contract tests for the outbox: sequence assignment in the change transaction,
  idempotent apply, cursor advance, pruning.
- Multi-instance acceptance scenarios in the `acceptance-peers` CI job: pair two and three
  instances, partition one, resume from cursor, force a full sync after pruning.

## References

- [Research 40](../40-federated-repositories-7x.md), sections 5 to 7; [research 16](../16-enterprise-products.md), sections 3 and 4
- [0017](0017-sealed-columns-and-master-key.md), [0020](0020-domain-model.md),
  [0022](0022-federated-repositories.md), [0023](0023-release-bundles-and-distribution.md),
  [0025](0025-platform-federation-and-topology.md)
- 7.161.26 `web/rest/.../servicetrust/ServiceTrustResource.java`,
  `backend/core/.../servicetrust/ServiceTrustUseCase.java`
- RFC 9421, HTTP Message Signatures: <https://www.rfc-editor.org/rfc/rfc9421>
- RFC 8032, Ed25519: <https://www.rfc-editor.org/rfc/rfc8032>
- Hybrid logical clocks: Kulkarni et al., "Logical Physical Clocks", 2014
