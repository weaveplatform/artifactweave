# 0025: Platform federation and topology

Status: Proposed

## Context

JFrog synchronises identity across deployments with Access Federation, groups deployments
into platform federations (`/pfed`) and reports topology and synchronisation health
through Grid (`/grid`)
([research 16](../16-enterprise-products.md#3-platform-federation-and-topology)). All three
were brought into scope on 2026-10-13. In artifactweave every JPD in these APIs is an
artifactweave instance known through the peer protocol
([0021](0021-peer-trust-and-sync-protocol.md)).

## Decision

**JPD means peer.** Every JPD the APIs list or accept is a peer record (plus this instance,
reported with `local: true`). JPD status is derived from the peer's last successful
signed exchange and protocol compatibility: `ONLINE`, `OFFLINE` (no exchange within the
stale window), `PARTIAL` (some use cases failing), `UNAUTHORIZED` (signature or pairing
rejected). Location (`city_name`, `country_code`) and a display name are attributes of the
peer record set by the administrator.

**Access Federation.** A target registered through `/access/api/v1/system/federation`
must be a peer paired for `access-federation`. The source streams changes to users,
groups, permissions and tokens (the `entities` selected per target) on the `access`
stream, filtered by `permission_filters` and `group_filters` patterns. Sync is one way,
source to target; a target applies with the source as authority for the entities it
receives, and `force_override` replaces conflicting local entities at registration.
`full_broadcast` sends a snapshot (204 when sent, 304 when the target is already current),
`validate_server` checks reachability and pairing (204). A target silent beyond the stale
window stops receiving events until a full broadcast revives it. Secrets travel as they are
stored: password hashes, never plaintext, and token records without their JWTs, so a
federated token validates on the target by its id and signature key distribution.

**Platform federations.** A federation is a named set of connection groups, each listing
target JPD ids, and the entity types it federates. Adding a follower JPD
(`POST /pfed/api/v1/jpd`) performs the pairing of 0021 with the `platform-federation` use
case using the supplied pairing token. The entity types the federation API documents are
Curation entity types; artifactweave accepts and stores them so federations round-trip,
but they carry no data because Curation is out of scope. Identity replication is
configured through Access Federation (above) and repository replication through federated
repositories ([0022](0022-federated-repositories.md)), not through a federation's entity
list.

**Topology and monitoring.** A Grid topology is a named set of sites, each a JPD, with
status `Connected` or `Disconnected` from the peer status. Monitoring entities are derived
from the outbox cursors: for each synchronised entity and target, `success` when
acknowledged, `in_progress` when sent and unacknowledged, `pending` when queued, `failure`
when a delivery failed; the summary counts these per entity type.

## Rationale

Treating every JPD as a peer makes the three surfaces views and controls over one trust
and transport layer instead of three integrations. One-way identity sync mirrors JFrog's
documented direction and avoids the conflict rules a multi-master identity store would
need. Deriving monitoring from cursors gives accurate status without a separate
monitoring pipeline.

Alternatives considered:

- *Multi-master identity sync.* Rejected: a user disabled on one instance and edited on
  another would need conflict rules users cannot predict.
- *A separate topology registry.* Duplicates the peer records.

## Constraints

- Only artifactweave instances can be JPDs; Mission Control is out of scope, so licence
  buckets and JPD management through `/mc` stay unavailable.
- Token federation requires targets to trust the source's token signing key; the key is
  shared at pairing for the `access-federation` use case and rotation re-shares it.
- JFrog's documented conflict resolution for Access Federation is not public in detail;
  artifactweave's source-wins rule is its own.

## Verification

- `acceptance-peers`: a user, group, permission and token created on the source appear on
  targets within the lag threshold; filters exclude non-matching groups and permissions;
  a stale target recovers by full broadcast.
- Topology and monitoring endpoint tests driven by synthetic peer states and cursors.

## References

- [Research 16](../16-enterprise-products.md), section 3
- [0005](0005-authentication-and-tokens.md), [0006](0006-authorization-native-permission-model.md),
  [0021](0021-peer-trust-and-sync-protocol.md), [0022](0022-federated-repositories.md)
- <https://docs.jfrog.com/administration/docs/access-federation.md>
- <https://docs.jfrog.com/administration/docs/jfrog-grid.md>
