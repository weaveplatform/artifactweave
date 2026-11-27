# 40: Federated repositories and peer trust in Artifactory 7.x

Checked against: Artifactory 7.161.32 (documentation) and the 7.161.26 OSS source archive,
read on 2026-11-24 for P13. **[KB]** and **[J]** are not cited.

This report records what the 7.x sources show about federated repositories and the
service-trust pairing they rely on, beyond what [research 16 §4](16-enterprise-products.md#4-federated-repositories)
and [research 03](03-repository-configuration-model.md#federated-fields) already hold. It
classifies every federation endpoint 7.161.26 serves for P13, records the receiving-only
(unidirectional) member contract, and derives the Terraform resources P13's exit qualifies.
Decisions [0021](decisions/0021-peer-trust-and-sync-protocol.md) and
[0022](decisions/0022-federated-repositories.md) hold the design; the
[plan](../implementation/plan.md#p13-peer-trust-and-federated-repositories) holds the tasks.

| Label | Means |
|---|---|
| **[S]** | seen in source: the 7.161.26 archive at the cited file and line, or a JFrog client at the cited tag |
| **[D]** | documented at the cited JFrog page |
| **[I]** | inferred; no source states it. Listed on [#216](https://github.com/weaveplatform/artifactweave/issues/216) for P19 |

## Sources

- The [official 7.161.26 OSS source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz),
  SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`. Paths below are
  relative to its root. It was read only; nothing in it was built or run.
- The [Federated Repositories API index](https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt)
  and the pages [Configure Receiving-Only Mode](https://docs.jfrog.com/artifactory/docs/configure-receiving-only-mode.md)
  (updated 2026-10-27), [Migrate Push Replication to Unidirectional Federation](https://docs.jfrog.com/artifactory/docs/migrate-push-replication-to-unidirectional-federation.md)
  (2026-08-17), [Federation Members and Topology](https://docs.jfrog.com/artifactory/docs/federation-members-and-topology.md)
  (2026-07-17), [RTFS](https://docs.jfrog.com/artifactory/docs/artifactory-federation-service-rtfs.md)
  (2026-11-13) and [Generate Pairing Token](https://docs.jfrog.com/artifactory/reference/generatepairingtoken.md)
  (2026-03-31).
- `jfrog/terraform-provider-artifactory` at
  [v12.11.14](https://github.com/jfrog/terraform-provider-artifactory/tree/v12.11.14), the
  pin in `deploy/artifactweave/terraform/cdn-repositories/versions.tf`.
- `jfrog/jfrog-client-go` at [v1.55.0](https://github.com/jfrog/jfrog-client-go/tree/v1.55.0)
  and `jfrog/jfrog-cli-core` at [v2.60.0](https://github.com/jfrog/jfrog-cli-core/tree/v2.60.0),
  the versions in the [research baseline](README.md#versions-at-the-research-baseline).

## 1. What the source holds and what it does not

- **The federation engine is closed source [S].** Every `Default*Impl` in
  `backend/core/src/main/java/org/artifactory/federation/` (`DefaultFederatedRepoServiceImpl`,
  `DefaultFederatedStatusServiceImpl`, `DefaultFederatedFullSyncRepoServiceImpl`,
  `DefaultFederatedBinaryTasksServiceImpl`, `DefaultFederatedReplaceURLServiceImpl`,
  `DefaultFederatedRepoPropertiesServiceImpl`) throws "Unsupported operation: federated",
  and `backend/core/src/main/java/org/artifactory/addon/CoreAddonsImpl.java:470`
  `createFederatedRepo` throws "Unsupported repository type: federated".
- **RTFS is an external client [S].** The REST resources import `org.jfrog.rtfs.*` DTOs
  (`web/rest/.../federation/FederatedRepoResource.java:64-73`), and
  `backend/core/.../federation/FederationServiceProxy.java:8-29` routes each call either to
  the RTFS service or to the legacy embedded federation.
- **What is open:** the REST resources, the descriptor and XSD, the member URL utilities,
  the service-trust pairing and its token specifications, the tuning constants and the
  status DTOs. Sections 2 to 6 record them.
- **`/api/v1/mirroring` does not exist** in 7.161.26 [S]: no source file names it.

## 2. Descriptor and repository JSON

| Item | Fact | Source |
|---|---|---|
| `FederatedRepoType` | Extends `LocalRepoType`. Elements: `federatedMembers` (wrapper) of `federatedMember`, `proxyRef` (IDREF), `disableProxy`, `modificationDate` (long, "Configuration last modificationDate"), `maxNumberOfThreads` (integer, "The max number of threads handling async operations triggered by federation events") | [S] `base/config/.../descriptor/repo/FederatedRepoDescriptor.java:29-52`; `base/config/src/main/resources/artifactory.xsd:503-545` |
| `FederatedMemberType` | `url` (required), `mode` (string, "Member replication mode bidirectional or unidirectional"), `enabled` (boolean, default `true`, "When false - the local mirror pausing federating events to remote mirrors") | [S] `base/config/.../descriptor/repo/FederatedMember.java:12-25`; `artifactory.xsd:3541-3565` |
| Member JSON | `{url, mode, enabled}`, `enabled` a `Boolean` defaulting to `true`; the repository JSON key is `members` | [S] `base/papi/.../repo/FederatedMemberConfiguration.java:5-19` |
| `mode` values on the wire | `bidirectional` (the default) and `receiver`. The XSD's description says "unidirectional"; the REST example sends `"mode": "receiver"` | [D] Configure Receiving-Only Mode, "How Transmission Mode Works" and Step 2 |
| `federatedRepositories` | The descriptor's list of federated repositories, of `FederatedRepositoriesType` | [S] `artifactory.xsd:142`, `:483` |
| `federatedRepoUrlBase` | "A hard-coded URL prefix used by federated repository as the local member URL base"; it overrides `urlBase` for member URLs, default empty | [S] `artifactory.xsd:211` |
| `gridTopologyId` | On every repository type: "Used to link repositories to a shared grid topology". Grid topology is P14 ([0025](decisions/0025-platform-federation-and-topology.md)) | [S] `artifactory.xsd:730-736` |
| No access token | Neither the descriptor member nor the REST member carries a credential | [S] the three files above |

Which of these the repositories API carries as well as the XML descriptor [S]:

| Field | XML descriptor | REST JSON | Source |
|---|---|---|---|
| `members[].url`, `members[].mode`, `members[].enabled` | yes (`federatedMember`) | yes, under `members`, for local and federated repositories when non-empty; the self member's URL is resolved | `base/common/.../repo/LocalRepositoryConfigurationImpl.java:130-136`, `FederatedRepositoryConfigurationImpl.java:38-55`; the key `members` in `web/rest/.../util/DataDiffsToRepoConfigChangesYamlParser.java:281` |
| `proxy` | yes (`proxyRef`) | yes, only while `disableProxy` is false | `LocalRepositoryConfigurationImpl.java:137-141`; `FederatedRepositoryConfigurationImpl.java:32-33`, `:47-50` |
| `disableProxy` | yes | yes | `LocalRepositoryConfigurationImpl.java:142-146`; `FederatedRepositoryConfigurationImpl.java:34-35` |
| `gridTopologyId` | yes, on every repository type | yes, for local and federated repositories when non-empty | `RepositoryConfigurationBase.java:164-166` |
| `maxNumberOfThreads` | yes | no | no REST configuration class declares it |
| `modificationDate` | yes | no | as above |

artifactweave's repositories API therefore adds `mode` in P13 (FED-01), keeps `proxy` and
`disableProxy` as today, and leaves `maxNumberOfThreads` and `modificationDate` to the XML
descriptor. `gridTopologyId` belongs to P14.

Receiver mode qualification uses the repositories API and YAML descriptor
application, which preserve `members[].mode` in the repository configuration
store. GET XML configuration intentionally retains empty repository sections,
as 7.161.26's `CentralConfigServiceImpl.getFilteredDescriptor` and
`cleanupConfigDescriptorRepositories` do (CFG-01, #1893; decision 0009).
XML import/replacement remains P36. FED-03 does not add repository entries to
that export or claim an XML repository round trip.


## 3. Member URL rules

From `base/common/src/main/java/org/artifactory/util/FederatedMemberUtils.java` [S]:

1. **The repository key is the last path element** of a member URL (`:127`, `:145`).
2. **The self member** is stored as `<base-url-placeholder>/<repoKey>` (`:95-99`;
   `BASE_URL_PLACEHOLDER` in `base/config/.../repo/federated/FederatedMember.java:20`) and is
   added when a member list lacks it (`membersWithSelfMember`, `:107-113`). It is resolved
   on read against `federatedRepoUrlBase`, or `urlBase` when that is empty
   (`getFederatedRepoUrlBaseOrDefault`, `:49`).
3. **`/artifactory` is appended** to a base URL that lacks it, after trailing slashes are
   trimmed (`normalizeBaseUrl`, `:76-79`).
4. **A member is this instance's own** when its URL holds the placeholder or the base URL and
   its last path element is the repository key (`:126-129`).
5. **A custom base URL is required.** Without one, `isOldFormatLocal` throws "Custom base URL
   must be configured for federated repositories' functionality" (`:140-143`). The HTTP status
   that reaches a client is **[I]**.

The provider's documentation adds the operator rule: the member list "must contain this
repository URL (configured base URL `/artifactory/` + repo `key`)", and every member needs a
base URL set ([S] provider 12.11.14 `docs/resources/federated_generic_repository.md:33-34`).
A member's repository on another instance is not deleted when the repository that names it
is ([S] the same page, `cleanup_on_delete`, line 41).

7.x documents creating a federation with `rclass: "local"` and a `members` list, and says the
UI's **Multi-site** tab of a local repository selects federation mode ([D] Configure
Receiving-Only Mode, "Create a New Federated Repository with Receiver Members" and Step 2A).
7.161.26's local configuration model carries `members`, `proxy` and `disableProxy` for that
reason ("Federation fields - exposed for local repos with federation capability",
[S] `LocalRepositoryConfigurationImpl.java:130-146`). What `rclass` the read then returns is
**[I]**.

## 4. Package types

- **`terraformbackend` is the only excluded type [S].**
  `base/config/src/main/java/org/artifactory/common/ConstantValues.java:1517` defines
  `unsupported.federation.packages.types` with the default `terraformbackend`. No open
  source file reads it, so where the closed engine applies it is **[I]**.
- **The documentation states the same rule [D]:** "You can't connect a Terraform Backend
  repository to a Federated repository" (Federation Members and Topology).
- No other `RepoType` (`RepoType.java:44-109`) is excluded.
- artifactweave already refuses `rclass: federated` with `terraformbackend`
  (`internal/artifacts/api/repo/packagetypes.go`, the `Federated` row of `refused`).
  P13 adds the same refusal to `POST /api/federation/migrate/{repoKey}`.

## 5. Service trust (pairing)

### Endpoints

`web/rest/src/main/java/org/artifactory/rest/resource/servicetrust/ServiceTrustResource.java`,
base path `/v1/service_trust/` (`:81`), served under `/artifactory/api` [S]. Every
operation except `exchange` is admin only.

| Method and path | Line | Body and answer |
|---|---|---|
| `GET pairings/{usecase}` | 115 | `[{"base_url"}]`, one per paired instance (`:233-247`; `BaseUrlModel.java:11`) |
| `POST pairing/{usecase}` | 125 | `{"pairing_token"}` (`PairingToken.java:10`). For `federated-repo/*` and `/r`, the token's base URL is `federatedRepoUrlBase` without `/artifactory` (`:200-202`, `:218-227`). The documentation's sample adds `"expires_in": 300` [D] |
| `DELETE pairing/{usecase}` | 136 | Body `{"base_url"}`, query `force`. Calls the peer's `revoke/{usecase}` with the stored master token, then deletes it; `force` deletes it even when the revoke fails (`ServiceTrustServiceImpl.java:255-296`). Answers `{"message":"Trust with service <name> was canceled successfully"}`, `<name>` the use case's name, such as `federated-repo` (`:75-76`, `:262-272`) |
| `DELETE revoke/{usecase}` | 148 | Revokes the caller's own token when its scope names the use case (`ServiceTrustServiceImpl.java:307-322`). 200 `{"message":"Token was successfully revoked"}`, or 200 `{"message":"Token already revoked or not found."}` |
| `PUT pairing/{usecase}` | 159 | Body `{"pairing_token"}`. Rejects an expired token ("Attempt to pair instances with an expired token"), then calls the issuer's exchange URL from the token and stores the master token it returns (`ServiceTrustServiceImpl.java:187-202`). Answers `{"message":"Trust with service <name> was established successfully"}` (`ServiceTrustResource.java:72-73`, `:251-259`) |
| `POST exchange` | 169 | The issuer side of `PUT pairing`: the caller's bearer token is exchanged for a token of the requested target scope (`ServiceTrustServiceImpl.java:335-343`). Not on the documentation's index |

**Cancelling a trust and revoking a token are two halves of one exchange [S].**
`DELETE pairing/{usecase}` is what an administrator calls on the instance that redeemed a
token (B), naming the issuer by `base_url`, to end an established trust. B then calls the
issuer's `DELETE revoke/{usecase}` itself, authenticated with the master token it holds, so the
issuer revokes that master token; B deletes its copy whether or not the revoke succeeded when
`force` is set. `DELETE revoke/{usecase}` thus revokes the caller's own credential and is
meant for the paired instance, not for an administrator. Neither cancels an unredeemed
pairing token: it simply expires after five minutes. The documentation calls the first
"Unregister Pairing Token: Cancels the trust relationship with a specific service" [D].

**`POST exchange` is the issuer's token endpoint [S].** A redeemer calls it during
`PUT pairing` with the pairing token as bearer to get its master token, and later with the
master token to get scoped one-hour tokens for each federation call
(`exchangeMasterToken`, `ServiceTrustServiceImpl.java:240-247`). In P13 artifactweave answers
it 400 `{"message":"Error while token exchange. <reason>"}`: peers sign their requests
instead of holding tokens, and the redeemer's call goes to `/artifactweave/peer/v1/pair`
([0021](decisions/0021-peer-trust-and-sync-protocol.md#decision)), registered as
[DV-205](../implementation/deviations.md#api-behaviour).

Errors are `{"message"}` with 400 for a trust or state error and 500 otherwise
(`ServiceTrustResource.java:92-110`); an unknown use case is 404 `{"message":"Unknown use case"}`.
The message patterns are "Error creating pairing token. %s", "Error while token pairing. %s",
"Error while cancelling token pairing. %s", "Error revoking token. %s" and "Error while token
exchange. %s" (`:63-68`).

### Use cases and scopes

`backend/core/src/main/java/org/artifactory/servicetrust/ServiceTrustUseCase.java` [S]:

| Use case | Lines | Path patterns | Trust |
|---|---|---|---|
| `artifactory-cold` | 19-26 | `^artifactory-cold$`, `^artifactory-cold/.+$` | one way |
| `distribution` | 29-33 | the three `DistributionTokenSpecService` patterns | one way |
| `FEDERATED_REPO` (`federated-repo`) | 36-38 | `^federated-repo/.*:[xw]$`, `^federated-repo/\*$`, `artifact:\*:r` | one way |
| `FEDERATED_REPO_READ` (`federated-repo`) | 41-43 | `^federated-repo/r$`, `^federated-repo/.*:r$` | one way |

The documentation lists only `federated-repo/*`, `federated-repo/r`, `artifactory-cold` and
`artifactory-cold/<namespace>` for this endpoint, and 404 for anything else [D]. The source also
matches `distribution`.

### Tokens

| Token | Lifetime | Source |
|---|---|---|
| Pairing token | 5 minutes | [S] `backend/core/.../servicetrust/ServiceTrustService.java:17` |
| Master token, held by the redeeming instance | 365 days, refreshed by a cron (`federated.master.tokens.refresh.job.cron`, hourly) with a 7-day grace (`federated.master.tokens.refresh.grace.days`) | [S] `ServiceTrustService.java:18`; `ConstantValues.java:1404-1405`; `RemoteMasterTokenRefreshJob.java` |
| Exchanged scoped token | 1 hour (`federated.exchange.token.expiration.time.hours` holds seconds) | [S] `ServiceTrustService.java:19`; `ConstantValues.java:1515-1516` |

The two-way specification refreshes with scope `federated-repo/*`
(`servicetrust/federation/FederatedRepositoryTokenSpecService.java:95-96`) and limits an
exchanged token to `r` when `federated.exchange.token.limit.scope.enabled` is on (`:146-148`;
`ConstantValues.java:1514`). The receiving-only specification refreshes with
`federated-repo/r` (`UnidirectionalFederationTokenSpecService.java:70-72`) and never grants
admin scope (`:55`). Neither "join key" nor "circle of trust" appears in the Java source.

## 6. Receiving-only (unidirectional) members

From [Configure Receiving-Only Mode](https://docs.jfrog.com/artifactory/docs/configure-receiving-only-mode.md) [D]:

- A member with `mode: receiver` "receives events and binaries from other Federation members
  but cannot send". On that member, outbound federation processing and Full Sync initiation
  are skipped and federation configuration changes are rejected.
- Local writes on a receiver are not blocked and are never synchronized. A later Full Sync
  from the sender may remove locally added artifacts and restore locally deleted ones.
- Creating or updating a repository with a receiver member starts a Full Sync towards it
  automatically.
- So a local write on a receiver is accepted with its usual answer and is never sent; no
  status or message refuses it [D]. artifactweave does the same
  ([0022](decisions/0022-federated-repositories.md#decision)).
- `GET /api/federation/status/connectionStates/{repoKey}` distinguishes target member
  mode `Receiving_Only` from connection status `HEALTHY` (the documented verification
  table, rechecked 2026-11-26). The API schema publishes `status` but no mode field;
  artifactweave uses an additional `mode` string [I], marked `x-unverified` and logged
  on #216. Its status remains a health value, not `Receiving_Only`.
- Trust: a `federated-repo/r` pairing gives the redeeming (target) instance a receiving-only
  master token, so its administrator cannot build federations that send back to the
  issuer. A `federated-repo/*` pairing also works, with the receiver mode set per repository.
- The feature is RTFS only ([D] RTFS page, "New features (for example, Unidirectional Sync)").
  The migration plugin `jf federation-migrator replication_to_federation` turns push
  replication into a receiver federation, requires RTFS and plugin 2.4 or later, and
  removes the source's replication definitions ([D] Migrate Push Replication).

## 7. Federation endpoints and how artifactweave answers them

### Classes

- **P13 (task):** artifactweave serves the 7.x contract in the named task of the
  [P13 plan](../implementation/plan.md#p13-peer-trust-and-federated-repositories).
- **Deviation:** artifactweave deliberately answers differently, because the request only
  has a meaning between Artifactory and a separate federation service, its HA peers or its
  migration tool. The task that implements the answer registers it in the
  [deviations register](../implementation/deviations.md).
- **[I]:** the named task serves it, but part of the contract rests on inference because the
  DTO or the behaviour is closed source; listed on #216.

### Without a separate federation service

artifactweave's federation runs inside the server, and instances exchange events over its own
peer protocol (`/artifactweave/peer/v1/`, [0021](decisions/0021-peer-trust-and-sync-protocol.md)).
It therefore presents itself as an instance whose federation service is connected and whose
migration is complete:

- `GET /api/federation/rtfs` answers 200 `text/plain` `true`, and
  `GET /api/federation/migration/status` answers 200 `text/plain` `MIGRATION_COMPLETED`
  (the last value of `FederationMigrationStatus`, `base/.../repo/FederationMigrationStatus.java`).
- The RTFS-era operations (`stateList`, `stateSummary`, `connectionStates`, `prioritization`,
  `propagateCreatedTime`, `removeMember`, receiver members) are served.
- [S] `stateList` and `stateSummary` check the configured federation base before asking
  the service, even when there are no federated repositories. Without one they answer
  400 with `{"federations":[]}` and a `federationStates` object containing eight zero
  counters respectively (`FederatedStatusResource.java:168-175,194-205`). The summary
  includes `READ_ONLY` (`StateSummaryRtfsResponse.java:23-38`). [I] artifactweave keeps
  receiver direction separate from health: its current RTFS classifier emits no
  distinct read-only health state, so that counter is zero. The cached service
  implementation supplies no classification algorithm; receiving-only mode is not
  evidence for a `READ_ONLY` RTFS count. This remains a P19 comparison on #216.
- The legacy monitoring operations (`state`, `status/repo` v1 and v2, `mirrorsLag`,
  `unavailableMirrors`) are served too, from the same cursors. 7.161.26 answers them 405 or
  blocks them once RTFS is enabled (`FederatedRepoResource.java:273-277`,
  `FederatedStatusResource.java:94-96`); serving both is a deviation (decision 0022).
- `POST /api/federation/reconnect` re-establishes the peer sessions and answers 200 `true`,
  as 7.x does after reconnecting its federation client (`FederatedRepoResource.java:694-710`).
- The migration tool's writes (`PUT rtfs`, `PUT migration`, `PUT migration/status/verbose`)
  answer every caller 400 with 7.x's message for a caller that is not the tool, "Access
  denied: This endpoint is restricted to migration tool calls only."
  (`FederatedRepoResource.java:1096-1110`); there is nothing to migrate.
- Internal plumbing, which only RTFS, an HA node or the migration tool calls with an
  `internal:` scope or the HA role (`web/rest/.../federation/PermissionScopes.java:6-28`),
  answers 404 like any path artifactweave does not serve. The peer protocol carries what
  those calls carry.

**Project decision (2026-11-27):** artifactweave's authenticated peer responses
retain the required RFC 9421 `Signature` and `Signature-Input` headers and issued
pairing-token responses. Private keys never appear in responses, logs or evidence.
Pairing secrets and signatures appear only in protocol messages that require them;
logs, refusal bodies and captured evidence redact them. The maintainer
[approved this criterion](https://github.com/weaveplatform/artifactweave/issues/1921#issuecomment-7213823211)
for [#1921](https://github.com/weaveplatform/artifactweave/issues/1921).
[Decision 0021](decisions/0021-peer-trust-and-sync-protocol.md) specifies signing,
verification and unsigned refusals before caller identification. This is the
artifactweave peer contract; it supplies no live JFrog wire observation or phase-exit
result.

### Endpoint table

Paths are under `/artifactory/api/`. Line numbers are the `@Path` lines in
`web/rest/src/main/java/org/artifactory/rest/resource/federation/` [S], or in
`ServiceTrustResource.java` for pairing. "Doc" marks an operation on the
[API index](https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt).

| Method and path | Source | Doc | Class | artifactweave answer |
|---|---|---|---|---|
| `POST federation/migrate/{repoKey}` | `FederatedRepoResource.java:328` | yes | P13 FED-01 | Converts a local repository; refuses `terraformbackend` |
| `POST federation/convertToLocal` | `:343` | yes | P13 FED-01 | |
| `POST federation/configSync/{repoKey}` | `:406` | yes | P13 FED-01 | 200 "Configuration sync executed successfully" (`:424`) |
| `POST federation/fullSync/{repoKey}` | `:360` | yes | P13 FED-04 | `?mirror` limits it to one member. `?isDry` (the comparison tool, from 7.104.2) is a deviation (DV-206): 400 naming the parameter, as the comparison files' format is RTFS's. 200 `{}` otherwise |
| `POST federation/forceFullSync/{repoKey}` | `:375` | yes | P13 FED-04 | `?mirror` as above |
| `POST federation/recovery` | `:601` | yes | P13 FED-04 | `?repoKey` limits it to one repository; `?force` retries every member's delivery [I] |
| `POST federation/reconnect` | `:694` | yes | P13 FED-04 | 200 `true`; a forced recovery of every repository [I] |
| `PUT federation/prioritization/{repoKey}` | `:852` | yes | P13 FED-04 | `?priorityLevel` HIGH or STANDARD |
| `PUT federation/propagateCreatedTime` | `:895` | yes | P13 FED-04 | `?enabled` |
| `POST federation/removeMember` | `:969` | yes | P13 FED-04 | Body is the member's base URL or member URL |
| `GET federation/state` | `:270` | yes | P13 FED-05 | Legacy monitoring, served (deviation above) |
| `GET federation/inconsistentRepos` | `:957` | yes | P13 FED-05 | |
| `GET federation/status/repo/{repoKey}` | `FederatedStatusResource.java:125` | yes | P13 FED-05 | Body in "Status payloads" below |
| `GET federation/status/repo/{repoKey}/{federatedKey}` | `:132` | no | P13 FED-05 | The `mirrors` or `binaries` projection, not a member selector |
| `GET v2/federation/status/repo/{repoKey}` and `.../{federatedKey}` | `FederatedStatusResourceV2.java:34`, `:41` | yes | P13 FED-05 | The same full DTO or projection, with v2 mirror states |
| `GET federation/status/mirrorsLag`, `.../unavailableMirrors` | `FederatedStatusResource.java:91`, `:108` | yes | P13 FED-05 | Legacy monitoring, served |
| `GET federation/status/stateList`, `.../stateSummary` | `:155`, `:183` | yes | P13 FED-05 | |
| `GET federation/status/connectionStates/{repoKey}` | `:216`, `:232-243`, `:293-295` | yes | P13 FED-03 (connectionStates); FED-05 (other status) | Unknown/non-federated: 200 empty `memberConnections`; federated without a base URL: 400 with the same empty envelope. Receiver `mode` reads `Receiving_Only` [I] field name; health remains `HEALTHY` when healthy |
| `GET federation/rtfs` | `FederatedRepoResource.java:682` | no | P13 FED-05 | 200 `text/plain` `true` |
| `GET federation/migration/status` | `:820` | no | P13 FED-05 | 200 `text/plain` `MIGRATION_COMPLETED` |
| `PUT federation/rtfs`, `PUT federation/migration`, `PUT federation/migration/status/verbose` | `:800`, `:836`, `:1011` | no | Deviation (FED-05) | 400 "Access denied: This endpoint is restricted to migration tool calls only.", to every caller |
| `GET federation/binaries/failures/list`, `PUT .../replay`, `DELETE .../delete` | `FederatedBinaryTasksResource.java:71`, `:93`, `:116` | yes | P13 FED-06 | |
| `GET federation/binaries/tasks/{repoKey}` | `:135` | no | [I] FED-06 | The repository's binary-task counts; the DTO is closed |
| `GET federation/status/repoState/{repoKey}/binaries/` | `FederatedStatusResource.java:254` | no | [I] FED-06 | As above, for any user with read |
| `POST federation/replaceUrl`, `POST federation/updateMemberUrl` | `FederatedReplaceUrlResource.java:61`, `:96` | no | [I] FED-01 | Body `{oldBaseUrl, newBaseUrl}` (`base/config/.../model/ReplaceUrlConfig.java:10-11`); rewrites member URLs on every federated repository; 200 text "Binding URL from URL %s to new %s finished." (`:137-140`). Whether 7.x then tells the members is **[I]** |
| `GET federation/compareResult/{fileName}` | `FederatedRepoResource.java:927` | no | Deviation (FED-04, DV-207) | 404: no comparison files exist, as `isDry` is refused |
| `GET federation/status` (the JPD status) | `FederatedStatusResource.java:139` | no | Deviation (FED-05) | 404; internal scope `jpd-status` |
| `GET federation/validate/{repoKey}`, `GET federation/repos`, `GET federation/baseUrlAndRepos` | `FederatedRepoResource.java:208`, `:238`, `:254` | no | Deviation (FED-05) | 404; internal scopes |
| `POST federation/properties/{repoKey}`, `GET federation/property/{repoKey}/{fileName}`, `GET federation/extendedInfo/{repoKey}` | `:291`, `:305`, `:321` | no | Deviation (FED-05) | 404; full-sync plumbing |
| `GET federation/ready/{repoKey}`, `GET federation/repoExists/{repoKey}` | `:428`, `:444` | no | Deviation (FED-05) | 404; internal scopes |
| `POST`/`PUT federation/metadata`, `POST federation/metadata/invalidate` | `:452`, `:473`, `:487` | no | Deviation (FED-05) | 404 |
| `PUT federation/negotiation/disable`, `POST federation/repos/enable` | `:498`, `:507` | no | Deviation (FED-05) | 404 |
| `POST federation/dryfullsync/repo/{fileName}`, `POST`/`GET`/`DELETE federation/fullsync/repo/{fileName}`, `POST federation/fullsync/systemRepo` | `:520`, `:538`, `:567`, `:588`, `:554`; constants `base/api/.../SystemRestConstants.java:109-113` | no | Deviation (FED-05) | 404; the peer snapshot (0021) replaces the full-sync files |
| `GET federation/federatedRepoConfigInfo`, `GET federation/federatedConfigInfo/{repoKey}` | `:623`, `:632` | no | Deviation (FED-05) | 404 |
| `POST federation/disableRemoteMember`, `POST federation/member/enable` | `:646`, `:660` | no | Deviation (FED-05) | 404; a member's `enabled` is set through the repository configuration |
| `GET federation/lastIdInSystem`, `POST federation/task/submit`, `GET federation/binary/provider/v2/check` | `:674`, `:911`, `:945` | no | Deviation (FED-05) | 404 |
| `POST federation/queue/remaining`, `PUT .../queue/stop`, `PUT .../queue/deregister`, `POST .../queue/reset`, `GET .../queue/state`, `PUT .../queue/state/{state}` | `:715`, `:728`, `:747`, `:768`, `:868`, `:881` | no | Deviation (FED-05) | 404; the outbox replaces RTFS's queues |
| `GET federation/tenant/move/status`, `PUT federation/snapshotFederatedRepos`, `GET federation/migration/status/verbose` | `:786`, `:986`, `:998` | no | Deviation (FED-05) | 404 |
| `PUT`/`GET federation/binaries/rtfs` | `FederatedBinaryTasksResource.java:156`, `:182` | no | Deviation (FED-05) | 404 |
| `POST federation/grid/topology/events` | `FederatedRepoResource.java:1057` | no | Deviation (FED-05) | 404 in P13; grid topology is P14 ([0025](decisions/0025-platform-federation-and-topology.md)) |
| `GET v1/service_trust/pairings/{usecase}` | `ServiceTrustResource.java:115` | no | P13 PEER-02 | `[{"base_url"}]` |
| `POST`, `PUT`, `DELETE v1/service_trust/pairing/{usecase}` | `:125`, `:159`, `:136` | yes | P13 PEER-02 | Use cases `federated-repo/*` and `federated-repo/r` |
| `DELETE v1/service_trust/revoke/{usecase}` | `:148` | no | P13 PEER-02 | |
| `POST v1/service_trust/exchange` | `:169` | no | P13 PEER-02 | See [0021](decisions/0021-peer-trust-and-sync-protocol.md#decision) |
| `POST /access/api/v1/service_trust/pairing/{useCase}` | `api/src/access/paths-identity.yaml:1573` | yes | P13 PEER-02 | Same pairing on the Access path |

### Status payloads

The repository status body's last two fields, untyped in the oracle
([12](12-open-questions.md)), are open in 7.161.26 [S]:

- `FederatedRepoStatusInfo` is `{localKey, binariesTasksInfo, mirrorEventsStatusInfo[], federatedArtifactStatus}`
  (`backend/core/.../federation/FederatedRepoStatusInfo.java:15-18`).
- `MirrorEventsStatusInfo` is `{remoteUrl, remoteRepoKey, status, createEvents, updateEvents, deleteEvents, propsEvents, errorEvents, lagInMS, eventRegistrationTimeStamp}`,
  the counts and times longs defaulting to 0 (`MirrorEventsStatusInfo.java:13-22`).
- `FederatedArtifactStatus` is `{countFullyReplicateArtifacts, countArtificiallyReplicatedArtifacts}`
  (`storage/common/.../federation/FederatedArtifactStatus.java:9-10`).

The JSON names follow the field names, as the classes carry no `@JsonProperty` [I].

The last path segment selects `mirrors` or `binaries`, not a remote member [S]
(`FederatedStatusResource.java:62–63`, `:321–343`). `mirrors` projects
`{localKey, mirrorEventsStatusInfo[]}` (`FederatedMirrorEventsStatusInfo.java`);
`binaries` projects `{localKey, binariesTasksInfo, federatedArtifactStatus}`
(`FederatedArtifactsStatusInfo.java`). An unrecognized selector is 400, after the
repository-existence check. The source resource accepts federated and local/cached
repositories for these legacy status reads (`:299–302`); a missing repository is 404.
`connectionStates` returns 200 with an empty list for an unknown or non-federated
repository (`:232–234`). The source handles base-URL absence separately; the
artifactweave status read uses its configured member base.

### FED-05 implemented status choices

[I] Status computation follows decision 0022's durable records. The monitoring lag
threshold is one minute. Pending lag is the age of the first unacknowledged entry;
acknowledged entries contribute no pending count, registration time or lag. A
recorded delivery failure, unavailable/incompatible peer or pending snapshot makes
an enabled member out of sync. An intentionally disabled target does not make the
repository out of sync; disabling the local member reports `DISABLED`.

[I] Event counters use the operation recorded by the item service's actual admission:
create, overwrite/update, property change or deletion. Delivery still carries whole
item state. Older `item.put` entries lacking that optional category count as updates;
a cursor with a delivery error attributes its queued entries to `errorEvents`.

[I] `countFullyReplicateArtifacts` counts current files whose winning state is not
waiting for content; `countArtificiallyReplicatedArtifacts` counts pending file
paths. A pending replacement excludes the old file from the complete count.
`failingTasks` counts recorded failed content fetches, while `inProgressTasks` counts
distinct pending contents without a recorded failure. These task-count meanings
remain inferred, separately from FED-06's `{failedTasks}` task endpoints.

[I] Configuration drift compares masked configurations read from reachable current
members over the signed peer API. Member order, equivalent defaults and different
local repository keys are normalized. A missing/unavailable answer is not evidence
of different configuration. The read rechecks trust and membership before disclosure;
the report refreshes administrator authority after remote calls.

Serving legacy monitoring beside RTFS, migration-tool refusals for every caller and
internal RTFS paths returning 404 are registered as DV-208, DV-209 and DV-210.


## 8. Clients

### Terraform provider 12.11.14

From `pkg/artifactory/resource/repository/federated/federated.go` [S]:

- **Schema version 4** (`:419`). `member` is a set; `url` is required and checked only as an
  http or https URL, described as "Full URL to ending with the repositoryName" without
  enforcing the suffix (`:134-138`); `enabled` is a required bool (`:140-145`).
- **`access_token`** (V4, optional, sensitive, `:147-152`) is never sent to Artifactory: the
  wire `Member` is `{url, enabled}` (`:67-70`), and the token stays in state, matched to the
  read-back member by URL (`:200-224`). There is no `mode` attribute, so the provider cannot
  configure a receiver member.
- **After a create or update** it calls `POST artifactory/api/federation/configSync/{key}`
  and turns a failure into a warning (`:236-262`, `:279`, `:297`). It skips that call for
  `releasebundles` (`:274-283`).
- **`cleanup_on_delete`** (default false): on destroy it sends
  `DELETE <member host>/artifactory/api/repositories/{last path segment}` to every member
  except its own, with that member's `access_token` or else the provider's credentials, and
  turns failures into warnings (`:319-377`).
- `proxy` and `disable_proxy` map to `proxy` and `disableProxy` (`:191`).
- **Create errors** come from `repository.Create`, which returns the response body verbatim
  as the diagnostic (`pkg/artifactory/resource/repository/repository.go:798-800`).

The 37 federated resources (`pkg/artifactory/provider/resources.go:32-46`, `:70-75`, `:87-90`;
`federated.PackageTypesLikeGeneric`, `federated.go:40-60`) are listed in
[research 08](08-terraform-providers.md#repository-resources). There is no federated
`terraformbackend` resource.

### jfrog-client-go 1.55.0 and the JFrog CLI

- `artifactory/services/federation.go` [S]: `ConvertLocalToFederated` sends
  `POST api/federation/migrate/{repoKey}` (`:26-35`), `TriggerFederatedFullSyncAll`
  `POST api/federation/fullSync/{repoKey}` (`:43-52`) and `TriggerFederatedFullSyncMirror`
  the same with `?mirror={url}` (`:60-69`). Each accepts exactly 200 and ignores the body.
- `artifactory/services/federatedrepository.go` [S]: federated repositories of 30 package
  types through the repositories API; the member is `{Url, Enabled *bool omitempty}` (`:150-153`).
- `jf rt repo-create` and `repo-update` handle `rclass: federated`. No `jf` command calls
  `fullSync` or `migrate`.
- `jf rt transfer-config` removes federated members before it creates repositories on the
  target and logs that they must be added again ([S] jfrog-cli-core v2.60.0
  `artifactory/commands/utils/transferconfigbase.go:197-199`, `:316-327`, `:386-392`).

## 9. Terraform exit set

P13's exit qualifies every `artifactory_federated_*_repository` in provider 12.11.14 whose
local type artifactweave implements (maintainer decision, 2026-11-24), except
`terraformbackend`. The provider list is [research 08](08-terraform-providers.md#repository-resources)
(`08-terraform-providers.md:104`); the implemented local types are those the
[package format matrix](../implementation/package-format-matrix.md) and the
[capability roadmap](../implementation/capability-roadmap.md#repository-type-and-class-checklist)
record as native, or as core for `generic`. That gives **35 resources over 34 package types**.

| # | Resource | Package type | Provider implementation |
|---|---|---|---|
| 1 | `artifactory_federated_alpine_repository` | `alpine` | dedicated |
| 2 | `artifactory_federated_ansible_repository` | `ansible` | dedicated |
| 3 | `artifactory_federated_bower_repository` | `bower` | generic-like |
| 4 | `artifactory_federated_cargo_repository` | `cargo` | dedicated |
| 5 | `artifactory_federated_chef_repository` | `chef` | generic-like |
| 6 | `artifactory_federated_cocoapods_repository` | `cocoapods` | generic-like |
| 7 | `artifactory_federated_composer_repository` | `composer` | generic-like |
| 8 | `artifactory_federated_conan_repository` | `conan` | dedicated |
| 9 | `artifactory_federated_conda_repository` | `conda` | generic-like |
| 10 | `artifactory_federated_cran_repository` | `cran` | generic-like |
| 11 | `artifactory_federated_debian_repository` | `debian` | dedicated |
| 12 | `artifactory_federated_docker_repository` | `docker` | alias of `docker_v2` |
| 13 | `artifactory_federated_docker_v2_repository` | `docker` | dedicated |
| 14 | `artifactory_federated_gems_repository` | `gems` | generic-like |
| 15 | `artifactory_federated_generic_repository` | `generic` | generic-like |
| 16 | `artifactory_federated_gitlfs_repository` | `gitlfs` | generic-like |
| 17 | `artifactory_federated_go_repository` | `go` | generic-like |
| 18 | `artifactory_federated_gradle_repository` | `gradle` | Java |
| 19 | `artifactory_federated_helm_repository` | `helm` | generic-like |
| 20 | `artifactory_federated_helmoci_repository` | `helmoci` | dedicated |
| 21 | `artifactory_federated_huggingfaceml_repository` | `huggingfaceml` | generic-like |
| 22 | `artifactory_federated_ivy_repository` | `ivy` | Java |
| 23 | `artifactory_federated_maven_repository` | `maven` | Java |
| 24 | `artifactory_federated_npm_repository` | `npm` | generic-like |
| 25 | `artifactory_federated_nuget_repository` | `nuget` | dedicated |
| 26 | `artifactory_federated_oci_repository` | `oci` | dedicated |
| 27 | `artifactory_federated_opkg_repository` | `opkg` | generic-like |
| 28 | `artifactory_federated_puppet_repository` | `puppet` | generic-like |
| 29 | `artifactory_federated_pypi_repository` | `pypi` | generic-like |
| 30 | `artifactory_federated_rpm_repository` | `rpm` | dedicated |
| 31 | `artifactory_federated_sbt_repository` | `sbt` | Java |
| 32 | `artifactory_federated_swift_repository` | `swift` | generic-like |
| 33 | `artifactory_federated_terraform_module_repository` | `terraform` (`terraformType` `MODULE`) | dedicated |
| 34 | `artifactory_federated_terraform_provider_repository` | `terraform` (`terraformType` `PROVIDER`) | dedicated |
| 35 | `artifactory_federated_vagrant_repository` | `vagrant` | generic-like |

Left out:

| Resource or type | Reason |
|---|---|
| `artifactory_federated_docker_v1_repository` | Sends `dockerApiVersion: V1`. The Docker Registry HTTP API V1 is superseded by V2/OCI ([supersession register](../implementation/capability-roadmap.md#supersession-register)), and artifactweave serves no V1 protocol to federate |
| `artifactory_federated_releasebundles_repository` | The `releasebundles` local type has no implementation yet (P15, with v2 workflows in P24); the provider also skips `configSync` for it |
| `terraformbackend` | 7.161.26 excludes it (section 4) and the provider has no federated resource for it. artifactweave refuses it on create and on `migrate` |

artifactweave also federates `pub`, `nix`, `machinelearning`, `bazelmodules`, `luarocks`
and `agentpackages`, which have no federated provider resource. P13's multi-instance
scenarios cover them through the repositories API, outside the Terraform exit.

The consumer configuration is
[`federated-repositories`](../../deploy/artifactweave/terraform/federated-repositories/README.md).
The required provider lane uses three owned, mutually paired instances and checks both
configuration revisions, empty refreshed plans and readbacks on every member. It checks
all 70 provider `configSync` responses rather than accepting the provider's warning path,
and verifies `cleanup_on_delete` on all members. The private negative-control configuration
exercises section 10 separately. The sanitized `p13-provider-evidence.json` records the
35 resource outcomes and exclusions; it contains neither state nor member credentials.
This is configuration qualification; content convergence remains the multi-instance exit.

`TestAccFederatedAPIFormatsSQLite/Postgres` in `test/peers` supplies content scenarios
for the six types without provider resources: Pub archive/finalize, Nix archive and
narinfo, Machine Learning transaction/manifest, Bazel module/source archive, LuaRocks
rockspec/source rock, and Agent Packages ZIP publication. All three members must serve
exact content and native metadata. This is artifactweave HTTP protocol evidence, not
a live JFrog comparison or actual CLI execution.

`TestAccFederatedNativeSQLite/Postgres` separately uses Maven, npm and JFrog CLI for
three-member publication, resolution, properties, deletion, partition repair and receiver
isolation. Host-client results supplement required pinned Linux execution. The integrated
exit, configured-member cleanup and final merged-main evidence remain open under
[#1937](https://github.com/weaveplatform/artifactweave/issues/1937) and
[#1798](https://github.com/weaveplatform/artifactweave/issues/1798).

## 10. An unpaired member in a Terraform run

This answers the "Federated repository plans" question in [12](12-open-questions.md).

- **Plan.** `terraform plan` shows the create or the member change and no error: the
  provider checks only that each `url` is an http or https URL, and sends nothing to the
  server while planning (section 8).
- **Apply.** The create's `PUT /api/repositories/{key}`, or the update's `POST`, answers 400
  in the repository API's envelope. The message names the first member whose base URL is
  not a paired peer and the use case it needs:

  ```text
  {"errors":[{"status":400,"message":"Federated member 'https://b.example.com/artifactory/libs-local' is not a paired peer: pair the instances for federated-repo/* first"}]}
  ```

  A `receiver` member is accepted with either pairing, so its message names
  `federated-repo/* or federated-repo/r`. The provider prints the body verbatim:
  `Error: {"errors":[{"status":400,"message":"Federated member '...' is not a paired peer: ..."}]}`,
  under the resource address. A failed create writes no state, so the next plan proposes
  the create again; a failed update keeps the old members in state. The `configSync` call is
  never reached.
- The message text is artifactweave's own. What 7.x answers for a member it cannot reach or
  does not trust is **[I]**.

## 11. Inferred items

Posted on [#216](https://github.com/weaveplatform/artifactweave/issues/216) for P19:

1. Where the closed engine applies `unsupported.federation.packages.types`, and the status
   and message of a refused `terraformbackend` federation or migration.
2. The HTTP status a client gets when no custom base URL is configured.
3. What `rclass` 7.x stores and reads back for a `PUT` with `rclass: "local"` and `members`.
4. Whether 7.x accepts `mode` values other than `bidirectional` and `receiver`, such as the
   XSD's "unidirectional". artifactweave refuses them with 400, since treating an unknown
   mode as bidirectional would send events to a member meant to only receive.
5. The bodies of `GET federation/binaries/tasks/{repoKey}` and
   `GET federation/status/repoState/{repoKey}/binaries/`, and whether `replaceUrl` and
   `updateMemberUrl` notify the other members.
6. The JSON names of the status DTOs, which carry no `@JsonProperty` (section 7).
7. The 400 or other answer 7.x gives for a member URL it does not trust (section 10).
8. The success bodies of `migrate` and `convertToLocal`, which jfrog-client-go does not
   read. artifactweave answers `Repository '<key>' was converted to a federated repository`
   and `Converted to local repositories: <keys>` (FED-01).
9. Whether 7.x refuses two members on one instance. artifactweave does, since its
   membership table holds one member per peer.
10. The message 7.x gives a federated repository naming a proxy that does not exist.
    artifactweave answers `Proxy '<key>' does not exist`.
11. Whether 7.x refuses `migrate` for a repository with push replication. artifactweave
    does, as decision 0022 forbids a repository being both federated and a push target.
12. `connectionStates.memberConnections[].mode` is inferred; the receiving-only
    documentation publishes `Receiving_Only` as the target mode separately from
    connection health but its API schema has no mode property. Registered on
    [#216](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-7198643299).
13. **Superseded/resolved by source:** the earlier missing-repository 404 inference
    is retracted. `FederatedStatusResource.java:232-234,293-295` returns 200 with
    empty `memberConnections` for unknown or non-federated repositories, before
    checking the base URL; `:237-243` returns 400 with that empty envelope for a
    federated repository without a configured base URL. The mode-field inference
    in item 12 remains open. The existing #216 comparison records this correction.

14. When a failed binary first appears in the list: the public list documentation mentions
    a default maximum of ten attempts without defining visibility timing. artifactweave
    exposes recorded failures from the first failed fetch while retries remain queued.
15. The list's `path` extension names the first still-pending path in byte order. The
    published task fields do not include it; compare the extension separately from
    the sourced checksum, repository, first-failure time and attempt count.
16. The closed task-count bodies use `{failedTasks: <count>}` for both task endpoints
    as artifactweave's inferred projection of recorded failed fetches (item 5).
17. Whether replay resets attempts in 7.x. The public API promises reprocessing, while
    artifactweave retains its existing attempt-reset choice.

### FED-06 implemented failed-binary choices

[I] artifactweave's failed-binary list retains the documented `repoKey`, `sha1`,
`creationTime` (epoch milliseconds) and `failuresCount`. Its `path` extension names
one current pending path; superseded failures omit it. The content task is keyed by
repository and SHA-256, while replay/delete accept the documented SHA-1 filter.
List defaults to page 1 and limit 1000, accepts positive limits and emits a next-page
Link. The store filters and pages failed rows before pending-path metadata is read
for the selected page. Invalid paging and digests are 400; an unknown repository is 404.

[I] The two closed-source task-count DTOs use `{failedTasks: <count>}`, marked
`x-unverified` in the spec, for comparison on #216. These are failed content fetches,
not a claim about private RTFS task queues. The repository-state form requires
current repository Read; the other four operations require an administrator.

Project choice [I] (decision 0022): replay resets attempts (the public reprocessing
contract does not specify this reset) and durably queues each selected binary for
retry without backoff. `replayTasks` counts tasks marked; the request returns without
a network call. Attempt resets and all enqueues commit or roll back together.
Workers retry unavailable content using the ordinary queue policy. An in-flight
lease stays held, and its eventual settlement cannot back off or park the fresh
replay. Deletion cancels queued or in-flight retry publication.
A newer event or normal full sync can supply content after cancellation; full sync
repairs pending content even when its conflict stamp already matches the sender.
