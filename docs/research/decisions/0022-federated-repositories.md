# 0022: Federated repositories

Status: Proposed

## Context

[0003](0003-repository-model.md) originally accepted `rclass: federated` only as a
configuration shim, treated as local when its only member was this instance, so the 37
`artifactory_federated_*` Terraform resources would apply. The owner brought federated
repositories into scope on 2026-10-13 with artifactweave-only peers
([research 16](../16-enterprise-products.md#4-federated-repositories)). The federation API
(`/artifactory/api/federation/**`) has 22 operations for migration, synchronisation,
status and failed-binary handling, and the repository configuration carries
`members[{url, enabled}]`.

## Decision

**A federated repository is a local repository with members.** It stores and serves
content exactly like a local repository of its package type, accepts writes on every
member, and keeps every member's content, properties and configuration converged through
the peer protocol ([0021](0021-peer-trust-and-sync-protocol.md)) on the stream
`repo:<key>`.

**Members.** Each entry in `members` names a peer's repository as
`<peer base URL>/artifactory/<repoKey>`. A member URL whose host is not a paired peer with
the `federation` use case is rejected with 400 naming the URL. This instance's own entry
may be present or omitted. `enabled` is string-typed on the wire and maps to whether
events flow to that member. When a federated repository is created or its member list
changes, the instance sends the configuration to each member over the peer protocol and
the member creates or updates its copy (`configSync`); a member that cannot be reached is
reported as unavailable and retried.

**What propagates.** Deploys, deletes, moves and copies (expressed as the resulting
creates and deletes at the destination paths), property additions and removals, and,
when enabled, download statistics. Folder creation follows from item creation. The
package index ([0018](0018-package-index.md)) and the format metadata (Maven metadata,
npm packuments, Helm index and the like) are recomputed locally on each member from the
applied items rather than shipped, so every member's metadata reflects its own content.

**Conflicts** follow 0021: last writer by hybrid logical clock time per item path, origin
`instance_id` as the tie-break, tombstones for deletes.

**Operations.**

- `migrate/{repoKey}` turns a local repository into a federated one with the members
  given in a following configuration update; `convertToLocal` drops the members and the
  stream.
- `fullSync` and `forceFullSync` run the snapshot path of 0021 for one repository (the
  forced variant discards the receiver's divergent items); `recovery` and `reconnect`
  re-establish the peer session and resume from the stored cursors.
- `removeMember` removes a member everywhere; `prioritization` sets HIGH or STANDARD,
  which orders the scheduler's dispatch of outbox batches; `propagateCreatedTime` makes
  full sync carry original creation times instead of the receiver's.
- Status endpoints are computed from the outbox cursors, the peer records and the failed
  binary tasks: `SYNCING` when every member's cursor is within the lag threshold,
  `OUT_OF_SYNC` when one is behind, `FULL_SYNC_RUNNING` during a snapshot, `DISABLED`
  when the repository's federation is switched off. Lag is the time between an event's
  registration and its acknowledgement.
- The failed-binary endpoints list, replay and delete the failed blob fetches recorded by
  0021.

**Package types.** Federation is available for every package type artifactweave
implements as a local repository; Docker and OCI propagate manifests and layers as items
in the Artifactory storage layout, so tags converge with the items that hold them.

## Rationale

Building federation on the item model rather than on format-specific replication keeps one
mechanism for every package type: formats already derive their metadata from items, so
shipping items and recomputing metadata avoids a second, per-format sync path. Requiring
paired peers for member URLs turns a typo into a create-time error instead of a silent
member that never syncs.

Alternatives considered:

- *Keep the local-only shim.* Withdrawn by the owner's scope decision.
- *Ship format metadata alongside items.* Metadata would race with local writes on the
  receiver and would have to be merged anyway.

## Constraints

- Members must be artifactweave instances paired for `federation`.
- JFrog limits a federation to ten members; artifactweave keeps the same limit so plans
  written for Artifactory behave the same.
- A repository cannot be both federated and the target of push replication from the same
  source, to avoid two writers on one path set.
- The status payload fields `mirrorEventsStatusInfo` and `federatedArtifactStatus` are
  untyped in the oracle; artifactweave defines their contents and marks them unverified.

## Verification

- Acceptance in `acceptance-peers`: three members converge after deploys, deletes, moves
  and property changes made on each; a partitioned member catches up from its cursor; a
  member pruned past the window recovers by full sync.
- Terraform: `artifactory_federated_<type>_repository` for the in-scope types apply with
  three members, plan clean and destroy on every member.
- Status endpoint tests driven by synthetic cursors.

## References

- [Research 16](../16-enterprise-products.md), section 4
- [03-repository-configuration-model](../03-repository-configuration-model.md)
- [0003](0003-repository-model.md), [0018](0018-package-index.md), [0021](0021-peer-trust-and-sync-protocol.md)
- <https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt>
