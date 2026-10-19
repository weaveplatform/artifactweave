# 0008: Virtual repositories

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 25 section 11](../25-npm.md#11-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)).

## Context

A virtual repository aggregates local, remote and other virtual repositories under one
URL. Clients resolve through it and, for most formats, publish through it. Artifactory
resolves members in order, writes to the `defaultDeploymentRepo`, merges per-format
metadata across members and caches the merged result for
`virtualRetrievalCachePeriodSecs`.

## Decision

`internal/service/repo` resolves a virtual repository by walking `repositories` in order
and returning the first member that has the item; nested virtuals are flattened with cycle
detection. Writes go to `defaultDeploymentRepo` and fail with Artifactory's error when it
is unset. `artifactoryRequestsCanRetrieveRemoteArtifacts` controls whether requests that
originate from another repository on the same instance may reach remote members.

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

Merged metadata is cached per virtual for `virtualRetrievalCachePeriodSecs` and
invalidated on writes to any member.

## Rationale

Order-based resolution and per-format merging are what clients observe; there is no
simpler model that keeps `npm install` through a virtual over a local and a remote
behaving as it does today.

**Alternatives considered:**
- *Resolving members in parallel and taking the first response*: faster, but changes
  which member wins and breaks `priorityResolution` semantics.

## Artifactory 7.x check

The decision stands against 7.161.32 as a plan for P10. The 7.x notes add inputs for P10:
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

- A virtual repository may have no members. Each member must have the virtual repository's
  package type unless the virtual repository is generic; Maven, Gradle, Ivy, SBT and P2
  count as one type **[I]**. Every write path refuses a mismatch; Artifactory's create API
  does from 7.133.3 (RTDEV-62756; [research 03](../03-repository-configuration-model.md),
  [#394](https://github.com/weaveplatform/artifactweave/issues/394)).
- Merging is bounded by the metadata cache; a write to a member is visible through the
  virtual after invalidation, not instantly for in-flight merges.

## Verification

- Merge unit tests per format with fixture member sets.
- Acceptance: each client resolves and publishes through a virtual over a local and a
  remote member; `artifactory_virtual_*_repository` resources for every in-scope type.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md), virtual fields
- [04-package-protocols](../04-package-protocols.md)
- <https://docs.jfrog.com/artifactory/docs/virtual-repositories>
