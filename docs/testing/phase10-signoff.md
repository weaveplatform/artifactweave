# Phase 10 qualification and epic closeout

Status: **implementation evidence recorded; final qualification pending**.

Audit date: 2026-11-18. Merged implementation baseline:
[`621523d38c1cda8b822fbb1b52a7f4b36e5f7b5d`](https://github.com/weaveplatform/artifactweave/commit/621523d38c1cda8b822fbb1b52a7f4b36e5f7b5d).
PR qualification evidence below was refreshed on 2026-11-19; the merged-child
snapshot remains tied to that baseline.
The native GitHub child lists and issue/merged-PR timelines show **47 of 49 child
tickets closed**, including the original acceptance follow-ups identified in this audit.
[HLM-03 #1713](https://github.com/weaveplatform/artifactweave/issues/1713) and
[TFM-03 #1714](https://github.com/weaveplatform/artifactweave/issues/1714)
remain open. ENG-11 #1710 closed with merged PR #1718 after successful full CI.
Epic closure remains with the maintainer.
The additional [virtual folder source-fault blocker #1723](https://github.com/weaveplatform/artifactweave/issues/1723)
is outside that 49-child count and must be resolved before final qualification.
Its correction in [PR #1726](https://github.com/weaveplatform/artifactweave/pull/1726)
has passed full CI and awaits maintainer merge.
GitHub closed [QUA-03 #1571](https://github.com/weaveplatform/artifactweave/issues/1571)
with documentation PR #1708. That records issue state; the remaining acceptance
and final combined-main gate below are still pending in the qualification epic.

This ledger records controlled implementation evidence, not a live Artifactory
comparison or a completed merged-main release gate. Decision 0008 remains
Proposed and P10 remains in qualification until the outstanding corrections and
the final gate below are complete.

## Epic delivery record

Checked child rows represent actual closed GitHub tickets. They do not stand in
for the separate acceptance criteria or the final combined-tree gate.

| Epic | Closed children | Delivery and remaining qualification |
| --- | --- | --- |
| [#1519](https://github.com/weaveplatform/artifactweave/issues/1519) Existing native virtual adapters adopt the engine (P9b/P9c types) | 8/8 | Required cross-protocol table and Bazel root correction: [#1715](https://github.com/weaveplatform/artifactweave/pull/1715). Earlier merged delivery: [#1637](https://github.com/weaveplatform/artifactweave/pull/1637), [#1647](https://github.com/weaveplatform/artifactweave/pull/1647), [#1648](https://github.com/weaveplatform/artifactweave/pull/1648), [#1649](https://github.com/weaveplatform/artifactweave/pull/1649), [#1653](https://github.com/weaveplatform/artifactweave/pull/1653), [#1655](https://github.com/weaveplatform/artifactweave/pull/1655), [#1656](https://github.com/weaveplatform/artifactweave/pull/1656), [#1657](https://github.com/weaveplatform/artifactweave/pull/1657), [#1659](https://github.com/weaveplatform/artifactweave/pull/1659) |
| [#1520](https://github.com/weaveplatform/artifactweave/issues/1520) Docker, OCI and Helm OCI virtual repositories | 4/4 | [#1669](https://github.com/weaveplatform/artifactweave/pull/1669), [#1673](https://github.com/weaveplatform/artifactweave/pull/1673), [#1677](https://github.com/weaveplatform/artifactweave/pull/1677), [#1683](https://github.com/weaveplatform/artifactweave/pull/1683) |
| [#1521](https://github.com/weaveplatform/artifactweave/issues/1521) Shared virtual engine (`internal/artifacts/core/virtual`, item routes, kit) | 11/11 | Shared traversal acceptance completed in [#1718](https://github.com/weaveplatform/artifactweave/pull/1718). Earlier merged delivery: [#1585](https://github.com/weaveplatform/artifactweave/pull/1585), [#1596](https://github.com/weaveplatform/artifactweave/pull/1596), [#1607](https://github.com/weaveplatform/artifactweave/pull/1607), [#1612](https://github.com/weaveplatform/artifactweave/pull/1612), [#1615](https://github.com/weaveplatform/artifactweave/pull/1615), [#1617](https://github.com/weaveplatform/artifactweave/pull/1617), [#1625](https://github.com/weaveplatform/artifactweave/pull/1625), [#1626](https://github.com/weaveplatform/artifactweave/pull/1626), [#1634](https://github.com/weaveplatform/artifactweave/pull/1634), [#1637](https://github.com/weaveplatform/artifactweave/pull/1637), [#1638](https://github.com/weaveplatform/artifactweave/pull/1638), [#1646](https://github.com/weaveplatform/artifactweave/pull/1646), [#1658](https://github.com/weaveplatform/artifactweave/pull/1658) |
| [#1522](https://github.com/weaveplatform/artifactweave/issues/1522) Go virtual repositories | 2/2 | [#1660](https://github.com/weaveplatform/artifactweave/pull/1660), [#1662](https://github.com/weaveplatform/artifactweave/pull/1662) |
| [#1523](https://github.com/weaveplatform/artifactweave/issues/1523) Helm virtual repositories | 2/3 | Required native install dry-run: [#1713](https://github.com/weaveplatform/artifactweave/issues/1713). Merged delivery: [#1666](https://github.com/weaveplatform/artifactweave/pull/1666), [#1667](https://github.com/weaveplatform/artifactweave/pull/1667) |
| [#1524](https://github.com/weaveplatform/artifactweave/issues/1524) Maven family virtual repositories (maven, gradle, ivy, sbt) | 5/5 | [#1661](https://github.com/weaveplatform/artifactweave/pull/1661), [#1665](https://github.com/weaveplatform/artifactweave/pull/1665), [#1668](https://github.com/weaveplatform/artifactweave/pull/1668), [#1682](https://github.com/weaveplatform/artifactweave/pull/1682), [#1688](https://github.com/weaveplatform/artifactweave/pull/1688) |
| [#1525](https://github.com/weaveplatform/artifactweave/issues/1525) npm virtual repositories | 5/5 | [#1675](https://github.com/weaveplatform/artifactweave/pull/1675), [#1679](https://github.com/weaveplatform/artifactweave/pull/1679), [#1684](https://github.com/weaveplatform/artifactweave/pull/1684), [#1690](https://github.com/weaveplatform/artifactweave/pull/1690), [#1691](https://github.com/weaveplatform/artifactweave/pull/1691) |
| [#1526](https://github.com/weaveplatform/artifactweave/issues/1526) NuGet virtual repositories | 3/3 | [#1674](https://github.com/weaveplatform/artifactweave/pull/1674), [#1676](https://github.com/weaveplatform/artifactweave/pull/1676), [#1685](https://github.com/weaveplatform/artifactweave/pull/1685) |
| [#1527](https://github.com/weaveplatform/artifactweave/issues/1527) PyPI virtual repositories | 2/2 | [#1671](https://github.com/weaveplatform/artifactweave/pull/1671), [#1672](https://github.com/weaveplatform/artifactweave/pull/1672) |
| [#1528](https://github.com/weaveplatform/artifactweave/issues/1528) Conformance, documentation and P10 sign-off | 3/3 | #1571 closed with [#1708](https://github.com/weaveplatform/artifactweave/pull/1708); final acceptance and merged-main completion record remain pending here. Earlier delivery: [#1591](https://github.com/weaveplatform/artifactweave/pull/1591), [#1699](https://github.com/weaveplatform/artifactweave/pull/1699) |
| [#1529](https://github.com/weaveplatform/artifactweave/issues/1529) Terraform virtual repositories | 2/3 | Required combined module/provider workflow and cache repeat: [#1714](https://github.com/weaveplatform/artifactweave/issues/1714). Merged delivery: [#1681](https://github.com/weaveplatform/artifactweave/pull/1681), [#1687](https://github.com/weaveplatform/artifactweave/pull/1687) |

[P10 index #1518](https://github.com/weaveplatform/artifactweave/issues/1518)
tracks these 11 epics. Epic closure remains with the maintainer after reviewing
their reconciled acceptance and evidence.

## Original acceptance audit

Closed implementation tickets and separate feature proofs do not automatically
satisfy an epic's exact client workflow. Original epic acceptance checkboxes
remain available for maintainer review; the audit preserves their requirements.

| Epic | Evidence and remaining acceptance boundary |
| --- | --- |
| ADP | The family rows below retain individual native proofs. Merged [PR #1715](https://github.com/weaveplatform/artifactweave/pull/1715) supplies #1709's guarded 23-adapter order/nesting/cycle table and corrects an independently reproduced Bazel-only slashless registry-root defect. Its 46 SQLite/PostgreSQL rows, race qualification and strict native Bazel proof supplement the existing individual client flows. |
| DKR | [Native registry scenarios](../../test/acceptance/features/registry_virtual.feature) include actual Docker remote pulls and a derived-image push/pull using cached remote layers, plus crane, ORAS and Helm OCI flows. [Catalog/tag paging tests](../../test/server/artifacts/docker) cover member union and pagination. Intentional mutation refusals remain part of the protocol contract. |
| ENG | R1–R11 anchors below cover the item/status/policy contracts. Merged [#1718](https://github.com/weaveplatform/artifactweave/pull/1718) consolidates the two core recursive traversals while retaining route-aware search, AQL, Docker and P2 behavior. The [traversal contract and characterization](../implementation/virtual-traversal.md) records both ordering/lookup contracts and allocation controls; configuration validation and serialization loops remain separate. |
| GOV | The [Go feature](../../test/acceptance/features/go.feature) resolves local and remote dependencies without a direct fallback, publishes through the virtual and builds from fresh consumer state. The merged #1707 fixture correction retains exact artifact counts for each independently collected build. |
| HLM | The merged [four URL/namespace examples](../../test/acceptance/features/helm_virtual.feature) retain signed pull, dependency and publication controls. [PR #1720](https://github.com/weaveplatform/artifactweave/pull/1720) adds repository-reference `install --dry-run=client` before cold signed pulls, using separate install caches: Darwin proof is 4 scenarios / 360 steps / zero skips, including eight installs. Its exact-source note records the pinned client's optional signed-install limitation; Linux CI and merge remain required. |
| MVN | [Merged metadata and sidecars](../../test/server/artifacts/maven/maven_virtual_metadata_test.go), [candidate selection](../../test/server/artifacts/maven/virtual_selection_test.go), [Maven deploy](../../test/acceptance/features/maven.feature), [Gradle/Ivy recipes](../../test/acceptance/features/system_recipes.feature) and [sbt publish](../../test/acceptance/features/sbt.feature) provide the named workflows. POM cleanup and its deliberate `nothing` control remain in the Maven contract. |
| NPM | The [virtual npm outline](../../test/acceptance/features/npm_virtual.feature) covers scoped/unscoped mixed dependencies, empty-cache install/`npm ci`, publication and signature audits. Vulnerability audit remains owned by P28; the no-501 requirement concerns supported P10 operations. |
| NUG | [Native V2/V3 restore and publication](../../test/acceptance/features/nuget_virtual.feature) are supplemented by [visibility/status tests](../../test/server/artifacts/nuget/virtual_v3_test.go) and [write/symbol/reindex controls](../../test/server/artifacts/nuget/virtual_write_test.go). Virtual reindex accepts the request without member work. |
| PYP | [pip/uv installation and twine/uv publication](../../test/acceptance/features/pypi_virtual.feature) retain native evidence. Merged [#1717](https://github.com/weaveplatform/artifactweave/pull/1717) repairs #1712's timestamp collision in the [server merge assertion](../../test/server/artifacts/pypi/virtual_read_test.go), retaining a fixed timestamp and exact semantic file-set/winner checks without production changes. |
| QUA | The 33-resource [provider scenario](../../deploy/artifactweave/terraform/virtual-repositories) and this 37-type ledger are distinct from package-consumer proof. Decision acceptance, P10 Done and the final post-documentation merged-main workflow remain required; the completion record belongs in #1528 even though #1571 is now closed. |
| TFM | The merged [virtual module](../../test/acceptance/features/terraform_remote.feature), [virtual provider](../../test/acceptance/features/terraform_providers.feature) and [direct-local combined registry](../../test/acceptance/features/terraform_registry.feature) proofs remain distinct. [PR #1721](https://github.com/weaveplatform/artifactweave/pull/1721) now supplies one virtual with a local module and private remote signed provider, two init/apply rounds with empty installation/plugin caches, command-local route audits and module-derived applied state. Darwin proof passes 1 scenario / 28 steps plus two retained controls / 128 steps, all zero skips; Linux CI and merge remain required. |

The Helm and Terraform findings were missing qualification artifacts; the ready
PRs above now provide reviewable local proof and await their full CI and merges.
No runtime defect was reproduced for either. #1712's fixture assertion defect was repaired
separately by merged #1717, outside the epic child-ticket count above.

## Engine rules R1–R11

The source contract is [decision 0008](../research/decisions/0008-virtual-repositories.md).
The following are executable test anchors in the merged tree, not inferred
results from repository creation alone.

| Rule | Controlled evidence | Delivery |
| --- | --- | --- |
| R1 effective order | [`TestAccVirtualServesLocalBeforeRemoteCache`](../../test/server/artifacts/virtual_order_test.go) | [#1252](https://github.com/weaveplatform/artifactweave/issues/1252) |
| R2 nesting, cycles, duplicates | [`TestAccVirtualNestingAndCycles`](../../test/server/artifacts/virtual_order_test.go) | [#1252](https://github.com/weaveplatform/artifactweave/issues/1252) |
| R3 fall-through and final status | [`TestAccVirtualMissAndRefusalStatuses`](../../test/server/artifacts/virtual_status_test.go) | [#1533](https://github.com/weaveplatform/artifactweave/issues/1533) |
| R4 hiding | [`TestAccVirtualHidesUnauthorizedResources`](../../test/server/artifacts/virtual_status_test.go) | [#1533](https://github.com/weaveplatform/artifactweave/issues/1533) |
| R5 virtual and member patterns | [`TestAccVirtualNestedPatterns`](../../test/server/artifacts/virtual_order_test.go) | [#1252](https://github.com/weaveplatform/artifactweave/issues/1252) |
| R6 DDR and mutations | [`TestAccDeployThroughVirtual`](../../test/server/artifacts/virtual_write_test.go) | [#1534](https://github.com/weaveplatform/artifactweave/issues/1534) |
| R7 originated requests | [`TestAccVirtualOriginRestriction`](../../test/server/artifacts/virtual_remote_test.go) | [#1542](https://github.com/weaveplatform/artifactweave/issues/1542) |
| R8 merged cache identity/freshness | [`TestUnitCacheKeys`](../../internal/artifacts/core/virtual/cache_test.go) | [#1543](https://github.com/weaveplatform/artifactweave/issues/1543) |
| R9 storage and browsing | [`TestAccVirtualStorageItemInfo`](../../test/server/artifacts/virtual_storage_test.go) | [#1537](https://github.com/weaveplatform/artifactweave/issues/1537) |
| R10 remote member policy | [`TestAccVirtualFetchesRemoteMembersUpstreams`](../../test/server/artifacts/virtual_remote_test.go) | [#1535](https://github.com/weaveplatform/artifactweave/issues/1535) |
| R11 package membership | [`TestAccVirtualMemberPackageTypes`](../../test/server/artifacts/virtual_members_test.go) | [#1532](https://github.com/weaveplatform/artifactweave/issues/1532) |

R6 also covers multipart creation, checksum deployment, DELETE and transfer
refusals in the same [write suite](../../test/server/artifacts/virtual_write_test.go).
R8 expiry, source/visibility keys, stale fallback, invalidation, cancellation and
single flight have separate cases in the [cache suite](../../internal/artifacts/core/virtual/cache_test.go).
The [save suite](../../test/server/artifacts/virtual_config_test.go) supplies R11's
configuration statuses. Native handler exceptions remain in the family contracts.

## Family, client and provider evidence

The 14 original families and 23 adopted types have separate rows below. Client
versions come from the linked maintained recipes and their `version_sources`;
bundled clients are identified by the immutable image or the runtime version
recorded in CI. Docker CLI/daemon are supplied by the runner; the accompanying
crane client is pinned. A `client` reference executes that client. A `server`
reference is supplemental protocol evidence and does not replace a native run.

The [provider scenario](../../deploy/artifactweave/terraform/virtual-repositories)
uses `jfrog/artifactory` 12.11.14 to apply, update, obtain an empty plan and destroy
33 virtual resources. It proves configuration, not package retrieval. The four
types without a provider resource retain native evidence and are named explicitly.

| Type | Pinned client or recorded runtime | Local + remote virtual evidence | Resolution / merged cache | Provider resource |
| --- | --- | --- | --- | --- |
| `docker` | [Docker CLI/daemon supplied by the CI runner; crane 0.22.1](../client-recipes/docker.md) | [client: crane pulls local and remote images by tag and digest through one virtual](../../test/acceptance/features/registry_virtual.feature); [client: crane pushes a derived image through the virtual using cached remote layers](../../test/acceptance/features/registry_virtual.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_docker_repository` |
| `generic` | [JFrog CLI 2.131.0](../client-recipes/generic.md) | [client: curl and JFrog CLI fetch a cold file through a virtual and reuse the remote's cache through an outage](../../test/acceptance/features/generic_remote.feature); [client: jf rt u through a virtual repository](../../test/acceptance/features/artifacts.feature) | item routes; no R8 merged-document cache | `artifactory_virtual_generic_repository` |
| `go` | [Go 1.27 (module toolchain); JFrog CLI 2.131.0](../client-recipes/go.md) | [client: A cold Go build resolves local and private remote modules through one virtual](../../test/acceptance/features/go.feature); [client: Publish a Go build through a virtual and resolve mixed dependencies through the same virtual](../../test/acceptance/features/go.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_go_repository` |
| `gradle` | [Gradle 9.8.2 on Temurin 27; optional JFrog CLI 2.131.0 wrapper](../client-recipes/gradle.md) | [client: Gradle resolves the exact bytes from a <class> repository](../../test/acceptance/features/system_recipes.feature); [client: Gradle publishes a module into a <class> repository](../../test/acceptance/features/system_recipes.feature) | item routes; no R8 merged-document cache | `artifactory_virtual_gradle_repository` |
| `helm` | [Helm 4.3.1](../client-recipes/helm.md) | [client: charts, member selection and publishing resolve with relative URLs <relative> and namespaces <namespaces>](../../test/acceptance/features/helm_virtual.feature) | native merge; merged index | `artifactory_virtual_helm_repository` |
| `helmoci` | [Helm 4.3.1; JFrog CLI 2.131.0](../client-recipes/helmoci.md) | [client: Helm pulls local and remote chart versions through a Helm OCI virtual](../../test/acceptance/features/registry_virtual.feature); [client: Helm pushes and pulls a chart through a Helm OCI virtual DDR](../../test/acceptance/features/registry_virtual.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_helmoci_repository` |
| `ivy` | [Apache Ivy 2.6.0 on the pinned Maven/Temurin image](../client-recipes/ivy.md) | [client: Ivy publishes into a mixed virtual deployment target and resolves with a fresh cache](../../test/acceptance/features/system_recipes.feature) | item routes; no R8 merged-document cache | `artifactory_virtual_ivy_repository` |
| `maven` | [Maven 3 image / Temurin 27; compatibility Maven 3.9 image / Temurin 26 (exact bundled versions in CI logs)](../client-recipes/maven.md) | [client: <maven> deploys release and snapshots through a mixed virtual and resolves merged versions without repository escape](../../test/acceptance/features/maven.feature); [client: Gradle uses an Ivy artifact pattern to resolve a Maven publication through a different virtual layout](../../test/acceptance/features/system_recipes.feature) | item routes; no R8 merged-document cache | `artifactory_virtual_maven_repository` |
| `npm` | [Bundled npm on Node 26.13.1; npm 12.2.1 on Node 24.22.3](../client-recipes/npm.md) | [client: <npm> installs a cold mixed dependency tree and verifies its virtual lockfile with npm ci](../../test/acceptance/features/npm_virtual.feature); [client: <npm> logs in, publishes, tags, deprecates and unpublishes through one virtual deployment target](../../test/acceptance/features/npm_virtual.feature); [client: <npm> audits both signing members and reports an unsigned virtual member](../../test/acceptance/features/npm_virtual.feature); [client: <npm> installs allowed tarball and GitHub dependencies only through the virtual cache remote](../../test/acceptance/features/npm_virtual.feature); [client: <npm> searches live local and cached remote members through a nested virtual](../../test/acceptance/features/npm_virtual.feature) | native merge; packuments/search parts; keys per request | `artifactory_virtual_npm_repository` |
| `nuget` | [NuGet bundled with .NET SDK 10.0 image; .NET 11.0 image also exercised (exact SDK versions in CI logs)](../client-recipes/nuget.md) | [client: <dotnet> restores a remote parent and its local transitive dependency](../../test/acceptance/features/nuget_virtual.feature); [client: <dotnet> restores local and private remote packages through a virtual v2 feed](../../test/acceptance/features/nuget_virtual.feature); [client: <dotnet> publishes package and symbols through a forced virtual and restores from empty caches](../../test/acceptance/features/nuget_virtual.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_nuget_repository` |
| `oci` | [ORAS 1.3.6; crane 0.22.1; Cosign 3.1.4](../client-recipes/oci.md) | [client: oras pulls both OCI members through a virtual repository](../../test/acceptance/features/registry_virtual.feature); [client: oras pushes and pulls an artifact through an OCI virtual DDR](../../test/acceptance/features/registry_virtual.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_oci_repository` |
| `pypi` | [Python 3.15.2; pip 26.3; Twine 7.0.0; uv 0.13.7; Poetry 2.5.1](../client-recipes/pypi.md) | [client: pip and uv install wheel and sdist dependencies from virtual HTML and JSON indexes](../../test/acceptance/features/pypi_virtual.feature); [client: twine and uv publish a wheel and sdist to a virtual deployment target and install back](../../test/acceptance/features/pypi_virtual.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_pypi_repository` |
| `sbt` | [sbt 2.0.12; Eclipse Temurin 26](../client-recipes/sbt.md) | [client: sbt resolves mixed virtual origins, publishes into its deployment target and uses empty client caches](../../test/acceptance/features/sbt.feature) | item routes; no R8 merged-document cache | `artifactory_virtual_sbt_repository` |
| `terraform` | [Terraform 1.17.1; JFrog CLI 2.131.0](../client-recipes/terraform.md) | [client: a fresh init resolves rewritten HCL and JSON registry dependencies through one virtual](../../test/acceptance/features/terraform_remote.feature); [client: fresh init installs hosted and private remote providers through a nested virtual <mode>](../../test/acceptance/features/terraform_providers.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_terraform_repository` |
| `alpine` | [apk-tools bundled with Alpine 3.24 (immutable image)](../client-recipes/alpine.md) | [client: apk installs a local and a remote member through a signed virtual repository offline](../../test/acceptance/features/alpine.feature) | native merge; merged index | `artifactory_virtual_alpine_repository` |
| `ansible` | [ansible-core 2.22.0 on Python 3.15.2](../client-recipes/ansible.md) | [client: Galaxy publishes through a virtual to its deployment repository and installs over a remote and a nested virtual](../../test/acceptance/features/ansible.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_ansible_repository` |
| `bazelmodules` | [Bazel from the pinned linux/amd64 image (runtime version recorded by native CI)](../client-recipes/bazelmodules.md) | [client: TestAccBazelClient](../../internal/artifacts/ecosystem/bazelmodules/http/bazelmodules_test.go) | native merge; no R8 merged-document cache | **no provider resource** |
| `bower` | [Bower 1.8.14; bower-art-resolver 2.0.10; Node 26.13.1](../client-recipes/bower.md) | [client: virtual dependency rewrite and art shorthand stay cached with fresh client state](../../test/acceptance/features/bower.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_bower_repository` |
| `chef` | [Chef 19.4.46, Knife 19.0.134, knife-art 1.0.2 (Ruby 4.0.9)](../client-recipes/chef.md) | [client: Knife resolves cookbooks through a virtual over a remote and a nested virtual](../../test/acceptance/features/chef.feature) | native merge; cookbook/version documents | `artifactory_virtual_chef_repository` |
| `cocoapods` | [CocoaPods 1.17.0; Ruby 4.0.9](../client-recipes/cocoapods.md) | [client: install pods through a virtual repository over a remote member and a nested local member](../../test/acceptance/features/cocoapods.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_cocoapods_repository` |
| `composer` | [Composer 2.11.1; Composer 1.10.28 local compatibility](../client-recipes/composer.md) | [client: Composer 2 installs a version only a later virtual member holds](../../test/acceptance/features/composer.feature) | native merge; merged p2 | `artifactory_virtual_composer_repository` |
| `conan` | [Conan 2.34.0 primary; Conan 1.66.0 legacy](../client-recipes/conan.md) | [client: TestAccConanRemoteClient](../../test/server/artifacts/conan/conan_client_remote_test.go); [client: TestAccConanClients](../../test/server/artifacts/conan/conan_client_test.go) | native merge; revision/search/package documents | `artifactory_virtual_conan_repository` |
| `conda` | [micromamba 2.10.0 (Conda package protocol)](../client-recipes/conda.md) | [client: micromamba installs through a virtual channel over a remote and a nested virtual](../../test/acceptance/features/conda.feature) | native merge; remote subdir parts | `artifactory_virtual_conda_repository` |
| `cran` | [R 4.6.2](../client-recipes/cran.md) | [client: R installs from a remote member through a nested CRAN virtual](../../test/acceptance/features/cran.feature) | native merge; merged index | `artifactory_virtual_cran_repository` |
| `debian` | [APT bundled with Ubuntu 26.10 (immutable image)](../client-recipes/debian.md) | [client: apt installs a remote member through a signed virtual repository offline](../../test/acceptance/features/debian.feature) | native merge; merged metadata | `artifactory_virtual_debian_repository` |
| `gems` | [RubyGems 4.0.23 and Bundler 4.0.23 on Ruby 4.0.9](../client-recipes/gems.md) | [client: gem push and install through a virtual repository over local and remote members](../../test/acceptance/features/gems.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_gems_repository` |
| `gitlfs` | [Git LFS 3.8.1](../client-recipes/gitlfs.md) | [client: the pinned client pulls through a virtual repository over a local and a remote member](../../test/acceptance/features/gitlfs.feature); [client: the pinned client publishes through a virtual deploy target](../../test/acceptance/features/gitlfs.feature) | first member wins; no R8 merged-document cache | `artifactory_virtual_gitlfs_repository` |
| `hex` | [Hex 2.6.0; Elixir 1.20.5; OTP 29.1.2](../client-recipes/hex.md) | [client: Virtual repository falls through to a signed remote member](../../test/acceptance/features/hex.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_hex_repository` |
| `huggingfaceml` | [huggingface_hub 2.4.0; hf-xet 1.8.0](../client-recipes/huggingfaceml.md) | [client: Hub client resolves through a virtual member](../../test/acceptance/features/huggingface_xet.feature); [client: Hub client publishes through <class>](../../test/acceptance/features/huggingface.feature) | first member wins; no R8 merged-document cache | **no provider resource** |
| `luarocks` | [LuaRocks 3.13.0; Lua 5.5.1](../client-recipes/luarocks.md) | [client: virtual publication and mixed install with offline cached member](../../test/acceptance/features/luarocks_virtual.feature) | native merge; no R8 merged-document cache | **no provider resource** |
| `nix` | [Nix 2.35.6](../client-recipes/nix.md) | [client: Nix verifies a remote cache and substitutes with the upstream offline](../../test/acceptance/features/nix.feature) | first member wins; no R8 merged-document cache | `artifactory_virtual_nix_repository` |
| `opkg` | [opkg 0.10.0](../client-recipes/opkg.md) | [client: opkg installs from a local and a remote member of a virtual feed](../../test/acceptance/features/opkg.feature) | native merge; no R8 merged-document cache | **no provider resource** |
| `p2` | [Eclipse Platform / Equinox P2 4.41, Java 21](../client-recipes/p2.md) | [client: a virtual P2 repository keeps external remote and local Generic members ordered](../../test/acceptance/features/p2.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_p2_repository` |
| `pub` | [Dart/pub 3.14.0](../client-recipes/pub.md) | [client: virtual repository resolves ordered local and remote members](../../test/acceptance/features/pub.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_pub_repository` |
| `puppet` | [Puppet 8.10.0 on Ruby 3.3.13](../client-recipes/puppet.md) | [client: Puppet resolves modules through a virtual over a remote and a nested virtual](../../test/acceptance/features/puppet.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_puppet_repository` |
| `rpm` | [DNF and RPM bundled with Fedora 45 (immutable image)](../client-recipes/rpm.md) | [client: dnf installs through a virtual RPM repository backed by a remote and a local member](../../test/acceptance/features/rpm.feature) | native merge; merged metadata | `artifactory_virtual_rpm_repository` |
| `swift` | [Swift/SwiftPM 6.4.2 pinned Linux client](../client-recipes/swift.md) | [client: TestAccSwiftPMVirtualLocalRemoteAndPublish](../../internal/artifacts/ecosystem/swift/http/remote_test.go); [server: a Swift virtual repository resolves a release through a nested virtual member](../../test/acceptance/features/swift.feature) | native merge; no R8 merged-document cache | `artifactory_virtual_swift_repository` |

### Publication evidence

The [operations guide R6](../operations/virtual-repositories.md#r6-deployment-deletion-and-transfers)
distinguishes native publication from generic item deployment. Native DDR proofs
include [Maven](../../test/acceptance/features/maven.feature),
[Gradle/Ivy](../../test/acceptance/features/system_recipes.feature),
[sbt](../../test/acceptance/features/sbt.feature),
[npm](../../test/acceptance/features/npm_virtual.feature),
[PyPI](../../test/acceptance/features/pypi_virtual.feature),
[NuGet](../../test/acceptance/features/nuget_virtual.feature),
[Go](../../test/acceptance/features/go.feature),
[Docker/OCI/Helm OCI](../../test/acceptance/features/registry_virtual.feature),
[RubyGems](../../test/acceptance/features/gems.feature),
[LuaRocks](../../test/acceptance/features/luarocks_virtual.feature),
[Ansible](../../test/acceptance/features/ansible.feature),
[Git LFS](../../test/acceptance/features/gitlfs.feature) and
[Swift](../../internal/artifacts/ecosystem/swift/http/remote_test.go).
The [Conan client suite](../../test/server/artifacts/conan/conan_client_test.go)
and [Hub feature](../../test/acceptance/features/huggingface.feature) retain their
own publication contracts. Classic Helm publishes through the item DDR. A generic
PUT does not establish an additional native publishing API for a resolve-only client.

## Security, cache and lifecycle review

| Boundary | Evidence and retained limit |
| --- | --- |
| Caller Read, hiding and actual member policy | [Status/principal suite](../../test/server/artifacts/virtual_status_test.go), [protocol policy suite](../../test/server/artifacts/protocol_download_policy_test.go), and R4/R5. Direct-addressed Read-first behavior remains DV-057; a silent virtual member checks policy before member Read. |
| Cache visibility and source authority | [Cache key/expiry/fallback suite](../../internal/artifacts/core/virtual/cache_test.go), [upstream current membership](../../test/server/artifacts/searches_upstream_routes_test.go), and each native family's warm/cold controls. Credentials, source/repository incarnation, path epoch and publication revision stay fenced. |
| DDR writes | [Deploy/multipart/delete/transfer suite](../../test/server/artifacts/virtual_write_test.go) plus the native publication proofs above. A virtual owns no ordinary uploaded artifact. |
| Source SQL versus member content faults | [Merged #1695](https://github.com/weaveplatform/artifactweave/pull/1695), [#1700](https://github.com/weaveplatform/artifactweave/pull/1700), and the [phase/source audit](../research/virtual-metadata-faults-source-audit.md). Lookup/authority SQL faults abort with sanitized 500; selected member-content failure retains the documented tolerance. Open corrections below are not treated as merged. |
| Lifecycle and browsing | [Stored views](../../test/server/artifacts/virtual_storage_test.go), [native fixture lifecycle](../operations/virtual-repositories.md#family-evidence), merged-index invalidation and the family reindex/delete/restore contracts. Stored views do not imply cold upstream listing support. |

## Deviations and inferred answers

Compared with pre-P10 main `f79170f6e`, P10 adds **DV-202** (fixed browser and
content-disposition system-property defaults), amends **DV-057** with the virtual
member policy-first exception, and corrects **DV-153**'s outdated Docker listing
implementation wording. No DV entry was removed. The [deviation register](../implementation/deviations.md)
retains the reasons and compatibility impact; this ledger creates no new waiver.

The [P10 inference register](../operations/virtual-repositories.md#inference-register)
links the actual existing #216 comments by rule and family. The
[published closeout update](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992)
registers policy-path, lookup/content, validation-clock and origin-miss questions,
and corrects the older cached-as-Chef Ansible proposal to its implemented
per-request merge. These remain comparison questions, not vendor observations.
Runtime-fault classifications retain their primary-source and modern-version
limits in their owning research; other unposted drafts are not treated as comments.

## Qualification status and corrective work

| Item | Current disposition |
| --- | --- |
| [#1709 / PR #1715](https://github.com/weaveplatform/artifactweave/pull/1715) | Merged as `1db9b298b` after [full exact-head CI 44383455350](https://github.com/weaveplatform/artifactweave/actions/runs/44383455350) passed all 19 required jobs and both coverage thresholds. Local proof has 46 SQLite/PostgreSQL rows, race checks and five strict native Bazel subtests with zero skips. |
| [#1710 / PR #1718](https://github.com/weaveplatform/artifactweave/pull/1718) | Merged as `de718166bfc93952ad744d3a44ff6d22089da2bc` after [full CI 44392699224](https://github.com/weaveplatform/artifactweave/actions/runs/44392699224) passed all 20 required jobs, native clients and both coverage thresholds at `3871a8139d047c015e9d573179dfdd34355bd68f`. One shared traversal preserves effective-member and route-aware contracts. Final combined-main qualification remains required. |
| [#1713 / PR #1720](https://github.com/weaveplatform/artifactweave/pull/1720) | Full [CI 44399073797](https://github.com/weaveplatform/artifactweave/actions/runs/44399073797) passed all 20 required jobs, actual Linux native clients and both coverage thresholds at `bcd0d988320d06c1020b8d836f7f43985b5b8717`. Explicit 4 scenarios / 360 steps / eight installs / zero skips remain qualified to the separate Darwin run; non-verbose Linux success does not print those totals. Maintainer merge remains required. |
| [#1714 / PR #1721](https://github.com/weaveplatform/artifactweave/pull/1721) | Full [CI 44401261445](https://github.com/weaveplatform/artifactweave/actions/runs/44401261445) passed all 20 required jobs, actual Linux native clients and both coverage thresholds at `bf14f376f7289a5b942795803f36ed20eb6eda69`. Separate Darwin runs passed 1/28 and 2/128 scenarios/steps with zero skips; Linux package success is separate from those explicit counts. Maintainer merge remains required. |
| [#1645 / PR #1705](https://github.com/weaveplatform/artifactweave/pull/1705) | Merged as `6c55dff0e` after [full exact-head CI 44378840058](https://github.com/weaveplatform/artifactweave/actions/runs/44378840058) passed all 19 required jobs, including native clients and both coverage thresholds. The corrected file/API-document/content-phase boundary is delivered. The preceding superseded run and original red evidence remain separately qualified. |
| [#1704 / PR #1711](https://github.com/weaveplatform/artifactweave/pull/1711) | Corrected head `f0af0fc11d74ee419fbe7ecfb24db1cecdfb073a` normally integrates main `901a11a5c`; [full CI 44399136390](https://github.com/weaveplatform/artifactweave/actions/runs/44399136390) passed all 20 required jobs, native partitions and both coverage thresholds. Fresh local normal/race commands each pass 96 clock-fault rows plus four retention controls on SQLite/PostgreSQL. The [old failed run](https://github.com/weaveplatform/artifactweave/actions/runs/44373109714/job/135045401094) remains historical evidence of the separate timestamp assertion repaired by merged #1717. Maintainer merge remains required. |
| [#1712 / PR #1717](https://github.com/weaveplatform/artifactweave/pull/1717) | Test-only repair merged as `901a11a5c` after [exact-head format/PyPI CI 44390144833](https://github.com/weaveplatform/artifactweave/actions/runs/44390144833) passed all nine selected required jobs. It is included in #1711's corrected successful full gate. |
| [#1706 / PR #1707](https://github.com/weaveplatform/artifactweave/pull/1707) | Merged as `fb67c046` after [full exact-head CI 44361946001](https://github.com/weaveplatform/artifactweave/actions/runs/44361946001) passed all 19 required jobs. Both independently collected Go builds still require exactly three artifacts; no registry behavior changed. |
| [#1693 / PR #1698](https://github.com/weaveplatform/artifactweave/pull/1698) | Merged as `544e9acad` after [full exact-head CI 44377565202](https://github.com/weaveplatform/artifactweave/actions/runs/44377565202) passed all 20 required jobs. The dedicated Windows lane passes all five driver cases, including literal absolute/relative filenames; it does not establish UNC-share or Windows application-runtime evidence. Both coverage thresholds passed. |
| [#1571 / PR #1708](https://github.com/weaveplatform/artifactweave/pull/1708) | Documentation merged as `df78dd391` after exact-head documentation checks passed. GitHub closed #1571 with that merge. The two original acceptance follow-ups and qualified clock correction await maintainer merges. Resolve #1723, accept the decision and qualify the final combined main before recording completion in #1528. |
| [#1636 / PR #1724](https://github.com/weaveplatform/artifactweave/pull/1724) | Separate trace follow-up outside the 49-child count. Full [CI 44419496608](https://github.com/weaveplatform/artifactweave/actions/runs/44419496608) passed all 20 required jobs, actual Linux native clients and both coverage thresholds at `55df7f0a685cd72deab208c898a35640d93e0cb1`. Stored-file query-property evaluations now add the source-established trace step without changing matching or download answers. Maintainer merge remains pending. |
| [#1640 / PR #1725](https://github.com/weaveplatform/artifactweave/pull/1725) | Separate direct-folder follow-up outside the 49-child count. Full [CI 44425983909](https://github.com/weaveplatform/artifactweave/actions/runs/44425983909) passed all 20 required jobs, actual Linux native clients and both coverage thresholds at `739aac6179857b3be82b2cf934dd50b8a9260156`. The direct index combines cached/upstream children with cache-name/type precedence and final descendant checks. Explicit 4 scenarios / 102 steps / zero skips belong to the separate Darwin proof. Maintainer merge remains pending. |
| [#1723 / PR #1726](https://github.com/weaveplatform/artifactweave/pull/1726) | Additional final-qualification blocker outside the 49-child count. Full [CI 44430053878](https://github.com/weaveplatform/artifactweave/actions/runs/44430053878) passed all 20 required jobs, actual Linux native clients and both coverage thresholds at `d19d9787c0f62fbfd812cc84f79173f4bf19aeb4`. Repeated Generic/Maven SQLite/PostgreSQL source faults now abort the virtual index with sanitized 500; final authority failures also abort, while ordinary initial upstream fallback remains. The focused local Linux proof passed 6 scenarios / 218 steps / zero skips. Source-era HTML mapping and the observed project authority contract remain distinct. Maintainer merge is still required before final qualification. |


The successful [RPM correction gate](https://github.com/weaveplatform/artifactweave/actions/runs/44355578274)
at `c7f8dd05dbb64a830f1e32e26b56c1980c87271f` passed all 19 required jobs
and both repository coverage thresholds: **95.4% (91,263/95,687)**. Its strict
native-client selection includes RPM/DNF. The user merged that correction as
`cbed189bceac60069fd037d9de56235e05877e68`. This is useful integrated PR evidence;
it is **not** the post-signoff merged-main run required for the final completion
record in #1528.

Remaining final qualification steps:

1. Resolve #1723 and merge the outstanding acceptance/corrective PRs only after
   successful latest-head CI. Its source-fault suppression is a separate blocker
   even after all 49 original children have closed.
2. Reconcile every evidence row and inference/deviation limit against that main.
3. Accept decision 0008 and mark P10 Done only when its acceptance conditions are met.
4. After the documentation PR merges, dispatch the full quality workflow on the
   actual merged main SHA. Record every required job, merged coverage and native
   outcome in [EPIC-QUA #1528](https://github.com/weaveplatform/artifactweave/issues/1528).
   Missing pinned clients or a skipped required native test fail this qualification.
5. Leave epic closure to the maintainer; an earlier PR gate cannot qualify a later head.

## Receiving phases

| Owner | Boundary retained after P10 |
| --- | --- |
| P11 | Project-source sharing and related project authorization remain separate from virtual membership. |
| P12 security configuration | Delivered by [#1903](https://github.com/weaveplatform/artifactweave/issues/1903): `security.hideUnauthorizedResources` applies through PATCH, `config apply` and the bootstrap descriptor, is exported and reported by `GET /api/securityconfig`, and hides every download refusal at run time (`TestAccGlobalHideUnauthorizedResources`); per-virtual hiding retains R4's tests unchanged. |
| P13 | Cross-peer federation, convergence and peer recovery; this ledger proves one-node federated members only. |
| P19 / #216 | Choose a live Artifactory build and compare the recorded inferred answers; no trial or live capture was performed for this work. |
| P28 / named format owners | Remaining protocol/client compatibility beyond the explicitly linked P10 flows. |
| P35 | Multi-node merged-cache coordination and distributed invalidation; current caches are bounded per process. |
