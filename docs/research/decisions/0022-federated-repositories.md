# 0022: Federated repositories

Checked against: Artifactory 7.161.32 and the 7.161.26 source ([research 40](../40-federated-repositories-7x.md))

Status: Accepted

Implementation status (2026-11-24): **in progress, P13**. Members and
configuration sync are implemented (FED-01, [#1930](https://github.com/weaveplatform/artifactweave/issues/1930)):
member checks against paired peers, `federatedRepoUrlBase`, configSync over the
peer API, `migrate`, `convertToLocal`, `replaceUrl` and `updateMemberUrl`. Change
propagation is implemented (FED-02, [#1931](https://github.com/weaveplatform/artifactweave/issues/1931)):
the `repo:<key>` stream kind (`internal/federation/core/federated`, `Stream`), its
producer in the item service's transaction and its applier under the conflict rule.
Full sync and the member operations are implemented (FED-04,
[#1932](https://github.com/weaveplatform/artifactweave/issues/1932)): the repository's
snapshot and reconciler, `fullSync`, `forceFullSync`, `recovery`, `reconnect`,
`removeMember`, `prioritization` and `propagateCreatedTime`. The
rest of the decision below describes intended behavior that later P13 tasks
implement; their generated API stubs and storage models do not implement it yet.
A `receiver` member is refused until FED-03.
P37 owns vendor federation/RTFS interoperability and migration contracts. [0042](0042-capability-scope-and-supersession.md)
replaces permanent scope cuts; [research 10](../10-scope-and-tiers.md) records current
availability.

## Context

[0003](0003-repository-model.md) originally accepted `rclass: federated` only as a
configuration shim, treated as local when its only member was this instance, so the 37
`artifactory_federated_*` Terraform resources would apply. The owner brought federated
repositories into scope on 2026-10-13 with artifactweave-only peers
([research 16](../16-enterprise-products.md#4-federated-repositories)). The federation API
(`/artifactory/api/federation/**`) has 22 documented operations for migration,
synchronisation, status and failed-binary handling, and the repository configuration carries
`members[{url, mode, enabled}]`.

In Artifactory 7.x the federation runs in the standalone Artifactory Federation Service
(RTFS) at `/rtfs` (from 7.111.12). 7.133.3 announced the sunset of the legacy federation
embedded in Artifactory, and develops new features, such as unidirectional sync with the
`federated-repo/r` pairing use case, for RTFS only. RTFS does not support four status
operations: `state`, `status/repo` (v1 and v2), `status/mirrorsLag` and
`status/unavailableMirrors`. JFrog now files these under "Legacy Federation Monitoring"
([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed),
[API index](https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt)).

The 7.161.26 source serves about 70 federation paths, most of them calls between
Artifactory, RTFS, HA nodes and the RTFS migration tool. It also holds the member URL rules,
`federatedRepoUrlBase`, the `terraformbackend` exclusion and the status DTOs; the
documentation holds the receiving-only member contract
([research 40](../40-federated-repositories-7x.md)).

## Decision

**A federated repository is a local repository with members.** It stores and serves
content exactly like a local repository of its package type, accepts writes on every
bidirectional member, and keeps every member's content, properties and configuration
converged through the peer protocol ([0021](0021-peer-trust-and-sync-protocol.md)) on the
stream `repo:<key>`.

**Members.** Each entry in `members` names a peer's repository as
`<peer base URL>/artifactory/<repoKey>`, following 7.x's rules
([research 40 §3](../40-federated-repositories-7x.md#3-member-url-rules)):

- The repository key is the URL's last path element, and the base URL is what precedes it.
- This instance's own member is the one whose base URL is this instance's
  `federatedRepoUrlBase`, or `urlBase` when that is empty, normalised by trimming trailing
  slashes and appending `/artifactory` when missing, and whose key is the repository's.
  A member list without it gets it added, and reads return it resolved.
- `federatedRepoUrlBase` is a descriptor setting, in the XML and YAML forms, that overrides
  `urlBase` for member URLs and for the base URL a federated pairing token carries.
- With neither setting configured, a federated repository with members is refused with
  400 "Custom base URL must be configured for federated repositories' functionality",
  7.x's message.
- Every other member's base URL must belong to a paired peer. A bidirectional member needs
  `federated-repo/*`; a `receiver` member accepts `federated-repo/*` or `federated-repo/r`.
  Otherwise the request is refused with 400 naming the URL and the use case
  ([research 40 §10](../40-federated-repositories-7x.md#10-an-unpaired-member-in-a-terraform-run)
  gives the message and how it reads in a Terraform run).
- At most ten members, as 7.x
  ([members](https://docs.jfrog.com/artifactory/docs/federation-members-and-topology.md)).
- `enabled` is a boolean (JFrog documents a string; either is accepted) and maps to whether
  events flow to that member.
- `mode` is `bidirectional`, the default when absent, or `receiver`; any other value is
  refused with 400 naming the field.

When a federated repository is created or its member list changes, the instance sends the
configuration to each member over the peer protocol and the member creates or updates its
copy (`configSync`); a member that cannot be reached is reported as unavailable and retried.

**Receiver members.** A member with `mode: receiver` follows 7.x's receiving-only mode
([research 40 §6](../40-federated-repositories-7x.md#6-receiving-only-unidirectional-members)):

- It applies the events and fetches the blobs the other members send, and sends none. Its
  local writes are stored and served but never appended to the `repo:<key>` stream, as 7.x
  does not block them either; a later full sync from a sender reconciles them.
- It refuses `fullSync` and `forceFullSync` for that repository, and any change to the
  repository's members, with 400 naming receiver mode.
- Adding a receiver member, at create or update, starts a full sync to it.
- Status reports its mode as `Receiving_Only` in `connectionStates`.
- The peer relationship enforces the same direction when it was paired with
  `federated-repo/r` (0021), whatever the repository says.

**What propagates.** Deploys, deletes, moves and copies (expressed as the resulting
creates and deletes at the destination paths), property additions and removals, and,
when enabled, download statistics. Folder creation follows from item creation. The
package index ([0018](0018-package-index.md)) and the format metadata (Maven metadata,
npm packuments, Helm index and the like) are recomputed locally on each member from the
applied items rather than shipped, so every member's metadata reflects its own content.

**Conflicts** follow 0021: last writer by hybrid logical clock time per item path, origin
`instance_id` as the tie-break, tombstones for deletes.

How FED-02 carries this out, where no 7.x source fixes the detail [I] (#216 compares
them against a live 7.x in P19):

- Every entry is an item's whole state after the change: a put carries the content's
  checksums, the creation and modification times and users, and the item's whole
  property set; a delete carries the path. Whichever change wins therefore leaves the
  same item on every member, whatever order the entries arrive in. A move is a put at
  the destination and a delete at the source; a removed folder's files go first, then
  its folders deepest first, and a member deletes a folder only once it is empty.
- Each member keeps a stamp per item (`federation_item_stamps`): the clock time and
  origin of the change that left it so, a tombstone for a delete, kept for the outbox
  retention window. A local change first merges the stamp it replaces into the clock,
  so it orders after everything the instance knew of the item.
- A member applies a winning change without asking the format to admit it again (the
  member that made it did), then runs the format's deploy and property hooks, so its
  own metadata and package index follow; a delete goes to the trash can when it is on,
  as a client's would. A file where the change needs a folder, or the reverse, or a
  repository no longer federated here, is logged and skipped rather than stopping the
  stream.
- `enabled: false` pauses sending: no member sends to a disabled member, and a member
  whose own entry is disabled sends to none. The paused member stays subscribed, so
  its cursor holds within the retention window and it catches up from there once
  enabled again; receiving is not paused.
- Maven treats a federated repository as a local one for metadata, snapshot names and
  cleanup, since 7.x's `FederatedRepoDescriptor` extends `LocalRepoDescriptor`
  ([research 40](../40-federated-repositories-7x.md)).

**Operations.**

- `migrate/{repoKey}` turns a local repository into a federated one with the members
  given in a following configuration update; `convertToLocal` drops the members and the
  stream.
- `fullSync` and `forceFullSync` run the snapshot path of 0021 for one repository (the
  forced variant discards the receiver's divergent items); `?mirror` limits either to one
  member. `recovery` and `reconnect` re-establish the peer session and resume from the stored
  cursors.
- `removeMember` removes a member everywhere; `prioritization` sets HIGH or STANDARD,
  which orders the scheduler's dispatch of outbox batches; `propagateCreatedTime` makes
  full sync carry original creation times instead of the receiver's.

How FED-04 carries these out, where no 7.x source fixes the detail [I] (#216 compares
them against a live 7.x in P19):

- A full sync runs from the instance it is called on towards its members, as 7.x's full
  sync runs from the sender (research 40 §6): the instance queues, per member, a request
  that the member resynchronise from it (`POST /streams/{stream}/resync`, 0021), and the
  member runs and records the run. `?mirror` takes a member's base URL or member URL;
  one that names no member is 400. Both answer 200 with the documented object and
  neither of its optional fields, which jfrog-client-go ignores.
- The snapshot is the repository's files and folders in byte order of path, each under
  the stamp of the change that left it so, and the tombstones still kept. A file no
  change stamped, such as one stored before the repository was federated, is stamped
  with its modification time and the sending instance, and its content may be fetched.
  A normal run settles what both hold by the conflict rule and keeps what only the
  member holds; a forced run discards that too, with everything below a discarded
  folder. A tombstone pruned past the retention window is no longer in the snapshot,
  so only a forced run removes the members' copies of an item deleted during a
  partition longer than the window.
- `propagateCreatedTime` is the sender's instance-wide setting: while it is off, a
  snapshot carries no creation time, and the member gives an item a full sync creates
  the time it arrives; an item it already holds keeps its own.
- `recovery` queues, per repository in scope, a retry of delivery to each member whose
  last push failed, or to every member and from every member pulled from with
  `force=true`, run at once from the work queue; delivery resumes from each member's
  acknowledged cursor, so nothing applies twice. `reconnect` is `recovery` with
  `force` for every federated repository, as the peer client holds no session to
  re-establish, and answers `true`. Both, `removeMember`, `prioritization` and
  `propagateCreatedTime` answer messages of artifactweave's own wording.
- `removeMember` takes a base URL or member URL as its body, removes that member from
  every federated repository on the instance and sends each changed configuration to
  the remaining members. Every instance that drops a member forgets its cursor and any
  full sync with it on the repository's stream. The removed instance is not contacted:
  it keeps its copies and its configuration, and its events are refused as a
  non-member's.
- `prioritization` keeps at most ten HIGH repositories, in an instance setting; a HIGH
  repository's dispatchers and pulls are queued an hour ahead of now, so under
  contention they go before every STANDARD repository's queued within that hour.
- A receiver member added by FED-03 is sent a full sync through the same request
  (`Membership.QueueFullSync`).
- Status endpoints are computed from the outbox cursors, the peer records and the failed
  binary tasks: `SYNCING` when every member's cursor is within the lag threshold,
  `OUT_OF_SYNC` when one is behind, `FULL_SYNC_RUNNING` during a snapshot, `DISABLED`
  when the repository's federation is switched off. Lag is the time between an event's
  registration and its acknowledgement. artifactweave serves the four operations that
  Artifactory 7.x keeps for legacy federation only, since they are computed from the same
  cursors and a client written against legacy federation still calls them. The repository
  status body uses 7.161.26's `mirrorEventsStatusInfo` and `federatedArtifactStatus` fields
  ([research 40 §7](../40-federated-repositories-7x.md#status-payloads)).
- The failed-binary endpoints list, replay and delete the failed blob fetches recorded by
  0021 (FED-06, #1935), from the first failed attempt while retries remain queued
  ([I]; the public page names ten default attempts but not list visibility timing).
  Tasks are one per repository and SHA-256, ordered by SHA-256;
  list pages start at one with 1000 tasks by default and positive page sizes, with a
  next-page Link. The documented task fields carry SHA-1, repository, first-failure
  epoch milliseconds and failed-attempt count. `path` is an inferred extension naming
  the first still-pending path in byte order; a superseded task has no path. SHA-1 is
  captured from pending metadata when failure is first recorded, so a later deletion
  or replacement does not lose the filter identity. Old records lacking SHA-1 derive
  it from still-pending metadata when available.
- Replay resets attempts ([I], preserving artifactweave's existing spec; the public
  JFrog page promises marking tasks for reprocessing, not an attempt-counter reset)
  and marks every matching binary for immediate durable retry in the same current-
  authority transaction. The HTTP request makes no peer call. Pending backoff is
  cleared, parked work is revived, and an active lease is retained with a fresh run
  owed immediately after settlement. Workers fetch outside metadata transactions.
  An unavailable source remains recorded for automatic retry; `replayTasks` counts
  tasks marked for replay. An enqueue failure rolls back every reset and enqueue
  in that request. The engine publishes only still-current pending
  changes. Publication claims the failed row in its metadata transaction, so a task
  deleted while its download is in flight cannot publish. Publication rechecks current
  Receive authority for the stream: any still-authorized member from the prefetch
  candidate set permits digest-verified content to publish; if every candidate loses
  its trust or membership, the content stays pending with its failed task.
  A failed in-flight retry
  updates only an existing row and cannot recreate a deleted task. Deleting a task
  preserves the applied cursor and pending conflict stamp: the content stays absent
  until a newer event or full sync. Repository deletion/recreation and exit from
  the federated class clear the repository stream's failed rows in the same
  transaction as its pending stamps and membership, using a stream-scoped bulk
  deletion. Rollback retains them and other streams are untouched. Queued old
  retries then have no failed task to publish into the new repository lifetime.
  A normal full sync reconciles a same-stamp pending
  item too, rather than mistaking metadata delivery for complete content delivery.
- `binaries/tasks/{repoKey}` and `status/repoState/{repoKey}/binaries/` project
  `{failedTasks: <count>}` from the repository's failed-fetch records. Their DTO is
  closed source; this field and the shape are [I], marked `x-unverified` for #216.
  Task controls and `binaries/tasks` are administrator-only; `repoState` requires
  current Read on the repository. Missing repositories answer 404; existing
  non-federated repositories and invalid filters/paging answer 400 after authority.

**Endpoints only a separate federation service would serve.** artifactweave has no RTFS: it
presents itself as an instance whose federation service is connected and whose migration
is complete. `GET federation/rtfs` answers `true`, `GET federation/migration/status`
answers `MIGRATION_COMPLETED`, and the migration tool's writes answer every caller 400 with
7.x's message for a caller that is not the tool. The internal calls between Artifactory,
RTFS, HA nodes and the tool answer 404, as the peer protocol carries what they carry.
`fullSync?isDry` is refused with 400, as RTFS's comparison files have no public format,
and `compareResult` answers 404, as no comparison file exists (FED-04,
[DV-206 and DV-207](../../implementation/deviations.md#api-behaviour)).
[Research 40 §7](../40-federated-repositories-7x.md#7-federation-endpoints-and-how-artifactweave-answers-them)
classifies every path, and FED-05 registers the other deviations.

**Package types.** Federation is available for every package type artifactweave
implements as a local repository, except `terraformbackend`: 7.161.26 excludes it
(`unsupported.federation.packages.types`) and its documentation says a Terraform backend
repository cannot be federated. A federated `terraformbackend` create and a `migrate` of a
`terraformbackend` repository are refused with 400. Docker and OCI propagate manifests and
layers as items in the Artifactory storage layout, so tags converge with the items that
hold them.

**Terraform exit set.** P13 qualifies the 35 `artifactory_federated_*_repository`
resources of provider 12.11.14 whose local type artifactweave implements, listed in
[research 40 §9](../40-federated-repositories-7x.md#9-terraform-exit-set). It leaves out
`docker_v1` (Docker Registry V1 is superseded) and `releasebundles` (no local
implementation until P15); the provider has no federated `terraformbackend`.

## Rationale

Building federation on the item model rather than on format-specific replication keeps one
mechanism for every package type: formats already derive their metadata from items, so
shipping items and recomputing metadata avoids a second, per-format sync path. Requiring
paired peers for member URLs turns a typo into a create-time error instead of a silent
member that never syncs. Refusing an unknown `mode` follows the same reasoning: treating it
as bidirectional would send a site's writes to a member its operator meant to only
receive.

Presenting the embedded federation as a connected, migrated RTFS lets clients that probe
for RTFS take the current 7.x path, while legacy monitoring stays available from the same
cursors. Answering the internal calls 404 rather than imitating them avoids pretending to
hold queues, full-sync files and RTFS state that artifactweave does not have.

Alternatives considered:

- *Keep the local-only shim.* Withdrawn by the owner's scope decision.
- *Ship format metadata alongside items.* Metadata would race with local writes on the
  receiver and would have to be merged anyway.
- *Block local writes on a receiver.* Safer for drift, but 7.x documents that it does not
  block them, and clients would see a refusal 7.x never gives.

## Constraints

- Members must be artifactweave instances paired for `federated-repo/*` (or
  `federated-repo/r` for a receiver member).
- Pairing for federation uses JFrog's 7.x use cases on the Artifactory endpoint, as
  `api/src/artifactory/paths-admin.yaml` specifies
  ([pairing](https://docs.jfrog.com/artifactory/reference/generatepairingtoken.md)).
- Provider 12.11.14 has no `mode` attribute, so a receiver member is configured through the
  repositories API, not Terraform.
- A repository cannot be both federated and the target of push replication from the same
  source, to avoid two writers on one path set.
- The status DTOs' JSON names follow 7.161.26's field names, which carry no
  `@JsonProperty`; P19 confirms them (#216).

## Verification

- Acceptance in `acceptance-peers`: three members converge after deploys, deletes, moves
  and property changes made on each; a partitioned member catches up from its cursor; a
  member pruned past the window recovers by full sync; a receiver member converges and
  sends nothing back.
- Terraform: the 35 resources of research 40 §9 apply with three members, refresh to an
  empty plan, read back and destroy.
- Handler tests for the member URL rules, the paired-peer and ten-member refusals, `mode`,
  the `terraformbackend` refusal on create and `migrate`, and every answer in research 40 §7.
- Status endpoint tests driven by synthetic cursors.

## References

- [Research 40](../40-federated-repositories-7x.md); [research 16](../16-enterprise-products.md), section 4
- [03-repository-configuration-model](../03-repository-configuration-model.md#federated-fields)
- [0003](0003-repository-model.md), [0018](0018-package-index.md), [0021](0021-peer-trust-and-sync-protocol.md)
- <https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt>
- <https://docs.jfrog.com/artifactory/docs/configure-receiving-only-mode.md>
