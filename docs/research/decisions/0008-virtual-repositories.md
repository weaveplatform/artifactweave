# 0008: Virtual repositories

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 25 section 11](../25-npm.md#11-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)). Rules R1 to R11 amended on 2026-11-15 against the Artifactory 7.161.26 sources, JFrog's documentation, the 7.x release notes and public Jira ([#1530](https://github.com/weaveplatform/artifactweave/issues/1530)).

## Context

A virtual repository aggregates local, remote and other virtual repositories under one
URL. Clients resolve through it and, for most formats, publish through it. Artifactory
searches local members before remote caches and remote caches before remote upstreams
(R1), writes to the `defaultDeploymentRepo` (R6), merges per-format metadata across
members and, for some package types, caches the merged result for
`virtualRetrievalCachePeriodSecs` (R8).

## Decision

`internal/artifacts/core/virtual` holds the shared engine, as decision
[0044](0044-modular-monolith-migration.md) and the target layout
([5-modular-monolith](../architecture-options/5-modular-monolith.md), lines 659-665)
place it: `resolve.go` for member order, the `defaultDeploymentRepo` and
`hideUnauthorizedResources`; `merge.go` for the `Merger` contract a format implements;
`cache.go` for `virtualRetrievalCachePeriodSecs`. Each format's merge lives in
`internal/artifacts/ecosystem/<type>/virtual.go`.

Rules R1 to R11 are Artifactory 7.x's, and P10 implements them. Until a P10 ticket lands,
the code answers as the P9c sections below and the format guides describe. Each rule names
its sources. Class names and line numbers are 7.161.26's, under
`backend/core/src/main/java/org/artifactory/` unless another path is given. **[I]** marks
an answer that no source settles; #216 lists each one for the comparison in P19.

### R1. Member order

A virtual repository searches its real members in six groups:

1. local and federated members with `priorityResolution`;
2. the caches of remote members with `priorityResolution`;
3. the upstreams of remote members with `priorityResolution`;
4. the other local and federated members;
5. the other remote members' caches;
6. the other remote members' upstreams.

Within a group the order is the configured order after nested virtuals are flattened depth
first (R2). A remote listed before a local is therefore searched after it, unless only the
remote has `priorityResolution`. Package handlers walk the same list and leave the caches
out while remote members remain in it, so each remote is asked once.

Sources: `repo/virtual/VirtualRepositoriesResolver.assembleSearchRepositoriesList` 104-143,
over `VirtualRepositoryConfigResolverImpl.getRepoConfigs` 61-90, which lists every local
and federated member before every remote one; `ph/PackageRepoServiceImpl.getSubRepositories`
318-339; JFrog's [Virtual Resolution Order](https://docs.jfrog.com/artifactory/docs/virtual-repositories#virtual-resolution-order):
"local repositories first, then remote repository caches, and finally remote repositories
themselves", each in "the order in which they are listed in the configuration".

### R2. Nesting, cycles and duplicates

One set of visited virtual keys spans the whole walk, the requested virtual's own key
included. A virtual reached a second time, through a cycle, a diamond or as its own member,
is skipped without an error. A real member reached twice keeps its first position. When the
request names a path, a nested virtual whose own include or exclude patterns reject it is
dropped with its members. 7.x has no cycle refusal at save time: its code saves a cycle and
JFrog's page only warns "Be careful not to create an infinite loop of nested repositories".
Whether the UI or the REST validator refuses one is **[I]**.

Sources: `repo/virtual/VirtualRepositoryConfigResolverImpl.VirtualConfigResolverHelper.resolve`
109-143; `repo/virtual/VirtualResolverRequestFilter.accepts` 90-109 and `comparePaths`
111-128; the "Nesting" part of JFrog's
[Virtual Repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories) page.

### R3. Fall-through and the final status

Every member miss passes over to the next member: not found, a member's blackout or pattern
refusal, an offline remote and a Read denial alike. The first member that has the item
answers. A file whose properties conflict with a matrix parameter of the request passes over
like a miss (`PropertiesImpl.matchQuery`'s `CONFLICT`); one that lacks a parameter the
request names (`NO_MATCH`) is only the closest match: the walk looks on in local members, and
in remote members that synchronize properties, for a file that carries it, and answers the
closest match when none does (`processStandard`'s `closestMatch` and
`isNotSynchronizeProperties` 479-481). On the item routes, and on the Maven routes, a walk that finds nothing answers 403
only if a member denied Read, and otherwise 404 `Could not find resource`. Package handlers
count a member the caller cannot read as not found, so they answer their own not-found and
never 403: [7.146.25](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714625-self-managed)
fixed NuGet V2 (RTDEV-88743, "An unreadable constituent is now treated as 'not found'
during virtual resolution"), [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)
a Docker push's tag check (RTDEV-81046), and
[RTFACT-30175](https://jfrog.atlassian.net/browse/RTFACT-30175) the Docker catalog. 7.x's npm
and Go handlers are closed code, so that they answer the same is **[I]**. Whether a member's
5xx or transport failure stops the walk is **[I]**: `processStandard` treats every resource
it did not find alike, so the inferred answer is that the walk passes over it and answers 404
unless a member denied Read.

Sources: `repo/virtual/ArtifactoryVirtualDownloadStrategy.processStandard` 209-260;
`repo/db/DbLocalRepo.checkDownloadIsAllowed` 212-219 and
`repo/RemoteRepoBase.checkDownloadIsAllowed` 744-758 (R10).

### R4. Hiding unauthorized resources

When the virtual's `hideUnauthorizedResources` (default false) or the global security setting
of that name is on, a 401 or 403 that the walk would answer becomes 404
`Resource not found; Path: '<repoPath>'`. Otherwise an anonymous caller who would get 403
gets the 401 challenge; the challenge's body is **[I]**, since `sendAuthorizationRequired`
is closed code: artifactweave writes `Unauthorized`, as 6.20's
`HttpArtifactoryResponse.sendAuthorizationRequired` does and as a direct request's challenge
does ([0032](0032-package-format-handlers.md#decision)).

Sources: `engine/DownloadServiceImpl.respondResourceNotFound` 1465-1497,
`shouldHideUnauthorisedResponse` 1499-1518 and `shouldSendAuthRequired` 1460-1463;
`base/common/src/main/java/org/artifactory/repo/VirtualRepositoryConfigurationImpl` line 74;
JFrog's [Hide Existence of Unauthorized Resources](https://docs.jfrog.com/administration/docs/security-configuration#hide-existence-of-unauthorized-resources).

### R5. Include and exclude patterns

The virtual's own patterns are checked first, on the request path, and apply to
administrators too. When they reject the path no member is searched, and the answer is 404
`No repository found to serve the request for <key>:<path>`. Each nested virtual's patterns
then drop that virtual (R2), and each real member's own patterns apply to that member alone:
a member whose patterns reject the path is passed over (R3). Merged listings and indexes are
filtered by the patterns before they are paged
([RTFACT-26524](https://jfrog.atlassian.net/browse/RTFACT-26524) for the Docker catalog,
[RTFACT-29600](https://jfrog.atlassian.net/browse/RTFACT-29600) for PyPI,
[RTFACT-30802](https://jfrog.atlassian.net/browse/RTFACT-30802) for Terraform).

Sources: JFrog's [Virtual Repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories)
page ("Include and Exclude Patterns apply to all users, including admin users");
`repo/virtual/ArtifactoryVirtualDownloadStrategy.getInfoFromSearchableRepositories` 158-169;
`repo/RealRepoBase.assertValidPath` 126-143; decision
[0032](0032-package-format-handlers.md#r5-virtual-repositories), R5.

### R6. Writes

An upload to a virtual goes to its `defaultDeploymentRepo`, which is a local or federated
member. With none set, it answers 405 with `Allow: GET` and
`No local repository was configured as local deployment repository for the (<key>) virtual repository.`
A virtual cannot be another virtual's `defaultDeploymentRepo`
([RTFACT-31328](https://jfrog.atlassian.net/browse/RTFACT-31328) is an open request for it).
A save that sets a remote or virtual repository, the virtual itself included, as the
`defaultDeploymentRepo` answers 400
`Cannot set repository reference '<ddr>' as the default deployment repository for virtual repository '<key>' since it is not a local repository`
**[I]**: 7.161.26 fails that save on a remote with a `ClassCastException` and drops a virtual
silently (`base/config/.../descriptor/repo/VirtualRepoDescriptor` 357-361), neither of which
is meant, while its UI keeps only a local or federated one (`RepoConfigDescriptorBuilder`)
and JFrog's deploy page says to "set one of the local repositories aggregated by the virtual
repository as the default deployment target". The other save rules for the
`defaultDeploymentRepo` are R11's.
DELETE through a virtual answers 404 with an empty body, since 7.x deletes only in local,
federated and cache repositories
([RTFACT-26096](https://jfrog.atlassian.net/browse/RTFACT-26096) is an open request to
forward it). Copy or move into a virtual is refused with
`Target repository <key> is a virtual repository. <copy|move> to virtual repositories is not allowed.`;
its status is **[I]**, inferred 400 from the `IllegalArgumentException` it throws.

Sources: `util/UploadServiceUtils.getTargetRepository` 248-261;
`engine/UploadServiceImpl.sendInvalidTargetRepositoryError` 337-352;
`repo/webdav/methods/DeleteMethod.handle` 75-83;
`repo/service/mover/RepoPathMover.throwExceptionIfTargetRepoIsForbidden` 121-138; JFrog's
[Configure Virtual Repositories for Deployment](https://docs.jfrog.com/artifactory/docs/deploy-artifacts#configure-virtual-repositories-for-deployment).

### R7. Requests from another Artifactory

`artifactoryRequestsCanRetrieveRemoteArtifacts` (default false) is about requests from
another Artifactory, not about requests between repositories of one instance. A request
comes from another Artifactory when it carries `X-Artifactory-Originated` or
`Origin-Artifactory`. While the flag is false, such a request skips the upstreams of remote
members, and their caches are still searched. artifactweave's upstream requests send both
headers, so a downstream artifactweave or Artifactory recognises them; the form of the host
id they carry is **[I]**. A download whose `X-Artifactory-Originated` holds more than one
value, one of them this node's host id, answers 404
`Recursive call detected for '<request>'. Returning nothing.` JFrog's page says the flag is
"Relevant for all package types except Swift, Terraform, and Composer". A Smart Remote keeps
its own loop guard ([Smart Remote synchronization](../../operations/smart-remote.md)).

Sources: `repo/virtual/VirtualRepositoriesResolver.addRemoteRepo` 145-159;
`base/common/src/main/java/org/artifactory/webapp/servlet/HttpArtifactoryRequest.isFromAnotherArtifactory`
102-105 and `isRecursive` 107-123; `engine/DownloadServiceImpl` 379-385;
`repo/HttpRepo.addDefaultHeadersAndQueryParams` 1658-1690; "Remote Requests Can Retrieve
Remote Artifacts" on JFrog's [Virtual Repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories)
page.

### R10. A member keeps its own rules

A member reached through a virtual answers under its own permission and download policy,
checked at the member's path (decision
[0032](0032-package-format-handlers.md#download-policy-rules-for-the-native-protocols-2026-11-11-1251), R4
and R5). In the walk 7.x checks a member's blackout and patterns before its Read permission,
so a refusal passes over silently and only a Read denial counts toward R3's 403. A request
to the member itself keeps decision 0032's order, Read first (DV-057). A remote member keeps
its own source rules: offline, blackout, `storeArtifactsLocally`, its retrieval policy, and
Read checked on its `<key>-cache` path. Filling or refreshing its cache from the upstream
also needs Deploy/Cache on that cache path (Annotate for a metadata file): JFrog's
[Permissions](https://docs.jfrog.com/administration/docs/permissions) page says Read on a
remote "only allows downloading from the remote cache", and 7.x checks the deploy path
before it fetches. A caller with Read alone therefore gets only what the cache holds. What
such a caller gets through a virtual for a file only a remote's upstream holds is **[I]**.
[RTFACT-30824](https://jfrog.atlassian.net/browse/RTFACT-30824) (closed without a
resolution) shows a package handler's answer: a read-only user got 404
`Could not find resource` for a PyPI page through a virtual over a Smart Remote that a
deploying user could read. On the item routes the walk finds the file from the upstream's
answer and the fetch is then refused, so the inferred answer is 403
`Rejected artifact download request: User <user> is not permitted to deploy '<path>' into '<remote>-cache:<path>'.`
(the 401 challenge for an anonymous caller). The remote policy checks the deploy path
since [#1587](https://github.com/weaveplatform/artifactweave/issues/1587), and a package
route's merge passes over a member whose fill is refused as over one that misses.

Sources: `repo/db/DbLocalRepo.checkDownloadIsAllowed` 212-219;
`repo/RemoteRepoBase.checkDownloadIsAllowed` 744-758 and its fetch, which calls
`assertValidDeployPathAndPermissions` (846-855; `repo/service/RepositoryServiceImpl`
2817-2870) on the cache path; `engine/DownloadServiceImpl` 1006-1022;
`repo/RealRepoBase.assertReadPermissions` 145-153.

### Per-format merges

Each format handler implements a merge for its non-unique metadata:

| Format | Merged artefact |
|---|---|
| Maven, Gradle, Ivy, SBT | `maven-metadata.xml` at artifact and snapshot level: union of versions, newest `lastUpdated`, newest snapshot |
| npm | packument `versions` union, `dist-tags` by member order, tarball URLs rewritten to the virtual |
| PyPI | simple index and project pages, union of files; JSON only when every member can |
| NuGet | registration pages, flat-container version lists and search results |
| Helm | `index.yaml` entries union, chart URLs rewritten; Helm and Helm OCI cannot share a virtual |
| Go | `@v/list` union; remote Go members are only reachable this way |
| Terraform | provider and module version lists |
| Docker, OCI | `tags/list` union, `_catalog` union; `resolveDockerTagsByTimestamp` picks the newest manifest for a tag |

### R8. Merged metadata cache

7.x keeps a virtual's merged metadata in the virtual's own storage and expires it by age: an
entry older than `virtualRetrievalCachePeriodSecs` is built again, a negative period (-1) is
always expired, and 0 builds it on every request (JFrog's page: "A value of 0 indicates no
caching"). An entry is dropped when a member answers 404 for it on a read. 7.x does not
invalidate a merged entry when a member is written, except for Helm: a write to any member
drops every cached index of each Helm virtual that contains it, through nesting
([RTFACT-25909](https://jfrog.atlassian.net/browse/RTFACT-25909), 7.31.10). Chef, Conda,
CRAN, Conan, Debian, Helm, npm and YUM (RPM) virtuals carry the field (model default 600);
JFrog's page also lists it for Alpine and OCI, default 7200, and the OCI entry is **[I]**
because the 7.161.26 model leaves OCI out. Whether 7.x stores a negative period through the
REST API is **[I]**. artifactweave merges the other types per request. It holds the cache in
each process, bounded, and keys an entry by what the caller may read, so no caller gets a
merge built from members it cannot read (Helm's index is per permission set,
[RTFACT-25936](https://jfrog.atlassian.net/browse/RTFACT-25936)). Coordination across nodes
is P35's ([shared remote retrieval policy](../../implementation/remote-cache-policy.md#concurrency-and-publication)).

Sources: `repo/virtual/VirtualRepo.isCachedResourceExpired` 658-662;
`repo/virtual/ArtifactoryVirtualDownloadStrategy.getInfoInternal` 90-136;
`base/common/src/main/java/org/artifactory/repo/VirtualRepositoryConfigurationImpl` 93-95;
"Metadata Retrieval Cache Period (sec)" on JFrog's
[Virtual Repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories) page.

## Rationale

7.x's member order (R1), its fall-through (R3) and per-format merging are what clients
observe; there is no simpler model that keeps `npm install` through a virtual over a local
and a remote behaving as it does on Artifactory.

**Alternatives considered:**
- *Resolving members in parallel and taking the first response*: faster, but changes
  which member wins and breaks `priorityResolution` semantics.
- *Walking members in plain configured order*, as this decision first said: simpler, but
  7.x serves a local member before a remote listed ahead of it (R1), so a client would get
  other bytes than Artifactory gives it.
- *Invalidating merged metadata on every member write*, as this decision first said:
  fresher, but it changes answers inside the cache period, which 7.x does for Helm alone (R8).

## Artifactory 7.x check

The decision stands against 7.161.32 as a plan for P10, with R1 to R11 checked against the
7.161.26 sources on 2026-11-15 (#1530). The 7.x notes add inputs for P10:
- **Projects.** A virtual repository may include repositories not assigned or shared to its
  project ([7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)).
- **npm dist-tags.** Custom dist-tags resolve to the first member that has the tag, unless
  `artifactory.npm.merge.latest.dist.tag.base.strategy.enabled` is set (from 7.75.3,
  [research 25](../25-npm.md#7-latest)).
- **npm metadata cache.** npm virtual repositories default to a two-hour metadata retrieval
  cache on SaaS ([SaaS 7.165.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71650-saas)).
- **Version tokens** resolve in virtual repositories ([research 27](../27-maven.md#13-artifactory-7x-check)).
- **Docker pushes.** A Docker push through a virtual repository answers 404, not 403, when a
  tag existence check meets members the caller cannot read ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), RTDEV-81046).

## Constraints

### R9. Storage API and browsing

The storage API answers a virtual from its local, federated and cache members only, never
from a live remote listing. File and folder info come from the first member item the caller
can read, with `repo` set to the virtual's key; a folder's children are the union of those
members' children. `?lastModified` and `?permissions` answer 400
`This method can only be invoked on local/cached repositories.` `?properties` answers from
the first member with non-empty properties, and otherwise 404 `No properties could be found.`
`?list` and `?stats` are supported: [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)
fixed `?list&deep=1` on a nested virtual (RTDEV-62248). HTML browsing of
`/<virtual>/<folder>/` lists the remote members' live children too. Two answers are
**[I]**: that the children ignore the virtual's own patterns, and that `?properties` checks
no Read on each member.

Sources: `web/rest/src/main/java/org/artifactory/rest/resource/artifact/ArtifactResource`
lines 420, 485-493, 550 and `prepareStorageInfoResponse` 569;
`repo/service/RepositoryBrowsingServiceImpl.getVirtualRepoItem` 727-759,
`getVirtualRepoItems` 762 and `getVirtualRepoBrowsableChildrenData` 370-428.

### R11. Package types

A virtual repository may have no members. Each member has the virtual's package type, with
three exceptions: a generic virtual takes members of any type; Maven, Gradle, Ivy, SBT and
P2 count as one type; OCI and Helm OCI count as one type. So Helm and Helm OCI cannot share a
virtual, and neither can Docker and OCI. A P2 virtual also takes a local Generic member,
which JFrog's [P2 Repositories](https://docs.jfrog.com/artifactory/docs/p2-repositories) page
names ("local Maven or Generic repositories") and 7.x's UI validator allows
(`RepoConfigValidator.filterByType`); whether the REST save refuses one, as the interceptor's
code would, is **[I]**. A P2 virtual's members of another type than P2 must be local.

7.x checks membership whenever the repositories API or the YAML configuration patch saves a
virtual, with these answers, each a 400:

- A member or `defaultDeploymentRepo` that does not exist, when the save adds it:
  `Cannot create or update repo '<key>': repo reference '<ref>' does not exist in the config descriptor.`
  A key the same request saves counts as existing, so a virtual may list itself (R2 skips it).
  A stored member that no longer exists is dropped, and a stored `defaultDeploymentRepo` the
  update keeps is cleared.
- A create, or a change of the `defaultDeploymentRepo`, naming one that is not a member:
  `Cannot set repository reference '<ddr>' as the default deployment repository for virtual repository '<key>' since it is not included in the repository references`.
  An update that takes the `defaultDeploymentRepo`'s member away and keeps it:
  `Cannot remove repository reference '<ddr>' since it is defined as the default deployment repository for the virtual repository '<key>'`.
  An update that changes neither is accepted.
- A member of a type the virtual does not take, checked only when the save changes the
  members or the `defaultDeploymentRepo`:
  `Virtual repository '<key>' has package type '<V>' but includes non-matching repository '<m>' with package type '<M>'`,
  where the types are `RepoType`'s names (`Npm`, `Maven`, `HelmOCI`, `YUM`). The create API
  refuses a mismatch from
  [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)
  (RTDEV-62756; [research 03](../03-repository-configuration-model.md),
  [#394](https://github.com/weaveplatform/artifactweave/issues/394)).
- A remote or virtual `defaultDeploymentRepo`: R6's answer.

No cycle is refused (R2). The interceptors run in no set order, so which message a save with
several faults gets is **[I]**; artifactweave checks in the order above. That each refusal
answers 400 is **[I]** too, from the exception mappers below, since the repositories
resource is closed code. artifactweave applies these rules from
[#1532](https://github.com/weaveplatform/artifactweave/issues/1532) (`repos.CheckMembers`).

Sources: `repo/interceptor/config/VirtualRepoConfigChangeInterceptor` 56-156, gated by
`artifactory.virtual.block.package.type.mismatch` (default true);
`repo/interceptor/config/RepoConfigReferencesInterceptor` `handleNonFederatedRepositoryRefs`
and `handleDefaultDeploymentRepoRef`; `repo/interceptor/config/DefaultDeploymentRepoInterceptor`
44-89; `base/config/src/main/java/org/artifactory/descriptor/repo/RepoType.java` line 124
(`MAVEN_GROUP`); `web/rest/.../exception/mapper/ConfigurationExceptionMapper` and
`web/rest-common/.../exception/mapper/IllegalArgumentExceptionMapper`; JFrog's
[Kubernetes Helm Chart Repositories](https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories)
page.

### Merged metadata freshness

Merged metadata is cached per process (R8): a write to a member shows through the virtual
once the entry expires, except for Helm, whose member writes drop the entry at once.

## Owned follow-up

P10 owns virtual resolution and permission-filtered metadata merging. Its backlog,
[INDEX #1518](https://github.com/weaveplatform/artifactweave/issues/1518), implements R1 to
R11 through these epics: the shared engine
([EPIC-ENG #1521](https://github.com/weaveplatform/artifactweave/issues/1521)), the
Maven family ([#1524](https://github.com/weaveplatform/artifactweave/issues/1524)), npm
([#1525](https://github.com/weaveplatform/artifactweave/issues/1525)), PyPI
([#1527](https://github.com/weaveplatform/artifactweave/issues/1527)), NuGet
([#1526](https://github.com/weaveplatform/artifactweave/issues/1526)), Helm
([#1523](https://github.com/weaveplatform/artifactweave/issues/1523)), Go
([#1522](https://github.com/weaveplatform/artifactweave/issues/1522)), Terraform
([#1529](https://github.com/weaveplatform/artifactweave/issues/1529)), Docker, OCI and
Helm OCI ([#1520](https://github.com/weaveplatform/artifactweave/issues/1520)), the existing
native adapters ([EPIC-ADP #1519](https://github.com/weaveplatform/artifactweave/issues/1519))
and conformance and sign-off
([EPIC-QUA #1528](https://github.com/weaveplatform/artifactweave/issues/1528)). Each family
epic amends its own row of the format table when it lands, and
[QUA-03 #1571](https://github.com/weaveplatform/artifactweave/issues/1571) marks this
decision Accepted at sign-off.

The P9c sections below describe today's Puppet, Conda, Ansible and Chef adapters, for
example Ansible's "other source failures stop resolution" and the 400 for a nested virtual
member. They hold until [EPIC-ADP](https://github.com/weaveplatform/artifactweave/issues/1519)
moves each adapter onto the engine; R1 to R11 then apply to it, R3's fall-through included.

P10's **[I]** items, grouped by ticket with the source and the inferred answer of each, are
in the comment headed "From #1530" on
[#216](https://github.com/weaveplatform/artifactweave/issues/216), for the comparison in P19.

P28 adds remaining existing-format operations, such as npm audit and private PyPI search;
P29 to P33 add format-specific virtual adapters where the target permits that class; P35
coordinates the merged metadata cache across nodes (R8). Each must prove member ordering,
upstream failure, visibility and native-client behavior. A generic file lookup alone does
not satisfy a native metadata contract.

The [capability roadmap](../../implementation/capability-roadmap.md) records the
phase boundaries under decision [0042](0042-capability-scope-and-supersession.md).
These assignments describe planned work, not current runtime support.

## Verification

- Merge unit tests per format with fixture member sets.
- Acceptance: each client resolves and publishes through a virtual over a local and a
  remote member; `artifactory_virtual_*_repository` resources for every in-scope type.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md), virtual fields
- [04-package-protocols](../04-package-protocols.md)
- <https://docs.jfrog.com/artifactory/docs/virtual-repositories>
- <https://docs.jfrog.com/artifactory/docs/deploy-artifacts#configure-virtual-repositories-for-deployment>
- <https://docs.jfrog.com/administration/docs/security-configuration#hide-existence-of-unauthorized-resources>
- Artifactory 7.161.26 sources: `VirtualRepositoriesResolver`, `VirtualRepositoryConfigResolverImpl`,
  `ArtifactoryVirtualDownloadStrategy`, `VirtualRepo`, `DownloadServiceImpl`, `UploadServiceImpl`,
  `RepoPathMover`, `DeleteMethod`, `VirtualRepoConfigChangeInterceptor`
- Decision [0032](0032-package-format-handlers.md), "Download policy rules for the native protocols"

## Puppet native delivery (P9c, #784)

The Puppet Forge v3 adapter implements the approved ordered-member behavior
for local, remote and one-node federated members. Release versions are unioned;
the first readable member wins a same-version conflict. Every advertised file
passes both virtual and member permissions and repository patterns. Module and
archive links stay relative to the requesting Forge endpoint. Each request
resolves the current membership; no merged shared response is persisted.

This delivers the Puppet-specific slice of this decision ahead of general P10.
Nested virtual composition and cross-peer federation remain separate follow-up
contracts. See [Puppet native evidence and connection recipe](../../testing/puppet.md).

## P9c Conda CEP-16 composition (2026-10-28)

Conda's opt-in shard view follows the existing ordered member resolution. It validates every source document, filters records using both member and virtual permissions, and builds new content-addressed shards from the resulting per-name records. An upstream hash never names a changed virtual representation. Shard metadata retains concrete member source-policy and revision fences; it grants no independent virtual network authority. Classic indexes remain available, and shard failures do not become successful empty member views.

The [Conda operations guide](../../operations/conda.md#cep-16-sharded-repodata) records the checked CEP/JFrog/client versions, opt-in setting, native proof, bounds and source URL constraints. [The native feature](../../../test/acceptance/features/conda.feature) requires shard requests through every approved class and fails on classic fallback. This format-specific P9c delivery does not change the remaining P10 general composition or P13 cross-peer work.

## P9c Ansible evidence (#782)

The Ansible Galaxy v3 adapter merges versions from ordered concrete L/R/F members,
filters both member and virtual permissions, retains the first readable duplicate
version and rewrites archive links to the virtual URL. Missing members and upstream
404s fall through; other source failures stop resolution. Nested virtual members
return an explicit 400 through the current MemberRequest seam; the generic P10
graph walk remains pending. The pinned native ansible-core client installs a collection and its
ranged dependency through V in the [Ansible feature](../../../test/acceptance/features/ansible.feature).

## P9c Chef evidence (#783)

Chef's Supermarket adapter unions versions from ordered concrete L/R/F members.
The first readable member wins a duplicate version. Both virtual and member
permissions and repository patterns filter metadata before response generation;
links remain under the virtual URL and downloads retain the selected member's
source authority. Missing members and upstream 404s fall through, while integrity
and transport failures stop resolution. Nested virtual members are refused by
the shared MemberRequest boundary. Current Knife installs a cookbook and its
dependency from distinct local and federated members in the
[Chef feature](../../../test/acceptance/features/chef.feature). This delivers the
Chef-specific part of this decision; general P10 and cross-peer P13 remain.
See the [connection recipe and native evidence](../../testing/chef.md).
