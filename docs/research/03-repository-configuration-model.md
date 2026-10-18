# Repository configuration model

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341), and audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#artifactory-7x-check) records both.

Research baseline: 2026-10-12. This document describes the repository configuration JSON
that `PUT`, `POST` and `GET /artifactory/api/repositories/{key}` exchange, and its YAML
descriptor twin. It is the contract behind `pkg/repo` and behind every
`artifactory_*_repository` Terraform resource, so it lists the enumerations, every field
with its default, the typing traps, and the keys that exist only in the Terraform provider
structs. The endpoint behaviour is in [02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md).

Sources: <https://docs.jfrog.com/integrations/docs/configuration-json-files.md>, the
schema embedded in <https://docs.jfrog.com/artifactory/reference/createrepository.md>,
<https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md>,
<https://docs.jfrog.com/artifactory/docs/repository-layouts.md>, and the provider structs
under <https://github.com/jfrog/terraform-provider-artifactory/tree/master/pkg/artifactory/resource/repository>.

Claims whose source matters carry a label:

| Label | Means |
|---|---|
| **[D]** | documented at the cited JFrog page |
| **[S]** | seen in source code at the cited commit or tag |
| **[J]** | JFrog's public Jira, under the [rules for Jira claims](README.md#target-version-and-sources) |
| **[I]** | inferred; no source states it |

## Repository classes

`rclass` is one of `local`, `remote`, `virtual` or `federated`. Listing endpoints return
the same value upper-case under `type`, with the extra value `DISTRIBUTION` for release
bundle repositories. A federated repository is a local repository with members; this
project accepts the class, persists and echoes it, and treats it as local when the only
member is the instance itself.

## Package types

The full `packageType` enumeration JFrog's JSON page gives for a local repository is
`alpine`, `cargo`, `composer`, `bower`, `chef`, `cocoapods`, `conan`, `conda`, `cran`,
`debian`, `docker`, `helm`, `helmoci`, `huggingfaceml`, `gems`, `gitlfs`, `go`, `gradle`,
`ivy`, `maven`, `npm`, `nuget`, `oci`, `opkg`, `pub`, `puppet`, `pypi`, `rpm`, `sbt`,
`swift`, `terraform`, `terraformbackend`, `vagrant`, `ansible`, `bazelmodules` and
`generic`, with `generic` as the default. `bazelmodules` appears in the JSON documentation but not in
the OpenAPI enumeration.

The allowed set differs by class, and JFrog's sources disagree. The table compares each
class with the local list above:
- the JSON column is [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md) [D];
- the provider columns are the classes each package type lists in terraform-provider-artifactory
  12.11.14's [`default_repo_layout_map.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/default_repo_layout_map.go) [S].

| Class | JSON page leaves out | Provider leaves out | Provider adds |
|---|---|---|---|
| local | | `bazelmodules` | `hex`, `nix`, `releasebundles`; `machinelearning` has a local resource but no map entry |
| remote | `terraformbackend`, `vagrant`, `bazelmodules` | `terraformbackend`, `vagrant` | `vcs`, `p2`, `hex`, `nix`, `aieditorextensions`, `jetbrainsplugins` |
| virtual | `cargo`, `cocoapods`, `opkg`, `vagrant`, `terraformbackend` | `cargo`, `vagrant`, `terraformbackend`, `huggingfaceml`, `bazelmodules` | `p2`, `hex`, `nix` |
| federated | `terraformbackend`, `pub`, `swift`, `terraform` | `terraformbackend`, `bazelmodules` | `releasebundles` |

Where the sources conflict:
- **Remote `bazelmodules`:** the JSON page leaves it out. The provider has a remote Bazel
  resource (`resource_artifactory_remote_bazel_repository.go`) and no local one.
- **Virtual `cocoapods` and `opkg`:** the JSON page leaves them out and the provider offers
  them. The [YAML page](https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md)
  lists `cocoapods` and `p2` for virtual repositories.
- **Virtual `huggingfaceml` and `bazelmodules`:** the JSON page lists them and the provider
  does not. Virtual Hugging Face repositories arrived in
  [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed).
- **Federated `pub`, `swift` and `terraform`:** the JSON page leaves them out and the provider
  offers them.

Three types are newer than the JSON page's lists:
- Machine Learning, local repositories, from [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed);
- Hex, with virtual repositories in the same release, from [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed);
- Nix, local, remote, virtual and federated, from [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed).
  The provider has no federated Nix.

