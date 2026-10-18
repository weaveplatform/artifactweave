# 16: Distribution, cold storage, federation and Workers

Checked against: Artifactory 7.161.32

Five JFrog products that the first research pass placed out of scope and that the project
owner brought into scope on 2026-10-13: Distribution with release bundles v1, cold-storage
archive and retention, platform federation and topology, federated repositories, and
Workers. For each, this document records what the product does in JFrog, the endpoint
groups and their status codes, the entities, the clients that touch it, and what
artifactweave can and cannot reproduce. The decisions that follow from it are
[0021](decisions/0021-peer-trust-and-sync-protocol.md) to
[0026](decisions/0026-workers-runtime.md).

Research baseline: **2026-10-13**. Endpoint lists and status codes come from the cached
OpenAPI blocks (`.specoracle/`, [01](01-jfrog-api-landscape.md#parity-oracle)); behaviour
comes from the JFrog guide pages cited per section. Items that could not be confirmed are
marked *(unverified)*.

## Owner decisions that shape this document

| Decision | Consequence |
|---|---|
| Peers are artifactweave instances only | the management REST APIs below stay JFrog-compatible for the JFrog CLI and Terraform; the traffic between instances uses artifactweave's own documented protocol ([0021](decisions/0021-peer-trust-and-sync-protocol.md)); no federation or distribution with a real Artifactory or Edge node |
| Workers run on goja with esbuild | TypeScript is transpiled in-process and executed by a pure-Go engine; no Node runtime; executions run in helper processes of the same binary so a 64 MiB per-execution memory cap and a 1 s CPU budget are enforced ([0026](decisions/0026-workers-runtime.md)) |
| Cold storage is a blob tier | archived content moves to a second blob store inside the same instance instead of to a separate cold instance ([0024](decisions/0024-cold-storage-tier.md)) |
| Everything ships inside v0.1 | implementation phases P13 to P17 precede hardening (P18); milestone M4 marks the end of P17 ([plan](../implementation/plan.md)) |

## What is not reproducible

JFrog documents the management APIs of these products but not the protocols their
instances speak to each other: the federated-repository event stream between Artifactory
members, the Distribution-to-Edge transfer, the Access Federation synchronisation, the
cold-storage binding between a live and a cold Artifactory, and the Circle of Trust
pairing internals. Under the artifactweave-only decision these are replaced, not
reverse-engineered. The consequences:

- An artifactweave instance cannot be a member of a federation whose other members are
  Artifactory, cannot receive a bundle from JFrog Distribution, and cannot be a target of
  Access Federation run by Mission Control.
- Everything a user or a tool does through the documented APIs (create a federated
  repository, distribute a bundle, register an Access Federation target, define an archive
  policy, deploy a worker) works the same way; only the wire between two instances is
  artifactweave's own.
- Pairing tokens issued by artifactweave are opaque to Artifactory and the reverse; the
  pairing endpoints keep JFrog's paths and response shapes.

## 1. Distribution and release bundles v1

### What it does in JFrog

A release bundle v1 is a named, versioned, immutable manifest of artifacts selected by AQL
queries, with optional path mappings and added properties. It is created in the
Distribution service, signed with a GPG key, stored in a release-bundles repository on the
source Artifactory, and distributed to Edge nodes (read-mostly Artifactory instances near
consumers) chosen by distribution rules (site name, city, country codes). Edges verify the
signature before importing the artifacts. Artifactory keeps its own view of bundles it
stores or receives, a retention service for bundle versions, and an import path for
exported bundle archives.

Sources: https://docs.jfrog.com/artifactory/reference/jfrog-distribution-api/llms.txt,
https://docs.jfrog.com/artifactory/reference/release-bundles-v1-api/llms.txt,
https://docs.jfrog.com/artifactory/reference/retention-release-bundle-v1-api/llms.txt.

### Endpoint groups (75 operations)

| Group | Paths | Status codes (success first) |
|---|---|---|
| Bundle lifecycle | `GET/POST /distribution/api/v1/release_bundle`; `GET/PUT/DELETE/HEAD .../release_bundle/{name}/{version}`; `GET .../{name}`, `.../{name}/LATEST`; `DELETE .../{name}`; `POST .../{name}/{version}/sign` | create 200 (dry run and real), 400, 401; get 200, 404; delete 204, 403; sign 200, 400 |
| Distribution | `POST /distribution/api/v1/distribution/{name}/{version}`; `PUT .../abort`; `POST .../delete`; `POST /distribution/api/v1/dynamic/distribute`; `GET /distribution/api/v1/distribution/edge_nodes` | distribute 200 (dry run) or 202 (started); abort 202 or 204; dynamic 202 |
| Status | `GET /distribution/api/v1/release_bundle/distribution`, `.../{name}/distribution`, `.../{name}/{version}/distribution[/{tracker}]` | 200 |
| v2 distribution (release bundles v2; stub in artifactweave) | `POST /distribution/api/v2/distribution/distribute/{name}/{version}`; `PUT .../abort/...`; `POST .../remote_delete/...`; `GET .../trackers/{name}/{version}[/{tracker}]`; `PUT .../{tracker}/priority`; key `GET .../key/{key_name}`, `POST .../key/propagate/{key_name}` | JFrog: distribute 200, 409 on duplicate; abort 202 or 204. artifactweave: 501 |
| Signing keys | `GET/POST /distribution/api/v1/keys/pgp`; `GET .../aliases`; `GET/PUT/DELETE .../{alias}`; `POST .../propagate` | 200, 404 on unknown alias |
| Export and import | `POST/DELETE /distribution/api/v1/export/release_bundle/{name}/{version}`, `GET .../status`; `POST /distribution/api/release/import`; `POST /artifactory/api/release/import`, `GET .../{name}/{version}/status` | export 202; import 200 (Distribution) or 202 (Artifactory) |
| Artifactory bundle view | `GET /artifactory/api/release/bundles[/{name}[/{version}]]`; `DELETE .../source/{name}/{version}` | 200, 404 |
| Bundle retention v1 | `GET/POST /artifactory/api/v1/bundle/retention/config`, `.../config/enable`; `POST .../delete`, `.../keep`, `.../report`; `GET .../audit` | 200, 400; 405 when the retention service is disabled |
| Permissions | `GET /distribution/api/v1/security/permissions`, `GET/PUT/DELETE .../{name}` | put 200, 409 on conflict; delete 204 |
| Tokens | `GET/POST /distribution/api/v1/security/token`, `POST .../revoke` | create 200 or 201 |
| Maintenance | `POST .../maintenance/distributeByLastDays`, `POST .../execute/{actionType}`, `GET .../generate_token/{actionType}`, `GET .../stuck_distributions` | execute 202 |
| System | `GET .../system/info`, `/ping`, `/settings`, `POST .../settings`, `GET .../metrics`, support bundles (6) | 200 |
| Xray | `POST .../{name}/{version}/trigger_xray_scan` | 200 in JFrog; artifactweave answers 501 because Xray is out of scope |

### Entities

| Entity | Fields (wire names) | Notes |
|---|---|---|
| Release bundle v1 version | `name, version, description, release_notes{syntax, content}, storing_repository, state, created, created_by, spec{queries[{aql, query_name, mappings[{input, output}], added_props[{key, values[]}], exclude_props_patterns[]}]}, artifacts[{checksum, props[], sourceRepoPath, targetRepoPath}]` | the create body is untyped in the oracle; the field list is from the JFrog CLI release bundle commands *(unverified until captured)*. States: open, signed, stored, ready for distribution *(names unverified)* |
| Signature | detached GPG signature over the canonical manifest, key alias, passphrase header `X-GPG-PASSPHRASE` on create and sign | |
| Distribution rule | `site_name` (`*` for all), `city_name`, `country_codes[]` (ISO 3166-1 alpha-2), `priority low|medium|high` | request also carries `dry_run`, `auto_create_missing_repositories` |
| Distribution tracker | `id` (int64), `sites[{service_id, name, type}]`, per-target status | `type` distinguishes Edge from source |
| Edge node | `{service_id, name, type}` | `GET .../edge_nodes?limit&include_source_artifactory` |
| Distribution permission | `id, name, resourceType DESTINATION, distribution_destinations[{siteName, cityName, countryCodes[]}], principals{users{<name>: actions}, groups{...}}` | maps onto the Access v2 `destination` resource already in the internal permission shape ([0006](decisions/0006-authorization-native-permission-model.md)) |
| Bundle retention config | `automatic, num_of_versions_to_keep_per_release_bundle, max_days_to_keep, cron_pattern, max_deletions_per_background_run` | audit entries `{bundle_name, bundle_version, operation DELETE|KEEP|UNKEEP, created, created_by}` |
| Artifactory bundle view | `{bundles{<name>: [{version, created, status}]}}`; version detail `{name, version, description, created, artifacts[{repo_path, checksum, props[]}]}` | `type` query selects source or target bundles |

### Clients

- JFrog CLI `jf ds` (`rbc` create, `rbu` update, `rbs` sign, `rbd` distribute, `rbdel`
  delete) *(command names from the CLI documentation, unverified against 2.126.0)*.
- Terraform: `artifactory_distribution_public_key` (trusted keys at
  `/artifactory/api/security/keys/trusted`, already specified), webhooks for the
  `release_bundle`, `distribution`, `artifactory_release_bundle` and `destination` domains,
  and the `releasebundles` repository type (`artifactory_federated_releasebundles_repository`).
- Release bundles v2 (`/lifecycle`) stay out of scope. The six v2 distribution operations
  and `trigger_xray_scan` are specified for parity and answer 501
  ([0023](decisions/0023-release-bundles-and-distribution.md)).
- The `archive` flag on version delete is accepted and ignored; bundle import reads from
  the directory configured as `distribution.import_dir` (default `<data_dir>/import`).
- In 7.x release bundles v1 remain available but are not forward-compatible with release
  bundles v2 (7.63.5, [legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf)). The Replicator for v1 was deprecated in
  7.84.10 without changing what Distribution does
  ([deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations)). From
  7.146.7 the Artifactory bundle view also answers for release bundles v2 with
  `type=target`. Those release notes also list `GET .../{name}/{version}/status?type=target`,
  `GET .../{name}/{version}/artifacts` and a target-side
  `DELETE /api/release/bundles/{name}/{version}`, which the API reference does not
  document **[I]** ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed),
  [API index](https://docs.jfrog.com/artifactory/reference/release-bundles-v1-api/llms.txt)).

Timing: P15.

## 2. Cold-storage archive and retention

### What it does in JFrog

Two mechanisms share the purpose of moving rarely used content to cheaper storage while
keeping it findable and restorable. Self-hosted cold storage (Enterprise X and
Enterprise+) pairs a live Artifactory with a separate cold Artifactory through a binding
identified by a namespace; archive policies run on a schedule or on demand, select
artifacts or whole package versions by repository, path, name, properties and age (or a
custom AQL query), move the binaries and metadata to the cold instance and remove them
from the live one. Archived items are searched from the live instance and restored in the
background, to their original location with a fallback path or to a new location; restore
leaves the cold copy in place. Smart archiving (`/api/archive/v2`) has package-level
policies shaped like cleanup policies and archives to an archive instance in JFrog Cloud:
on SaaS with Enterprise+ from 7.111, and for a self-hosted active instance ("hybrid smart
archiving", Enterprise+) from 7.117.14. One active instance pairs with one archive
instance ([archive](https://docs.jfrog.com/administration/docs/archive.md)).

Sources: https://docs.jfrog.com/administration/docs/cold-storage.md,
https://docs.jfrog.com/administration/docs/archive.md,
https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt.

### Endpoint groups (30 operations)

| Group | Paths | Status codes |
|---|---|---|
| Archive policies (cold storage) | `GET /artifactory/api/retention/archive/policies`; `GET/POST/PUT/DELETE .../policies/{policyKey}`; `POST .../{policyKey}/trigger`, `/stop`, `/preview`; `POST .../policies/preview`, `.../convertToAql` | create 201, 409 on duplicate; trigger and stop 202; delete 200 |
| Executions | `GET /artifactory/api/retention/archive/executions?from&to&searchStr`; `GET /artifactory/api/retention/restore/executions?from&to` | 200 |
| Search and restore | `POST /artifactory/api/retention/search` (AQL, items domain), `POST .../searchPackages`; `POST /artifactory/api/retention/restore` | search 200; restore 202 with `restore_operation_id` |
| Namespace | `GET /artifactory/api/retention/namespace` | 200 `{jfrogColdStorageNamespaceId}` |
| Smart archiving v2 | `GET /artifactory/api/archive/v2/packages/policies`, `.../view`; `GET/POST/PUT/DELETE .../{policyKey}`; `POST .../{policyKey}/enablement`; `PUT .../{policyKey}/run`; `PUT .../runs/{runId}`, `.../runs/forceStopAll`, `.../forceDisableAll`; `GET .../runs/summaryReports`, `.../runs/downloadReport`; `POST /artifactory/api/archive/v2/restore/packages` | create 201, 409; run 200; stop 202 |

### Entities

| Entity | Fields | Notes |
|---|---|---|
| Archive policy | `key (req), enabled (req), cronExp (req), description, packageRetention, aqlQuery, searchCriteriaForm{...}, packageSearchCriteriaForm{packageType, componentName, version, packageRepos[], createdBefore, downloadedBefore}, durationInMinutes, expirationTimeInMonths, skipTrashcan`; response adds `lastRunStatus`, `nextExecutionTime` | `expirationTimeInMonths` purges archived content from the cold tier |
| Execution status | `policyKey, state PENDING|IN_PROGRESS|IN_PROGRESS_WITH_ERRORS|COMPLETE|COMPLETE_WITH_ERRORS|FAILED, completedNumberOfArtifacts, failedNumberOfArtifacts, totalNumberOfArtifacts, progressPercentage, startTime, finishTime, totalByteSize, successByteSize, failureByteSize, destination, destinationConfiguration, triggeredBy` | shared by archive and restore executions |
| Restore request | `restoreType PACKAGE_RESTORE|ARTIFACT_RESTORE (req), itemsToRestore[] (req), restoreToOriginal, fallbackDestinationPath, centralDestinationPath, addNamespace` | async; restore by folder path is not supported in JFrog |
| Smart archiving policy | the cleanup-policy envelope ([07](07-webhooks-retention-projects.md)): `key, description, cronExp, durationInMinutes, enabled, skipTrashcan, projectKey, searchCriteria{packageTypes, repos, excludedRepos, includedPackages, excludedPackages, includeAllProjects, includedProjects, createdBeforeInDays, lastDownloadedBeforeInDays, keepLastNVersions, includedProperties, excludedProperties, includedPackageVersions, excludedPackageVersions}` | reuses the cleanup run model |
| Package restore | `restoreToOriginal, fallbackDestinationPath, newDestinationPath, manifestPaths[]` → `{runSummaryId, responseMessage}` | |
| Archive search result | `{results[], range{start_pos, end_pos, total}}` | same envelope as AQL |

### Clients

- Terraform: `artifactory_archive_policy` (calls `/artifactory/api/archive/v2/packages/policies/{key}`
  per [08](08-terraform-providers.md)).
- JFrog limits worth keeping as defaults: about ten concurrent policies, 30,000 artifacts
  per restore run (`artifactory.retention.warm.restore.artifact.limit`).

Under the blob-tier decision there is no cold instance to bind, so the `artifactory-cold`
pairing use case answers 404. Timing: P16.

## 3. Platform federation and topology

### What it does in JFrog

Three related surfaces describe and synchronise a set of JFrog Platform Deployments
(JPDs). Access Federation copies users, groups, permissions and tokens one way from a
source Access service to target services, with include and exclude filters on permission
and group names, a full broadcast to resynchronise, and a stale-target window after which a
silent target stops receiving updates. Platform Federation (`/pfed`) groups JPDs into
named federations with connection groups and federated entity types, with a leader adding
follower JPDs by pairing token. Grid (`/grid`) lists JPDs with location and status,
defines topologies of sites and reports monitoring of synchronised entities per target.
JFrog sells this as Enterprise+ and wires it through Mission Control and the Circle of
Trust.

In 7.x, JFrog Platform Federation, the Grid product built on the pfed service, also
syncs projects, members, roles, global and project stages, users, groups, permission
targets and access tokens as global entities across a mesh of sites (no star topology).
It reached SaaS in 7.154.0 as an Enterprise+ early-access feature and does not yet take
self-managed sites ([jfrog-grid](https://docs.jfrog.com/administration/docs/jfrog-grid.md)).
Its documented API, the Curation Federation (pfed) reference, still names only Curation
entity types, so how the Grid product carries projects and identity is **[I]**. Access
Federation targets use `<base>/access`: 7.90.5 dropped the
`<base>/artifactory/api/access` form ([7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed)).

Sources: https://docs.jfrog.com/administration/docs/access-federation.md,
https://docs.jfrog.com/administration/docs/jfrog-grid.md,
https://docs.jfrog.com/administration/reference/jfrog-platform-access-federation-api/llms.txt.

### Endpoint groups (25 operations)

| Group | Paths | Status codes |
|---|---|---|
| Access Federation | `GET/POST /access/api/v1/system/federation`; `GET/PATCH/DELETE .../{serverName}`; `PUT .../{serverName}/full_broadcast`; `POST .../validate_server` | create 201, 409; delete 204; full broadcast 202, 204 or 304; validate 204 |
| Platform federations | `GET/POST /pfed/api/v1/federations`; `GET/DELETE .../{id}` | create 201, 409; delete 204 |
| Follower JPDs | `GET/POST /pfed/api/v1/jpd`; `GET/PUT/DELETE .../{id}` | add 201, 409; update 204; delete 204 |
| Grid | `GET /grid/api/v1/jpds?name&tag&url&status`; `GET/POST /grid/api/v1/topology`; `GET/PATCH/DELETE .../{key}`; monitoring `GET .../{key}/monitoring/entities[?entity_type&status&page_token]`, `.../entities/summary`, `.../entities/{entity_type}/{entity_id}` | create 201, 409; delete 204, 409 when in use |

### Entities

| Entity | Fields | Notes |
|---|---|---|
| Access Federation target | `name (req), url (req), entities[] (USERS, GROUPS, PERMISSIONS, TOKENS), permission_filters{include_patterns, exclude_patterns}, group_filters{...}, active, force_override` | `force_override` is write-only on create |
| Platform federation | `id, name (req), description, connection_groups{<group>: {target_jpd_ids[]}} (req), entities[{type}] (req)` | the entity types the oracle documents are Curation types; artifactweave accepts and stores them for wire compatibility but they carry no data. Identity replication is configured through Access Federation |
| Follower JPD | `id (req), url (req), pairing_token (req, write-only)` | the pairing token comes from the follower's pairing endpoint |
| JPD | `id, name, url, base_url, local, status{code ONLINE|OFFLINE|PARTIAL|UNAUTHORIZED, message}, location{city_name, country_code}, licenses[{type}]` | artifactweave reports a fixed licence type |
| Topology | `key, name (req), description, created, modified, sites[{site_name (req), is_current, location{city_name, country_code}, status Connected|Disconnected, status_description}] (req)` | |
| Monitoring entity | `entity_id, entity_type, targets[{source_jpd_id, source_site_name, target_jpd_id, target_site_name, status success|failure|in_progress|pending|sent, status_description, modified}]`; summary `counts{success, failure, in_progress}` per entity type | derived from the peer outbox cursors |

### Clients

The `jfrog/platform` provider has no resource for Access Federation, pfed or grid in
v2.2.11 *(unverified against later releases)*; these surfaces are used from the UI and
scripts. Timing: P14.

## 4. Federated repositories

### What it does in JFrog

A federated repository is a local repository whose content and configuration are kept in
step across up to ten members on different JPDs, every member accepting writes. Members
exchange events (deploy, delete, move, copy, property changes, optionally statistics)
over a dedicated federation service and fetch binaries they lack. A full sync rebuilds a
member from another; status endpoints report per-member connection state, lag, failed
binary transfers and inconsistent configurations. A local repository can be migrated to
federated and back. Enterprise X and Enterprise+ only, from 7.18.3, and at most ten
members ([members](https://docs.jfrog.com/artifactory/docs/federation-members-and-topology.md)).

In 7.x the federation service is the standalone Artifactory Federation Service (RTFS):
SaaS from 7.104.2, self-managed from January 2025, with PostgreSQL for its own store
([RTFS](https://docs.jfrog.com/artifactory/docs/artifactory-federation-service-rtfs.md)).
Its context path moved from `/artifactory/service/rtfs` to `/rtfs` in 7.111.12
([7.111.12](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-711112-self-managed)). 7.133.3 announced the sunset of the legacy
federation embedded in Artifactory, after an 18-month migration period, and develops new
features such as unidirectional sync for RTFS only ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)).

Sources: https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt,
https://docs.jfrog.com/artifactory/docs/access-federation-vs-repo-federation.md.

### Endpoint groups (22 operations, plus the pairing endpoints)

| Group | Paths | Status codes |
|---|---|---|
| Membership and conversion | `POST /artifactory/api/federation/migrate/{repoKey}`, `.../convertToLocal`, `.../removeMember`, `.../configSync/{repoKey}` | 200, 400, 404 on convert |
| Synchronisation | `POST .../fullSync/{repoKey}`, `.../forceFullSync/{repoKey}`, `.../recovery?repoKey&force`, `.../reconnect`; `PUT .../propagateCreatedTime`, `.../prioritization/{repoKey}` | 200; reconnect 424 when the federation client fails |
| Status | `GET .../state`, `.../inconsistentRepos`, `.../status/repo/{repoKey}` (v1 and `/api/v2/...`), `.../status/stateList?status`, `.../status/stateSummary`, `.../status/connectionStates/{repoKey}`, `.../status/mirrorsLag?thresholdTimeInMs`, `.../status/unavailableMirrors` | 200; 405 when federation is disabled. In 7.x, `state`, `status/repo` (v1 and v2), `mirrorsLag` and `unavailableMirrors` are "Legacy Federation Monitoring": RTFS does not support them ([API index](https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt), [sync state](https://docs.jfrog.com/artifactory/reference/getfederationsyncstate.md)) |
| Failed binaries | `GET .../binaries/failures/list?repoKey&limit&page`, `PUT .../replay?repoKey&sha1`, `DELETE .../delete?repoKey&sha1` | 200 |
| Pairing | `POST/PUT/DELETE /artifactory/api/v1/service_trust/pairing/{usecase}`; `POST /access/api/v1/service_trust/pairing/{useCase}` | 200, 404 on unknown use case |

### Entities

| Entity | Fields | Notes |
|---|---|---|
| Federated repository | the local repository fields plus `members[{url, enabled}]` (`enabled` a boolean, documented as a string), `proxy`, `disableProxy` ([03](03-repository-configuration-model.md)) | member `url` is `<base>/artifactory/<repoKey>` |
| Sync state | `[{key, status SYNCING|FULL_SYNC_RUNNING|DISABLED|OUT_OF_SYNC, members[{remoteKey, remoteUrl}]}]` | |
| State details | `{federations[{localRepoKey, priority HIGH|STANDARD, memberInfoList[{url, repoKey, aggregatedStatus, isSupported, isDisabled, isInLag}]}]}` | |
| Mirror lag | `[{localRepoKey, remoteUrl, remoteRepoKey, lagInMS, eventRegistrationTimeStamp}]` | |
| Unavailable mirror | `[{localRepoKey, remoteUrl, remoteRepoKey, status}]` | |
| Failed binary task | `[{sha1, repoKey, creationTime, failuresCount}]` | |
| Repository status | `{localKey, binariesTasksInfo{inProgressTasks, failingTasks}, mirrorEventsStatusInfo[], federatedArtifactStatus{}}` | the last two are untyped in the oracle |
| Pairing token | `{pairing_token}`; exchange body `{pairing_token}` → `{message}` | On the Artifactory endpoint 7.x documents `federated-repo/*` (two-way federated repository trust), `federated-repo/r` (receive-only, for unidirectional federation), `artifactory-cold` and `artifactory-cold/<namespace>`, and answers 404 to anything else ([pairing](https://docs.jfrog.com/artifactory/reference/generatepairingtoken.md)); the Access endpoint's use cases include `mission-control`, `bridge` and `platform-federation`; artifactweave adds its own (see [0021](decisions/0021-peer-trust-and-sync-protocol.md)). `artifactory-cold` answers 404 because cold storage is a blob tier, not a paired instance |

### Clients

Terraform `artifactory_federated_<type>_repository` (37 types, eight in artifactweave's
scope plus `generic`), which `PUT`s the repository with `members` and reads it back. With
federation real, a multi-member plan is accepted when every member URL belongs to a paired
peer; the earlier rule that rejected multi-member lists with 400 is withdrawn. Timing: P13.

## 5. Workers

### What it does in JFrog

A worker is a TypeScript function attached to a platform event (an action). Before-actions
run in the request path and can stop it (for example refuse a download); after-actions run
asynchronously after the event; HTTP-triggered workers (`GENERIC_EVENT`) run when called;
scheduled workers (`SCHEDULED_EVENT`) run on a cron expression, from 7.104.5
([7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)).
Each worker carries a key, the action, the source code, an enabled flag, a debug flag, a
shared flag and filter criteria on repositories. The runtime is a sandbox without Node
APIs or imports. JFrog's SaaS limits are 20 seconds of execution time, 1 second of CPU
time, 64 MB of memory and 100,000 characters of source, with an execution rate ceiling
(1,200 executions a second, 120 a second sustained for a minute) and a back-off that
blocks a worker after ten failures in ten minutes, up to 17 minutes, reset daily;
self-managed installations configure the limits in the Workers YAML
([limits](https://docs.jfrog.com/administration/docs/workers-limitations.md),
[YAML](https://docs.jfrog.com/installation/docs/worker-yaml-configuration.md)). The YAML's
`enableLocalNetworkValidation` (on by default) stops `clients.axios` from calling localhost,
"unicast IPs" and JFrog's domain names.

Sources: https://docs.jfrog.com/administration/reference/createworker.md,
https://docs.jfrog.com/administration/docs/typescript-code-for-workers.md,
https://docs.jfrog.com/administration/docs/workers-limitations.md,
https://docs.jfrog.com/administration/docs/worker-event-types.md.

### Endpoint groups (15 operations)

| Group | Paths | Status codes |
|---|---|---|
| Definitions | `GET/POST/PUT /worker/api/v1/workers`; `GET/DELETE .../workers/{workerKey}` | create 201; update 200, 404; delete 204 |
| Catalogue | `GET /worker/api/v1/actions`, `GET /worker/api/v2/actions` (metadata), `GET .../scaffold/tsconfig` | 200 |
| Execution | `POST .../execute/{workerKey}` (HTTP-triggered), `POST .../test`, `POST .../test/{workerKey}`, `POST .../rerun/{workerKey}`, `GET .../execution_history?maxItems&start&end` | 200, 404 |
| Health | `GET .../system/readiness`, `.../system/liveness` | 200 |

### The action catalogue

The actions below are those of the create-worker definition as `api/src/worker/paths-workers.yaml`
specifies them, grouped by the hook point they need in artifactweave. The current event
catalogue ([event types](https://docs.jfrog.com/administration/docs/worker-event-types.md))
names 41 events in prose, and each maps to one of them. It shows no identifiers, and four
actions here have no catalogue entry (`AFTER_SAVE`, `BEFORE_UPLOAD_REQUEST`,
`BEFORE_REMOTE_INFO_REQUEST`, `AFTER_WORKLOAD_STATE_CHANGE_REQUEST`), so whether 7.x still
accepts them is **[I]**:

| Hook point | Actions |
|---|---|
| Download path (before) | `BEFORE_DOWNLOAD_REQUEST`, `BEFORE_DOWNLOAD`, `BEFORE_REMOTE_DOWNLOAD` |
| Download path (after) | `AFTER_DOWNLOAD`, `AFTER_REMOTE_DOWNLOAD`, `AFTER_DOWNLOAD_ERROR` |
| Remote resolution | `BEFORE_REMOTE_INFO`, `BEFORE_REMOTE_INFO_REQUEST`, `ALT_REMOTE_PATH`, `ALT_REMOTE_CONTENT`, `ALT_RESPONSE`, `ALT_ALL_RESPONSES` |
| Upload and create | `BEFORE_UPLOAD_REQUEST`, `BEFORE_UPLOAD`, `BEFORE_CREATE`, `AFTER_CREATE`, `AFTER_SAVE` |
| Copy, move and delete | `BEFORE_COPY`, `AFTER_COPY`, `BEFORE_MOVE`, `AFTER_MOVE`, `BEFORE_DELETE`, `AFTER_DELETE` |
| Properties | `BEFORE_PROPERTY_CREATE`, `AFTER_PROPERTY_CREATE`, `BEFORE_PROPERTY_DELETE`, `AFTER_PROPERTY_DELETE` |
| Repositories | `BEFORE_REPO_CREATE`, `AFTER_REPO_CREATE`, `BEFORE_REPO_UPDATE`, `AFTER_REPO_UPDATE`, `BEFORE_REPO_DELETE`, `AFTER_REPO_DELETE` (repository events reached self-managed in 7.161.15) |
| Build-info | `BEFORE_BUILD_INFO_SAVE`, `AFTER_BUILD_INFO_SAVE` |
| Tokens | `BEFORE_CREATE_TOKEN`, `BEFORE_REVOKE_TOKEN`, `BEFORE_TOKEN_EXPIRY` (from 7.111.4) |
| Replication | `BEFORE_DIRECTORY_REPLICATION`, `BEFORE_FILE_REPLICATION`, `BEFORE_PROPERTY_REPLICATION`, `BEFORE_DELETE_REPLICATION`, `BEFORE_STATISTICS_REPLICATION` |
| Other | `AFTER_WORKLOAD_STATE_CHANGE` and `AFTER_WORKLOAD_STATE_CHANGE_REQUEST` (JFrog runtime workloads; no artifactweave equivalent), `GENERIC_EVENT` (HTTP-triggered), `SCHEDULED_EVENT` (cron) |

### Entities and runtime contract

| Entity | Fields | Notes |
|---|---|---|
| Worker | `key (req), action (req), sourceCode (req), enabled (req), description, debug, shared, filterCriteria{artifactFilterCriteria{repoKeys[], includePatterns[], excludePatterns[], anyLocal, anyRemote, anyFederated}}` | `projectKey` on project-scoped workers *(unverified)* |
| Action metadata (v2) | `action{application, name}, description, samplePayload, sampleCode, typesDefinitions, supportProjects, filterType, mandatoryFilter, wikiUrl, async, requestSchema, executionRequestType` | artifactweave writes its own samples and type definitions |
| Execution record | the worker's return value under `result`, plus logs and timing *(field names undocumented)* | `GET .../execution_history` |
| Function signature | default export `(context, data) => Promise<result>`; `data` carries the event payload and `userContext{id, isToken, realm}` | |
| Context | `baseUrl`, `platformToken`, `clients.platformHttp` (calls back into the platform; `request(method, body, headers)` beside the verb methods), `clients.axios` (outbound HTTP; `getAndForget`, `postAndForget` and `deleteAndForget` return without waiting, from Workers 1.542 self-managed, [Workers releases](https://docs.jfrog.com/releases/docs/jfrog-workers-release-information)), `wait(ms)`, `state` (persisted between runs: 10 items, 40-character keys, 250-character values by default), `getRemainingExecutionTime()`, `crypto{hash, hmac (sha256, sha512), randomBytes (at most 4096)}` | no `setTimeout`, no Node modules, no `import`; TypeScript targeting ES2022 |
| Result | `{status, message}`; `DOWNLOAD_PROCEED` is the documented status for download hooks; the stop and warn statuses per action are *(unverified)* | |

### Clients

Terraform `platform_workers_service` (`/worker/api/v1/workers`, [08](08-terraform-providers.md)).
The JFrog CLI has a `jf worker` command group for scaffolding and deploying workers
*(unverified against 2.126.0)*. Timing: P17.

## Summary of status-code traps

| Trap | Where |
|---|---|
| Create returns 201 on cold storage, smart archiving, pfed, grid, workers and Access Federation, but 200 on Distribution bundles | sections 1 to 5 |
| 405 means "service disabled" on federation status and bundle retention, not "wrong method" | sections 1 and 4 |
| `full_broadcast` answers 304 when nothing changed | section 3 |
| `reconnect` answers 424 when the peer is unreachable | section 4 |
| Distribution answers 200 for a dry run and 202 for a started distribution on the same path | section 1 |

## Artifactory 7.x check

Checked on 2026-10-17 against the self-managed release notes up to 7.161.32
([releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), the
legacy notes for 7.0 to 7.83 ([PDF](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf)), the Workers release notes and the
current product pages cited above. The sections above were corrected in place; this table
lists what each claim became.

| Claim | 7.x | Source |
|---|---|---|
| Release bundles v1 and Distribution endpoints and statuses | Confirmed; v1 kept beside v2. The extra bundle-view paths in the 7.146.7 notes are **[I]** | [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations) |
| Cold storage pairs a live and a cold self-hosted instance (Enterprise X, Enterprise+) | Confirmed; 30,000 artifacts per restore run (`artifactory.retention.warm.restore.artifact.limit`) confirmed | [cold storage](https://docs.jfrog.com/administration/docs/cold-storage.md) |
| Smart archiving is the SaaS variant | Changed: SaaS from 7.111 and hybrid self-hosted from 7.117.14, both Enterprise+, archiving to an instance in JFrog Cloud | [archive](https://docs.jfrog.com/administration/docs/archive.md) |
| Archiving from a federated repository | Confirmed limitation: archive and restore events are not synced to the other members (since 7.27.3, still documented) | [cold storage](https://docs.jfrog.com/administration/docs/cold-storage.md) |
| A package version already in cold storage | Added: archiving it again keeps the older archived copy and skips the newer one with an info log (7.71.2) | [legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf) |
| Platform federations carry Curation entity types only | Confirmed for the documented pfed API; the Grid product also syncs projects and identity on SaaS, how is **[I]** | [jfrog-grid](https://docs.jfrog.com/administration/docs/jfrog-grid.md), [pfed](https://docs.jfrog.com/administration/reference/curation-federation-pfed-service.md) |
| Access Federation target URLs | Changed: `<base>/access`, not `<base>/artifactory/api/access` (7.90.5) | [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed) |
| Federated repositories: Enterprise X and Enterprise+, up to ten members | Confirmed (from 7.18.3) | [members](https://docs.jfrog.com/artifactory/docs/federation-members-and-topology.md) |
| Members exchange events over a dedicated federation service | Changed: the standalone RTFS at `/rtfs` (7.111.12); legacy federation sunset announced in 7.133.3; four status operations are legacy-only | [RTFS](https://docs.jfrog.com/artifactory/docs/artifactory-federation-service-rtfs.md), [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) |
| Federation socket timeout | Added: 300,000 ms by default (7.111.8) | [7.111.8](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71118-self-managed) |
| Pairing use cases | Changed: on the Artifactory endpoint they are `federated-repo/*`, `federated-repo/r` (unidirectional, RTFS only) and the two `artifactory-cold` forms; `api/src/artifactory/paths-admin.yaml` already specifies the `federated-repo` pair | [pairing](https://docs.jfrog.com/artifactory/reference/generatepairingtoken.md) |
| Worker triggers: before, after and HTTP | Changed: scheduled workers from 7.104.5; the action catalogue grew to the list above | [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed), [event types](https://docs.jfrog.com/administration/docs/worker-event-types.md) |
| Worker limits and back-off | Confirmed, with the rate ceiling and the 17-minute back-off made explicit | [limits](https://docs.jfrog.com/administration/docs/workers-limitations.md) |
| Worker context | Added: fire-and-forget `axios` methods and `platformHttp.request`; JFrog blocks `axios` calls to local and JFrog hosts by default | [Workers releases](https://docs.jfrog.com/releases/docs/jfrog-workers-release-information), [YAML](https://docs.jfrog.com/installation/docs/worker-yaml-configuration.md) |
| Worker stop and warn statuses per action | Still **[I]** | |
