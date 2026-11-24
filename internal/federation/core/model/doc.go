// Package model holds the federation module's entity values: replications
// and their runs, paired peers and pairing nonces, the stream names and
// conflict stamps of the peer protocol, federated repository membership and
// full syncs, and the platform federation records (Access Federation
// targets, platform federations and topologies).
//
// # Role
//
// federation module, core layer, entity values. The module's store ports
// take and return them, and the replication, peer, sync, federated
// repository, Access Federation and topology services of P11, P13 and P14
// will build on them. The package imports the standard library,
// artifacts/api/repo (a repository's class and its federation members),
// platform/outbox (the clock a Stamp carries) and retention/api (the status
// a full-sync run reports), each another module's api or the platform, as
// decision 0044's dependency rules allow.
// Private push snapshots use artifacts/api.TransferItem and kernel credential
// references; neither a snapshot nor a stored reference grants current access.
//
// # Inputs
//
//   - For DirectionFor, a repository class; for ParseMemberURL and
//     MembersOf, a federation member URL or a federated repository's
//     configuration with this instance's base URL.
//   - For the peer values, a time and a staleness threshold (DeriveStatus,
//     Usable); for ParseStream, a stream name; for DeriveSyncStatus, a
//     repository's settings, whether a full sync runs and its members' lag.
//
// # Outputs
//
//   - Replication, ReplicationRun, ReplicationDirection with DirectionFor,
//     and ReplicationStatus. ReplicationTargetState is a credential-free
//     repository history projection; it supplies no folder-level effect proof.
//   - PushRun/PushBinding and PushItem preserve private logical execution,
//     source/target intent and monotonic verification independently of the
//     queue and public replication status. Their validation grants no current
//     content or transport authority.
//   - PullRun/PullBinding and PullItem preserve selected upstream/cache facts,
//     immutable caller/source bindings and actual artifact-owner receipts.
//     They distinguish pending/failed/skipped work from successful cache and
//     deletion effects; persistence and dispatch belong to the owning services.
//   - ExecutionRun/ExecutionBinding link one private logical dispatch to its
//     exact transfer lifetime. ExecutionOutcomes derives truthful public status
//     without treating pending, failed or uncertain work as successful. These
//     Executions persists them independently of queue and target rows; the
//     values themselves do not schedule or dispatch a run.
//   - ExecutionEffect retains the exact transfer intent/version, sanitized
//     outcome and actual affected paths. ApplyExecutionEffect updates counters
//     once per observation; ExecutionPathState reports only matching evidence.
//   - Peer with Has, Compatible and DeriveStatus (a peer whose recorded
//     major protocol version differs from this instance's is OFFLINE, as an
//     unreachable one is, until it sends a compatible request), PeerStatus,
//     UseCase with KnownUseCase and Name, Pairing with PairingFor,
//     PairingNonce with Usable, and PairingTokenTTL.
//   - Stamp with After; StreamKind with RepositoryStream, AccessStream,
//     DistributionStream and ParseStream, and the sentinel ErrInvalidStream.
//   - FederationMember with ParseMemberURL, MembersOf and the sentinel
//     ErrInvalidMemberURL; FederationSettings, FederationPriority,
//     SyncStatus with DeriveSyncStatus, and FullSyncRun.
//   - AccessFederationTarget with Streams, FederatedEntity and
//     PatternFilter; PlatformFederation; Topology and TopologySite.
//
// # Invariants
//
//   - Conflicts resolve last writer wins: the later clock time, and on a
//     tie the greater origin instance id, so every member picks the same
//     winner (decision 0021).
//   - A pairing nonce completes at most one pairing, and only before it
//     expires; pairing tokens last PairingTokenTTL, JFrog's default
//     (decision 0021).
//   - Use cases are JFrog's names where they mean the same thing, such as
//     federated-repo/* and federated-repo/r, and artifactweave's own
//     otherwise; JFrog use cases with no counterpart are unknown, which the
//     API answers with 404. PairingFor is the one classification both
//     pairing endpoints answer by.
//   - Stream names are part of the stored outbox, so their forms never
//     change: repo:<key>, access and distribution:<tracker>:<peer>.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     (peers, pairing, streams and the conflict rule).
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md
//     (members, sync status and full syncs).
//   - Decision 0025: docs/research/decisions/0025-platform-federation-and-topology.md
//     (Access Federation targets, platform federations, topologies and peer
//     statuses).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules").
//   - Research 16: docs/research/16-enterprise-products.md. Research 15:
//     docs/research/15-data-model.md, lines 689 and 690 (replications),
//     lines 701 and 702 (peers and pairing nonces), lines 707 and 708
//     (members and full syncs), lines 719 to 721 (the platform federation
//     tables) and line 762 (the peers table as built).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton ahead of its phase), lines 331 to
//     346 (the federation module), lines 577 and 578 (each module owns its
//     store ports) and line 1058 (the shared types split into each module's
//     core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0,
//     #p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: the replication, peer, federation and platform values
// among the shared domain types (pull request 3). Moved here in P9d, where
// Stamp names the outbox's clock from its platform home. The behaviour that
// uses them arrives with P11 (replication), P13 (peer trust and federated
// repositories) and P14 (platform federation and topology).
//
// # Testing
//
//   - execution_test.go: actual-outcome status, unfinished/overflow refusal,
//     immutable lifetime/credential bindings and monotonic retry evidence.
//   - model_test.go: TestUnitDirectionFor (locals push, remotes pull,
//     virtuals do not replicate), TestUnitPeer (known and unknown use cases,
//     Has, the four statuses and a never-seen peer, nonce expiry and single
//     use), TestUnitStreams (stream names and their parse, and malformed
//     names failing with ErrInvalidStream), TestUnitStamp (the conflict
//     rule), TestUnitFederation (member URLs with and without a prefix,
//     malformed ones failing with ErrInvalidMemberURL, MembersOf skipping
//     this instance, and the four sync statuses) and
//     TestUnitPlatformFederation (PatternFilter and Streams).
//
// The metadata store's Replications, Peers, PairingNonces,
// FederationMembers, FullSyncs, AccessTargets, PlatformFederations and
// Topologies contract subtests store and read these values on its
// in-memory, SQLite and PostgreSQL stores.
package model