This project implements `docker`, `oci`, `helm`, `helmoci`, `npm`, `maven`, `gradle`,
`ivy`, `sbt`, `go`, `pypi`, `nuget`, `terraform` and `generic`. Every other type that
`pkg/repo/packagetypes.go` allows for the class is accepted, and its configuration is
persisted and echoed with no package handler of its own. That includes `terraformbackend`,
which would need the Terraform Enterprise state API to serve; see
[12-open-questions.md](12-open-questions.md).

## Other enumerations

| Field | Values | Default |
|---|---|---|
| `checksumPolicyType` (local, federated) | `client-checksums`, `server-generated-checksums` | `client-checksums` |
| `remoteRepoChecksumPolicyType` | `generate-if-absent`, `fail`, `ignore-and-generate`, `pass-thru` | `generate-if-absent` |
| `snapshotVersionBehavior` | `unique`, `non-unique`, `deployer` | `unique` (since 7.41; in 6.20 the descriptor already defaulted to `unique` and the REST model to `non-unique`, [research 27](27-maven.md#10-corrections-to-research-03-and-04)) |
| `pomRepositoryReferencesCleanupPolicy` (virtual) | `discard_active_reference`, `discard_any_reference`, `nothing` | `discard_active_reference` |
| `dockerApiVersion` | `V2` (`V1` is legacy and still listed in the YAML form) | `V2` |
| `terraformType` | `MODULE`, `PROVIDER` in the documentation; the Terraform provider sends lower-case `module` and `provider` | none |
| `vcsType` | `GIT` | `GIT` |
| `vcsGitProvider` | `GITHUB`, `GITHUBENTERPRISE`, `BITBUCKET`, `OLDSTASH`, `STASH`, `ARTIFACTORY`, `CUSTOM` | `GITHUB` |

`terraformType` must be accepted case-insensitively and echoed upper-case.

## Repository layouts

`repoLayoutRef` names a layout. When omitted on create, the REST API defaults to
`maven-2-default` (the web UI defaults to `simple-default`). Bundled layout identifiers,
confirmed from the provider's default layout map:

| Identifier | Default for |
|---|---|
| `maven-2-default` | `maven`, `gradle` (REST default when omitted) |
| `maven-1-default`, `ivy-default`, `gradle-default`, `sbt-default`, `sbt-ivy-default` | legacy JVM layouts |
| `simple-default` | `generic`, `docker`, `oci`, `helm`, `helmoci`, `pypi`, `terraform`, `terraformbackend`, `debian`, `rpm`, `alpine`, `cargo`, `conda`, `cran`, `gems`, `gitlfs`, `vagrant` |
| `npm-default` | `npm` |
| `nuget-default` | `nuget` |
| `go-default` | `go` |
| `terraform-module-default`, `terraform-provider-default` | `terraform` by `terraformType` (the provider's `terraform_module` and `terraform_provider` resources) |
| `bower-default`, `cargo-default`, `composer-default`, `conan-default`, `puppet-default`, `ansible-default`, `swift-default`, `nix-default`, `build-default` | other ecosystems |
| `vcs-default` | none by default; Artifactory's built-in layout for `vcs` remotes |

A layout is defined by `artifactPathPattern`, `distinctiveDescriptorPathPattern`,
`descriptorPathPattern`, `folderIntegrationRevisionRegExp` and
`fileIntegrationRevisionRegExp`. Patterns use the tokens `[org]`, `[orgPath]`,
`[module]`, `[baseRev]`, `[folderItegRev]`, `[fileItegRev]`, `[classifier]`, `[ext]`,
`[type]` and `[custom<regex>]`; parenthesised groups are optional. The Maven 2 layout is:

```text
artifact:   [orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]
descriptor: [orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).pom
folder integration revision: SNAPSHOT
file integration revision:   SNAPSHOT|(?:(?:\d{8}.\d{6})-(?:\d+))
```

Source: <https://docs.jfrog.com/artifactory/docs/repository-layouts.md> and
<https://raw.githubusercontent.com/jfrog/terraform-provider-artifactory/master/pkg/artifactory/resource/repository/default_repo_layout_map.go>.

### Bundled definitions

`pkg/layout/bundled.go` holds each bundled definition with its source (#277):

- **Fifteen are copied from Artifactory 6.20's default descriptor** [S]:
  `base/config/src/main/resources/META-INF/default/artifactory.config.xml:112-221` in the
  `knottnhupfer/artifactory-oss` mirror at `b99e5649bcad`, kept in its order. They are
  `maven-2-default`, `ivy-default`, `gradle-default`, `maven-1-default`, `nuget-default`,
  `npm-default`, `bower-default`, `vcs-default`, `sbt-default`, `simple-default`,
  `composer-default`, `conan-default`, `puppet-default`, `go-default` and `build-default`.
  Maven 1 uses `.+` for both integration revisions and every later non-JVM layout `.*`.
- **Two depart from the descriptor on purpose:** `go-default` (DV-040) and
  `conan-default` (DV-056) match nothing as Artifactory ships them.
- **Six were added in 7.x without a published definition:** `sbt-ivy-default` (named on
  JFrog's sbt page), and `terraform-module-default`, `terraform-provider-default`,
  `ansible-default`, `swift-default` and `nix-default` (named in the Terraform provider's
  default map). They are reconstructed from the storage paths JFrog documents [I] (DV-055)
  and are on the P19 list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).
- **One was added in 7.x with a known definition:** `cargo-default`,
  `crates/[module]/[module]-[baseRev].[ext]` with `.*` integration revisions (#354). JFrog
  does not publish it, but every exported 7.x descriptor below defines it this way, and it
  describes the one path JFrog's [Cargo page](https://docs.jfrog.com/artifactory/docs/cargo-repositories.md)
  accepts, `crates/<name>/<name>-<version>.crate`; a crate stored anywhere else is not
  indexed. The provider's default for a `cargo` repository is still `simple-default`. On the
  P19 list [I].
- **"npm Test" is not bundled** [I]. The layouts page lists it among the default layouts,
  but its only trace in JFrog's documentation is the same page's screenshot of the layout
  list, which shows a layout named `npm-test-layout` beside `npm-default`. Every bundled
  layout is named `<type>-default`, no exported 7.x descriptor has it, and the provider's map
  does not name it, so it reads as a custom layout on the instance the screenshot came from.
  On the P19 list.

#### Exported 7.x descriptors

Four configuration descriptors exported from running 7.x instances are published in public
repositories. They are an instance's `artifactory.config.latest.xml`, or an import made from
one, not JFrog documentation, so what they show is [I]:

| Descriptor | Schema |
|---|---|
| [octopusden/octopus-dms-service](https://github.com/octopusden/octopus-dms-service/blob/9500dcb54b52/test-common/src/main/artifactory/dump/20260120.084302/etc/artifactory.config.latest.xml), dumped 2026-01-20 | 3.4.8 |
| [devarsh10/jfrog-artifactory](https://github.com/devarsh10/jfrog-artifactory/blob/1f7393f59ef0/artifactory/var/etc/artifactory/artifactory.config.latest.xml) | 3.4.5 |
| [scala/community-build](https://github.com/scala/community-build/blob/0a9bdc4e9f61/artifactory.xml) | 3.4.2 |
| [Panda-Academy-Workshops/Infrastructure](https://github.com/Panda-Academy-Workshops/Infrastructure/blob/bf56ad425899/artifactory/artifactory.config.import.xml) | 3.1.26 |

All four define `cargo-default` as `pkg/layout` now does. They also define other layouts
differently from `pkg/layout`. This document records the differences; nothing here acts on
them yet:

| Layout | Exported 7.x descriptors | `pkg/layout` |
|---|---|---|
| `npm-default` | `[orgPath]/-/[module]-[baseRev](-[fileItegRev]).tgz`, in all four | 6.20's `[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).tgz` |
| `go-default` | `[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]` in three; the scala one keeps 6.20's `[orgPath]/[module]/@v/v[refs].zip` | `[orgPath]/[module]/@v/[baseRev].[ext]` (DV-040) |
| `terraform-module-default` | `[namespace]/[module-name]/[provider]/[version].[ext]` | `[orgPath]/[module]/[provider<[^/]+>]/[baseRev].[ext]` (DV-055) |
| `terraform-provider-default` | `[namespace]/[provider-name]/[version]/terraform-provider-[provider-name]_[version]_[os]_[arch].[ext]` | `[orgPath]/[module]/[baseRev]/terraform-provider-[module]_[baseRev]_[os<[^_/]+>]_[arch<[^./]+>].[ext]` (DV-055) |
| `swift-default` | `[scope]/[name]/[name]-[version].zip` | `[orgPath]/[module]/[module]-[baseRev].[ext]` (DV-055) |
| `ansible-default` | `collections/[namespace]/[name]/[version]/[namespace]-[name]-[version].tar.gz` | `[orgPath]/[module]/[orgPath]-[module]-[baseRev].tar.gz` (DV-055) |
| `sbt-ivy-default` | `[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]`, descriptor `ivy-[baseRev](-[fileItegRev])(-[classifier]).xml` in the revision folder; in the two newest only | sbt's Ivy-style pattern (DV-055) |
| `gradle-default` | in the scala one only | bundled, from 6.20 |
| `nix-default` | in none | bundled (DV-055) |

The Terraform, Swift and Ansible patterns use tokens outside the documented set
(`[namespace]`, `[module-name]`, `[provider-name]`, `[scope]`, `[name]`, `[version]`).
6.20's parser drops an unknown token, as it does `[refs]` in its `go-default` (DV-040);
whether 7.x's parser reads them is not known.

Artifactory turns a pattern into an expression in `RepoLayoutUtils.generateRegExpFromPattern`
[S]: `[module]` is greedy (`[^/]+`), so where it appears only in a file name, as in
`maven-1-default` and `build-default`, `commons-lang-2.1.jar` is module `commons-lang`; a `?`
outside a token is escaped as a literal; and a repeated token is a back-reference to its first
value. `pkg/layout` matches all three.

What the bundled layouts do not describe, shown by real paths in
`pkg/layout/bundled_test.go` [S]:

| Layout | Path it does not match | Why |
|---|---|---|
| `npm-default` | `lodash/-/lodash-4.17.21.tgz` | npm's tarball path is not the layout's |
| `ivy-default` | `org.gradle.sample/project1/1.0/jars/project1.jar` | Ivy's own default pattern has no revision in the file name |
| `gradle-default` | `org.gradle.sample/project1/1.0/ivy-1.0.xml` | Gradle's ivy-publish writes the descriptor in the revision folder; the layout expects it beside the module folders, as in Gradle's old Wharf cache |
| `sbt-default` | `org.example/lib_2.13/1.0/jars/lib_2.13.jar` | the slashes after the optional `scala_` and `sbt_` groups are mandatory, so only plugin paths match; JFrog's sbt page says the layout does not support cleanup |

## Common fields

| Field | Default | Notes |
|---|---|---|
| `key` | | Mandatory on create; the URL path segment wins over the body. |
| `rclass` | | Mandatory. |
| `packageType` | `generic` | |
| `projectKey` | | JFrog marks `environments` "mandatory when assigning repo to project" ([D] configuration-json-files); artifactweave stores `["DEV"]` when it is missing ([decision 0013](decisions/0013-projects-and-environments.md)). The web UI prepends the project key to the key of a repository created inside a project ([D] [federated form reference](https://docs.jfrog.com/artifactory/docs/federated-repository-form-field-reference.md)); artifactweave does not check the prefix. |
| `environments` | | Array; `DEV` and `PROD` are built in. |
| `description`, `notes` | `""` | |
| `includesPattern` | `**/*` | An empty string also means everything. |
| `excludesPattern` | `""` | |
| `repoLayoutRef` | `maven-2-default` | See above. |
| `blackedOut` | `false` | |
| `xrayIndex` | `false` | Persisted and echoed; no scanner behind it. |
| `propertySets` | `[]` | Names of property sets. |
| `priorityResolution` | `false` | |
| `downloadRedirect` | `false` | |
| `cdnRedirect` | `false` | JFrog Cloud Enterprise+ only. |
| `primaryKeyPairRef`, `secondaryKeyPairRef` | | Signing key pairs (Helm, Debian, Terraform providers, and npm's ECDSA package signing, [research 25](25-npm.md#5-storage-layout-and-properties)). |
| `signedUrlTtl`, `xrayDataTtl` | | Read-only in responses. |

## Local fields

| Field | Default | Notes |
|---|---|---|
| `checksumPolicyType` | `client-checksums` | |
| `handleReleases`, `handleSnapshots` | `true` | Maven family. |
| `maxUniqueSnapshots` | `0` | 0 means unlimited. JFrog's knowledge base is reported to give an effective minimum of 2; 6.20's cleanup has no floor ([research 27](27-maven.md#7-maxuniquesnapshots)). |
| `snapshotVersionBehavior` | `unique` | |
| `suppressPomConsistencyChecks` | `false` for `maven`; `true` for `gradle`, `ivy` and `sbt` | As the Terraform provider and the UI set them; 6.20's descriptor defaults to `true` and its REST model to `false` ([research 27](27-maven.md#6-pom-consistency-checks)). |
| `maxUniqueTags` | `0` | Docker; 0 means unlimited. |
| `dockerTagRetention` | `1` | Docker; number of tags kept per image when `maxUniqueTags` applies. |
| `dockerApiVersion` | `V2` | |
| `blockPushingSchema1` | `false` | Docker. |
| `archiveBrowsingEnabled` | `false` | |
| `enableFileListsIndexing` | `false` | |
| `optionalIndexCompressionFormats` | `[]` | Debian. |
| `debianTrivialLayout`, `calculateYumMetadata`, `yumRootDepth` | | Out-of-scope ecosystems; accepted and persisted. |
| `terraformType` | | `MODULE` or `PROVIDER`. |
| `encryptStates` | `true` | `terraformbackend`. |
| `forceNugetAuthentication`, `forceP2Authentication`, `forceConanAuthentication` | `false` | |
| `forceMetadataNameVersion`, `forceNonDuplicateChart` | `false` | Helm enforce layout, 7.104.2+. |

## Remote fields

| Field | Default | Notes |
|---|---|---|
| `url` | | Mandatory. |
| `username`, `password` | | Password is a sealed value and is masked on read. |
| `proxy`, `disableProxy` | `""`, `false` | Named proxy from the descriptor. |
| `remoteRepoLayoutRef` | `""` | Layout of the upstream when it differs. |
| `remoteRepoChecksumPolicyType` | `generate-if-absent` | |
| `handleReleases`, `handleSnapshots`, `maxUniqueSnapshots`, `suppressPomConsistencyChecks` | as local | |
| `hardFail` | `false` | Fail the request when the upstream fails instead of serving from cache. |
| `offline` | `false` | Serve from cache only. |
| `storeArtifactsLocally` | `true` | `false` streams through without caching. |
| `socketTimeoutMillis` | `15000` | |
| `localAddress` | | Outbound bind address. |
| `retrievalCachePeriodSecs` | `7200`; `21600` for `docker`, `oci`, `helmoci` | How long non-unique metadata is served from cache. The minimum is 30, and 14,400 for NuGet remotes of chocolatey.org ([D] [remote-repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories.md)); smart remotes are exempt. SaaS enforces the 30 from [7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas); the self-managed notes up to 7.161.32 do not list it. |
| `missedRetrievalCachePeriodSecs` | `1800`; `600` for `go` | Negative cache. |
| `metadataRetrievalTimeoutSecs` | `60` | |
| `assumedOfflinePeriodSecs` | `300` | After a connection failure the upstream is assumed offline for this long. |
| `unusedArtifactsCleanupPeriodHours` | `0` | 0 disables cache cleanup. |
| `fetchJarsEagerly`, `fetchSourcesEagerly`, `rejectInvalidJars` | `false` | Maven family. |
| `shareConfiguration`, `synchronizeProperties` | `false` | |
| `blockMismatchingMimeTypes` | `true` | |
| `mismatchingMimeTypesOverrideList` | `""` | |
| `allowAnyHostAuth` | `false` | |
| `enableCookieManagement` | `false` | |
| `enableTokenAuthentication` | `false` | Required for Docker Hub. |
| `bypassHeadRequests` | `false` | |
| `clientTlsCertificate` | `""` | Alias of a stored client certificate. |
| `listRemoteFolderItems` | `false` | Documented as a string; see "Typing traps". |
| `queryParams` | `""` | Appended to upstream requests. |
| `disableUrlNormalization` | `false` | |
| `customHttpHeaders` | `[]` | `[{"name","value","sensitive"}]`; generic remotes need 7.146. |
| `externalDependenciesEnabled`, `externalDependenciesPatterns` | `false`, `[]` | Docker foreign layers; npm, Go and Bower external dependencies. |
| `contentSynchronisation` | | `{"enabled","statistics":{"enabled"},"properties":{"enabled"},"source":{"originAbsenceDetection"}}` for smart remotes. |
| `blockPushingSchema1` | `false` | Docker. |
| `forceNugetAuthentication`, `forceP2Authentication`, `forceConanAuthentication` | `false` | |
| `pyPIRegistryUrl` | `https://pypi.org` | Note the capitalisation. |
| `pyPIRepositorySuffix` | `simple` | |
| `feedContextPath` | `api/v2` | NuGet V2. |
| `downloadContextPath` | `api/v2/package` | NuGet V2. |
| `v3FeedUrl` | `https://api.nuget.org/v3/index.json` | |
| `symbolServerUrl` | | NuGet symbols. |
| `chartsBaseUrl` | | Helm; nested under `helm:` in the YAML form. |
| `terraformRegistryUrl`, `terraformProvidersUrl` | `https://registry.terraform.io`, `https://releases.hashicorp.com` | |
| `vcsType`, `vcsGitProvider`, `vcsGitDownloadUrl` | `GIT`, `GITHUB`, `""` | Go VCS remotes (out of scope initially). |
| `dockerProjectId` | | Google Artifact Registry upstreams. |
| `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl` | `https://registry.bower.io`, `https://github.com/rust-lang/crates.io-index`, `https://packagist.org` | Out-of-scope ecosystems; accepted and persisted. Defaults from [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md) [D]. |

## Virtual fields

| Field | Default | Notes |
|---|---|---|
| `repositories` | `[]` | Ordered member keys; local, remote or virtual. Optional ([D] configuration-json-files; the provider's `virtual.go` makes it optional too). From [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) (RTDEV-62756) a member must have the virtual's package type, unless the virtual is `generic`. |
| `defaultDeploymentRepo` | | Receives writes. |
| `artifactoryRequestsCanRetrieveRemoteArtifacts` | `false` | |
| `virtualRetrievalCachePeriodSecs` | `7200` | Cache period for merged metadata; in YAML it is `virtualCacheConfig.virtualRetrievalCachePeriodSecs`. "The default is 7200 seconds" ([D] [virtual-repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories.md)). npm virtual repositories moved from 600 to 7200 in [SaaS 7.165.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71650-saas); the YAML sample still shows 600. |
| `keyPair` | | Signing key for merged metadata. |
| `pomRepositoryReferencesCleanupPolicy` | `discard_active_reference` | |
| `forceMavenAuthentication`, `forceNugetAuthentication` | `false` | |
| `externalDependenciesEnabled`, `externalDependenciesPatterns`, `externalDependenciesRemoteRepo` | `false`, `[]`, `""` | npm, Go, Bower. |
| `hideUnauthorizedResources` | `false` | |
| `resolveDockerTagsByTimestamp` | `false` | |
| `useNamespaces` | `false` | Helm. |
| `dockerApiVersion` | `V2` | |
| `debianTrivialLayout`, `debianDefaultArchitectures`, `optionalIndexCompressionFormats` | | Out of scope; accepted. |

## Federated fields

Everything a local repository has, plus `members`, an array of `{"url","enabled"}` where
`enabled` is a boolean (default `true`; documented as a string, see below) and `url` may be
derived from the base URL and the key, and `proxy` and `disableProxy`.

## Typing traps

The JSON documentation and the OpenAPI schema type `enableFileListsIndexing`,
`downloadRedirect`, `cdnRedirect`, `priorityResolution` and `blockPushingSchema1` as
strings (`"false"`) on local and federated repositories, while `priorityResolution` is a
boolean on remote repositories, and federated `members[].enabled` is a string. The JSON
documentation also gives remote `listRemoteFolderItems` as `"false"` (default)
([configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md) [D]).

Artifactory sends JSON booleans for all of them, whatever the documentation says. Provider
12.11.14 decodes every one into a Go `bool` (`local.RepositoryBaseParams`, which federated
reuses, the Docker and RPM models, and the federated `Member`), and its sample local
repository shows `"cdnRedirect": false` and `"blockPushingSchema1": true`. Echoing the
string spelling broke the provider's refresh after a create ("cannot unmarshal string into
Go struct field ... priorityResolution of type bool"), which the `terraform` CI job found
(#69). artifactweave writes booleans for every class and accepts both spellings on input, for
clients that followed the documentation. The `pkg/repo` round-trip tests cover every such
field. `listRemoteFolderItems` is the exception. jfrog-client-go decodes it as a boolean
too (`remoterepository.go`), but `pkg/repo` accepts only the boolean spelling, so the
documented string is refused (see [the 7.x check](#artifactory-7x-check)).

## Keys known only from the Terraform provider

These keys do not appear in JFrog's JSON documentation or OpenAPI schema. They are real
REST keys because the provider reads them back after a create, so they must be supported:
`dockerTagRetention`, `pyPIRepositorySuffix`, `symbolServerUrl`, `terraformRegistryUrl`,
`terraformProvidersUrl`, `dockerProjectId`, `useNamespaces`, `hideUnauthorizedResources`,
`resolveDockerTagsByTimestamp`, `rejectInvalidJars`, `forceMetadataNameVersion`,
`forceNonDuplicateChart`. Keys present in the YAML documentation but not the JSON
documentation: `mismatchingMimeTypesOverrideList`, `queryParams`,
`virtualRetrievalCachePeriodSecs`, `chartsBaseUrl`.

The JFrog clients send more keys of this kind [S]:
- `curated` and `passThrough` on remote Docker, npm, Go, Maven and Gradle repositories
  (provider 12.11.14, [`remote/curation.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/curation.go));
- `propagateQueryParams` and `retrieveSha256FromServer` on remote generic repositories
  (provider 12.11.14, [`resource_artifactory_remote_generic_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/resource_artifactory_remote_generic_repository.go));
- `cargoAnonymousAccess`, `yumGroupFileNames`, virtual `p2Urls` and remote
  `podsSpecsRepoUrl` (jfrog-client-go, [`artifactory/services`](https://github.com/jfrog/jfrog-client-go/tree/master/artifactory/services)).

## JSON and YAML name differences

The YAML descriptor used by `PATCH /api/system/configuration` keys repositories by name
under `localRepositories`, `remoteRepositories`, `virtualRepositories` and
`federatedRepositories` (the last one as a list), and renames a few fields:

| JSON | YAML |
|---|---|
| `packageType` | `type` |
| `repoLayoutRef` | `repoLayout` |
| `xrayIndex` | `xray.enabled` |
| `virtualRetrievalCachePeriodSecs` | `virtualCacheConfig.virtualRetrievalCachePeriodSecs` |
| `chartsBaseUrl` | `helm.chartsBaseUrl` |
| `contentSynchronisation` | `contentSynchronisation` (same) |
| `downloadRedirect` | `downloadRedirect.enabled` on local and remote repositories; flat `downloadRedirect` in the federated sample |
| `primaryKeyPairRef` | `primaryKeyPairDescriptor` in the local sample |
| `remoteRepoLayoutRef` | `remoteRepoLayout`, the key in JFrog support's YAML in [J] [RTFACT-29540](https://jfrog.atlassian.net/browse/RTFACT-29540) and [RTFACT-29858](https://jfrog.atlassian.net/browse/RTFACT-29858). The second reports that on 7.49.8 the key did not change the field, and was closed without a fix |
| `podsSpecsRepoUrl` | `cocoaPods.cocoaPodsSpecsRepoUrl` |
| `p2Urls` (virtual) | `p2.urls` |
| `packageType: terraformbackend` | `type: terraformbe` |

The rows from `downloadRedirect` on, except `remoteRepoLayout`, are from the
[YAML page](https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md) [D].

A `null` value in YAML deletes the element. Inside a repository element, only string fields
other than `type` may be set to `null`
([D] [delete an element](https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md)).
An empty YAML document deletes every repository, which artifactweave refuses with 400
(DV-023). Example:

```yaml
localRepositories:
  docker-local:
    type: docker
    repoLayout: simple-default
    xray:
      enabled: false
remoteRepositories:
  maven-central:
    type: maven
    url: https://repo1.maven.org/maven2/
    retrievalCachePeriodSecs: 7200
virtualRepositories:
  maven:
    type: maven
    repositories:
      - maven-local
      - maven-central
```

## Per-ecosystem summary

| Ecosystem | Local | Remote | Virtual |
|---|---|---|---|
| docker, oci, helmoci | `dockerApiVersion`, `maxUniqueTags`, `dockerTagRetention`, `blockPushingSchema1` | `enableTokenAuthentication`, `externalDependenciesEnabled`, `externalDependenciesPatterns`, `blockPushingSchema1`, `retrievalCachePeriodSecs` 21600, `dockerProjectId` | `resolveDockerTagsByTimestamp`, `dockerApiVersion` |
| npm | | | `externalDependenciesEnabled`, `externalDependenciesPatterns`, `externalDependenciesRemoteRepo`, `virtualRetrievalCachePeriodSecs` |
| maven, gradle, ivy, sbt | `handleReleases`, `handleSnapshots`, `snapshotVersionBehavior`, `checksumPolicyType`, `maxUniqueSnapshots`, `suppressPomConsistencyChecks` | same plus `remoteRepoChecksumPolicyType`, `fetchJarsEagerly`, `fetchSourcesEagerly`, `rejectInvalidJars`, `listRemoteFolderItems` | `pomRepositoryReferencesCleanupPolicy`, `keyPair`, `forceMavenAuthentication` |
| pypi | | `pyPIRegistryUrl`, `pyPIRepositorySuffix` | |
| nuget | `forceNugetAuthentication`, `maxUniqueSnapshots` | `feedContextPath`, `downloadContextPath`, `v3FeedUrl`, `symbolServerUrl`, `forceNugetAuthentication` | `forceNugetAuthentication` |
| go | | `vcsGitProvider`, `vcsGitDownloadUrl`, `missedRetrievalCachePeriodSecs` 600 | `externalDependenciesEnabled`, `externalDependenciesPatterns` |
| helm | `forceMetadataNameVersion`, `forceNonDuplicateChart` | `chartsBaseUrl`, `externalDependenciesEnabled`, `externalDependenciesPatterns` | `virtualRetrievalCachePeriodSecs`, `useNamespaces` |
| terraform | `terraformType` | `terraformRegistryUrl`, `terraformProvidersUrl` | |

## Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)), and audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation. The pages read again are [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md), [remote-repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories.md),
[virtual-repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories.md), [repository-layouts](https://docs.jfrog.com/artifactory/docs/repository-layouts.md), [createrepository](https://docs.jfrog.com/artifactory/reference/createrepository.md) and
[getrepositoryconfiguration](https://docs.jfrog.com/artifactory/reference/getrepositoryconfiguration.md).

**Sources of the audit.** JFrog's public Jira, read through its REST API; terraform-provider-artifactory 12.11.14, terraform-provider-shared and jfrog-client-go; and the documentation pages cited in the sections above. A KB search found no article on repository configuration.

**Confirmed for 7.x:**
- `includesPattern` defaults to everything when a create request leaves it out. An empty
  string was stored instead until [7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed) (RTDEV-55298).
- `listRemoteFolderItems` defaults to `false`: remote folder listing has been off by default
  since [7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed).
- The Maven settings and their defaults ([research 27 section 13](27-maven.md#13-artifactory-7x-check)).
- `primaryKeyPairRef` and `secondaryKeyPairRef` in the create and get definitions.

**Changed or added in 7.x:**
- **Signing key pairs** also sign npm packages (ECDSA), as the row above now says.
- **Bundled layouts.** The 7.x layout page lists Cargo and npm Test among the default
  layouts. `cargo-default` is bundled now ([#354](https://github.com/weaveplatform/artifactweave/issues/354));
  "npm Test" is not a bundled layout ("Bundled definitions").
- **npm strict layout.** "Enforce strict layout" is an npm setting on the Basic tab, but no
  repository JSON field for it is documented (DV-041,
  [research 25 section 6](25-npm.md#6-strict-layout)).
- **Global excludes.** `artifactory.repo.includeExclude.globalExcludes` ([7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed)) adds
  system-wide exclude patterns. artifactweave has no equivalent, and no client sets it.
- **Repository API changes** that research 02 covers: filters on Get All Repository
  Configurations ([7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)) and batch updates ([7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)).

**Where artifactweave differs from 7.x** (found by #378; the issue named changes the code):
- 7.x differs: a key may not begin with a digit or contain spaces or special characters ([D] [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md); [S] terraform-provider-shared [`repo_key.go`](https://github.com/jfrog/terraform-provider-shared/blob/main/validator/fw/string/repo_key.go)); fixed by [#395](https://github.com/weaveplatform/artifactweave/issues/395).
- 7.x differs: a key has at most 64 characters ([D] [federated form reference](https://docs.jfrog.com/artifactory/docs/federated-repository-form-field-reference.md); [S] `repo_key.go`, "must be 1 - 64"); fixed by [#395](https://github.com/weaveplatform/artifactweave/issues/395).
- 7.x differs: `repositories` is optional, so a virtual repository may have no members ([D] [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md); [S] provider 12.11.14 [`virtual.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/virtual/virtual.go)); fixed by [#394](https://github.com/weaveplatform/artifactweave/issues/394).
- 7.x differs: a non-generic virtual repository refuses a member of another package type ([D] [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed), RTDEV-62756); fixed by [#394](https://github.com/weaveplatform/artifactweave/issues/394).
- 7.x differs: each class accepts the package types in the provider's map, and the newer `hex`, `nix` and `machinelearning` ([S] provider map and [D] release notes, "Package types"), where `pkg/repo/packagetypes.go` follows the JSON page; fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- 7.x differs: a federated repository accepts `terraformType` ([S] provider 12.11.14 [`resource_artifactory_federated_terraform_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/federated/resource_artifactory_federated_terraform_repository.go)); fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- 7.x differs: remote `listRemoteFolderItems` is documented as the string `"false"` ([D] configuration-json-files), a spelling `pkg/repo` refuses with 400 ("Typing traps"); fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- 7.x differs: Artifactory keeps `curated`, `passThrough`, `propagateQueryParams`, `retrieveSha256FromServer`, `cargoAnonymousAccess`, `yumGroupFileNames`, `p2Urls` and `podsSpecsRepoUrl`, which the clients read back ([S], "Keys known only from the Terraform provider"); `pkg/repo` drops them, so a plan that sets one never converges; fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- 7.x differs: `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl`, `terraformRegistryUrl` and `terraformProvidersUrl` take the defaults in "Remote fields" ([D] configuration-json-files; [S] provider remote Terraform resource); fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- 7.x differs: `retrievalCachePeriodSecs` has a minimum of 30 seconds on remote repositories other than smart remotes ([D] [remote-repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories.md); [D] [SaaS 7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas)); fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).

**Unconfirmed for 7.x:**
- The bundled layouts copied from the 6.20 descriptor ("Bundled definitions"): JFrog
  publishes only their names, so their patterns stay as 6.20 had them, on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
  Exported 7.x descriptors define `npm-default` and `go-default` differently
  ("Exported 7.x descriptors").
- 6.20's default for `snapshotVersionBehavior` and `suppressPomConsistencyChecks`, which the
  table notes.
