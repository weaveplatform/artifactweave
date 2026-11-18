# Phase 10 qualification and epic closeout

Status: **implementation evidence recorded; final qualification pending**.

Audit date: 2026-11-18. Merged implementation baseline:
[`cbed189bceac60069fd037d9de56235e05877e68`](https://github.com/weaveplatform/artifactweave/commit/cbed189bceac60069fd037d9de56235e05877e68).
The native GitHub child lists and issue/merged-PR timelines show **44 of 47 child
tickets closed**, including the qualification follow-up identified in this audit.
[QUA-03 #1571](https://github.com/weaveplatform/artifactweave/issues/1571) and
[ADP-08 #1709](https://github.com/weaveplatform/artifactweave/issues/1709) and
[ENG-11 #1710](https://github.com/weaveplatform/artifactweave/issues/1710)
remain open. Epic closure remains with the maintainer.

This ledger records controlled implementation evidence, not a live Artifactory
comparison or a completed merged-main release gate. Decision 0008 remains
Proposed and P10 remains in qualification until the outstanding corrections and
the final gate below are complete.

## Epic delivery record

Checked child rows represent actual closed GitHub tickets. They do not stand in
for the separate acceptance criteria or the final combined-tree gate.

| Epic | Closed children | Delivery and remaining qualification |
| --- | --- | --- |
| [#1519](https://github.com/weaveplatform/artifactweave/issues/1519) Existing native virtual adapters adopt the engine (P9b/P9c types) | 7/8 | Required cross-protocol qualification table: [#1709](https://github.com/weaveplatform/artifactweave/issues/1709). Merged delivery: [#1637](https://github.com/weaveplatform/artifactweave/pull/1637), [#1647](https://github.com/weaveplatform/artifactweave/pull/1647), [#1648](https://github.com/weaveplatform/artifactweave/pull/1648), [#1649](https://github.com/weaveplatform/artifactweave/pull/1649), [#1653](https://github.com/weaveplatform/artifactweave/pull/1653), [#1655](https://github.com/weaveplatform/artifactweave/pull/1655), [#1656](https://github.com/weaveplatform/artifactweave/pull/1656), [#1657](https://github.com/weaveplatform/artifactweave/pull/1657), [#1659](https://github.com/weaveplatform/artifactweave/pull/1659) |
| [#1520](https://github.com/weaveplatform/artifactweave/issues/1520) Docker, OCI and Helm OCI virtual repositories | 4/4 | [#1669](https://github.com/weaveplatform/artifactweave/pull/1669), [#1673](https://github.com/weaveplatform/artifactweave/pull/1673), [#1677](https://github.com/weaveplatform/artifactweave/pull/1677), [#1683](https://github.com/weaveplatform/artifactweave/pull/1683) |
| [#1521](https://github.com/weaveplatform/artifactweave/issues/1521) Shared virtual engine (`internal/artifacts/core/virtual`, item routes, kit) | 10/11 | Shared traversal acceptance: [#1710](https://github.com/weaveplatform/artifactweave/issues/1710). Merged delivery: [#1585](https://github.com/weaveplatform/artifactweave/pull/1585), [#1596](https://github.com/weaveplatform/artifactweave/pull/1596), [#1607](https://github.com/weaveplatform/artifactweave/pull/1607), [#1612](https://github.com/weaveplatform/artifactweave/pull/1612), [#1615](https://github.com/weaveplatform/artifactweave/pull/1615), [#1617](https://github.com/weaveplatform/artifactweave/pull/1617), [#1625](https://github.com/weaveplatform/artifactweave/pull/1625), [#1626](https://github.com/weaveplatform/artifactweave/pull/1626), [#1634](https://github.com/weaveplatform/artifactweave/pull/1634), [#1637](https://github.com/weaveplatform/artifactweave/pull/1637), [#1638](https://github.com/weaveplatform/artifactweave/pull/1638), [#1646](https://github.com/weaveplatform/artifactweave/pull/1646), [#1658](https://github.com/weaveplatform/artifactweave/pull/1658) |
| [#1522](https://github.com/weaveplatform/artifactweave/issues/1522) Go virtual repositories | 2/2 | [#1660](https://github.com/weaveplatform/artifactweave/pull/1660), [#1662](https://github.com/weaveplatform/artifactweave/pull/1662) |
| [#1523](https://github.com/weaveplatform/artifactweave/issues/1523) Helm virtual repositories | 2/2 | [#1666](https://github.com/weaveplatform/artifactweave/pull/1666), [#1667](https://github.com/weaveplatform/artifactweave/pull/1667) |
| [#1524](https://github.com/weaveplatform/artifactweave/issues/1524) Maven family virtual repositories (maven, gradle, ivy, sbt) | 5/5 | [#1661](https://github.com/weaveplatform/artifactweave/pull/1661), [#1665](https://github.com/weaveplatform/artifactweave/pull/1665), [#1668](https://github.com/weaveplatform/artifactweave/pull/1668), [#1682](https://github.com/weaveplatform/artifactweave/pull/1682), [#1688](https://github.com/weaveplatform/artifactweave/pull/1688) |
| [#1525](https://github.com/weaveplatform/artifactweave/issues/1525) npm virtual repositories | 5/5 | [#1675](https://github.com/weaveplatform/artifactweave/pull/1675), [#1679](https://github.com/weaveplatform/artifactweave/pull/1679), [#1684](https://github.com/weaveplatform/artifactweave/pull/1684), [#1690](https://github.com/weaveplatform/artifactweave/pull/1690), [#1691](https://github.com/weaveplatform/artifactweave/pull/1691) |
| [#1526](https://github.com/weaveplatform/artifactweave/issues/1526) NuGet virtual repositories | 3/3 | [#1674](https://github.com/weaveplatform/artifactweave/pull/1674), [#1676](https://github.com/weaveplatform/artifactweave/pull/1676), [#1685](https://github.com/weaveplatform/artifactweave/pull/1685) |
| [#1527](https://github.com/weaveplatform/artifactweave/issues/1527) PyPI virtual repositories | 2/2 | [#1671](https://github.com/weaveplatform/artifactweave/pull/1671), [#1672](https://github.com/weaveplatform/artifactweave/pull/1672) |
| [#1528](https://github.com/weaveplatform/artifactweave/issues/1528) Conformance, documentation and P10 sign-off | 2/3 | Final #1571 record and merged-main gate outstanding. [#1591](https://github.com/weaveplatform/artifactweave/pull/1591), [#1699](https://github.com/weaveplatform/artifactweave/pull/1699) |
| [#1529](https://github.com/weaveplatform/artifactweave/issues/1529) Terraform virtual repositories | 2/2 | [#1681](https://github.com/weaveplatform/artifactweave/pull/1681), [#1687](https://github.com/weaveplatform/artifactweave/pull/1687) |

[P10 index #1518](https://github.com/weaveplatform/artifactweave/issues/1518)
tracks these 11 epics. All remain open for the maintainer to close after reviewing
their reconciled acceptance and evidence.

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
links the actual existing #216 comments by rule and family. Those are comparison
questions, not live vendor observations. In particular the older ADP-04 Ansible
cached-as-Chef proposal is not an updated description of its per-request
implementation; that discrepancy remains explicit. Unposted inference drafts
are not represented as existing #216 comments. New runtime-fault classifications
retain their primary-source and modern-version limits in their owning research.

## Outstanding qualification and corrective work

| Item | Current disposition |
| --- | --- |
| [#1709](https://github.com/weaveplatform/artifactweave/issues/1709) | The adapter epic explicitly requires one table covering all 23 adapters over member order, nesting and a cycle. Individual native/kit tests exist, but the complete table was not identified in the merged tree. This tracks the missing qualification artifact, not a reproduced runtime defect, and blocks #1571. |
| [#1710](https://github.com/weaveplatform/artifactweave/issues/1710) | The engine epic requires one shared graph traversal. `Members`/`flatten` and route-aware `Walk` still have independent recursive loops. The follow-up shares traversal while preserving both contracts; it is structural acceptance work, not a newly reproduced client failure, and blocks #1571. |
| [#1645 / PR #1705](https://github.com/weaveplatform/artifactweave/pull/1705) | Draft correction for retaining an expired repository file on origin 404. Introduced metadata-content and API-document regressions are being fixed in that PR; its old-head CI failures do not qualify the new correction. |
| [#1704](https://github.com/weaveplatform/artifactweave/issues/1704) | Existing-copy validation-clock SQL faults still need the independently tested cause-preserving 500 correction merged. |
| [#1706 / PR #1707](https://github.com/weaveplatform/artifactweave/pull/1707) | Test-only Go repeated-publication fixture isolation is in CI. Both independent builds still require exactly three artifacts; no registry behavior is changed. |
| [#1693 / PR #1698](https://github.com/weaveplatform/artifactweave/pull/1698) | SQLite literal-file path fix has successful native Windows evidence. Full CI currently waits for the separately diagnosed Go fixture correction to merge and be integrated. |
| [#1571](https://github.com/weaveplatform/artifactweave/issues/1571) | Final evidence audit, decision acceptance, phase completion and exact merged-main gate remain required. |

The successful [RPM correction gate](https://github.com/weaveplatform/artifactweave/actions/runs/44355578274)
at `c7f8dd05dbb64a830f1e32e26b56c1980c87271f` passed all 19 required jobs
and both repository coverage thresholds: **95.4% (91,263/95,687)**. Its strict
native-client selection includes RPM/DNF. The user merged that correction as
`cbed189bceac60069fd037d9de56235e05877e68`. This is useful integrated PR evidence;
it is **not** the post-signoff merged-main run required by #1571.

Before closing #1571 and the qualification epic:

1. Merge the outstanding corrections only after successful latest-head CI.
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
| P12 security configuration | Complete the global `hideUnauthorizedResources` configuration surface; per-virtual hiding and the current global runtime policy retain R4's tests. |
| P13 | Cross-peer federation, convergence and peer recovery; this ledger proves one-node federated members only. |
| P19 / #216 | Choose a live Artifactory build and compare the recorded inferred answers; no trial or live capture was performed for this work. |
| P28 / named format owners | Remaining protocol/client compatibility beyond the explicitly linked P10 flows. |
| P35 | Multi-node merged-cache coordination and distributed invalidation; current caches are bounded per process. |
