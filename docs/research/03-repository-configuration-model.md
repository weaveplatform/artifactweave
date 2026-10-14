# Repository configuration model

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

## Repository classes

`rclass` is one of `local`, `remote`, `virtual` or `federated`. Listing endpoints return
the same value upper-case under `type`, with the extra value `DISTRIBUTION` for release
bundle repositories. A federated repository is a local repository with members; this
project accepts the class, persists and echoes it, and treats it as local when the only
member is the instance itself.

## Package types

The full `packageType` enumeration for a local repository is `alpine`, `cargo`,
`composer`, `bower`, `chef`, `cocoapods`, `conan`, `conda`, `cran`, `debian`, `docker`,
`helm`, `helmoci`, `huggingfaceml`, `gems`, `gitlfs`, `go`, `gradle`, `ivy`, `maven`,
`npm`, `nuget`, `oci`, `opkg`, `pub`, `puppet`, `pypi`, `rpm`, `sbt`, `swift`,
`terraform`, `terraformbackend`, `vagrant`, `ansible`, `bazelmodules` and `generic`,
with `generic` as the default. `bazelmodules` appears in the JSON documentation but not in
the OpenAPI enumeration.

The allowed set differs by class:

| Class | Excludes |
|---|---|
| remote | `terraformbackend`, `vagrant`, `bazelmodules` (adds `vcs` and `p2` in the YAML form) |
| virtual | `cargo`, `cocoapods`, `opkg`, `vagrant`, `terraformbackend` |
| federated | `terraformbackend`, `swift` |

This project implements `docker`, `oci`, `helm`, `helmoci`, `npm`, `maven`, `gradle`,
`ivy`, `sbt`, `go`, `pypi`, `nuget`, `terraform` and `generic`. `terraformbackend` is
rejected with a clear 400 in version 1 because it requires the Terraform Enterprise state
API; see [12-open-questions.md](12-open-questions.md).

## Other enumerations

| Field | Values | Default |
|---|---|---|
| `checksumPolicyType` (local, federated) | `client-checksums`, `server-generated-checksums` | `client-checksums` |
| `remoteRepoChecksumPolicyType` | `generate-if-absent`, `fail`, `ignore-and-generate`, `pass-thru` | `generate-if-absent` |
| `snapshotVersionBehavior` | `unique`, `non-unique`, `deployer` | `unique` (since 7.41) |
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
| `simple-default` | `generic`, `docker`, `oci`, `helm`, `helmoci`, `pypi`, `terraform`, `terraformbackend`, `debian`, `rpm`, `alpine`, `conda`, `cran`, `gems`, `gitlfs`, `vagrant` |
| `npm-default` | `npm` |
| `nuget-default` | `nuget` |
| `go-default` | `go` |
| `terraform-module-default`, `terraform-provider-default` | `terraform` by `terraformType` (the provider's `terraform_module` and `terraform_provider` resources) |
| `bower-default`, `composer-default`, `conan-default`, `puppet-default`, `ansible-default`, `swift-default`, `nix-default`, `build-default` | other ecosystems |

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

## Common fields

| Field | Default | Notes |
|---|---|---|
| `key` | | Mandatory on create; the URL path segment wins over the body. |
| `rclass` | | Mandatory. |
| `packageType` | `generic` | |
| `projectKey` | | When set, `environments` becomes mandatory and the key must carry the project prefix. |
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
| `primaryKeyPairRef`, `secondaryKeyPairRef` | | Signing key pairs (Helm, Debian, Terraform providers). |
| `signedUrlTtl`, `xrayDataTtl` | | Read-only in responses. |

## Local fields

| Field | Default | Notes |
|---|---|---|
| `checksumPolicyType` | `client-checksums` | |
| `handleReleases`, `handleSnapshots` | `true` | Maven family. |
| `maxUniqueSnapshots` | `0` | 0 means unlimited; the effective minimum is 2. |
| `snapshotVersionBehavior` | `unique` | |
| `suppressPomConsistencyChecks` | `false` | |
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
| `retrievalCachePeriodSecs` | `7200`; `21600` for `docker`, `oci`, `helmoci` | How long non-unique metadata is served from cache. |
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
| `listRemoteFolderItems` | `false` | |
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
| `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl` | | Out-of-scope ecosystems; accepted and persisted. |

## Virtual fields

| Field | Default | Notes |
|---|---|---|
| `repositories` | `[]` | Ordered member keys; local, remote or virtual. |
| `defaultDeploymentRepo` | | Receives writes. |
| `artifactoryRequestsCanRetrieveRemoteArtifacts` | `false` | |
| `virtualRetrievalCachePeriodSecs` | `600` in the YAML sample, `7200` in the provider | Cache period for merged metadata; in YAML it is `virtualCacheConfig.virtualRetrievalCachePeriodSecs`. |
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
boolean on remote repositories, and federated `members[].enabled` is a string.

Artifactory sends JSON booleans for all of them, whatever the documentation says. Provider
12.11.14 decodes every one into a Go `bool` (`local.RepositoryBaseParams`, which federated
reuses, the Docker and RPM models, and the federated `Member`), and its sample local
repository shows `"cdnRedirect": false` and `"blockPushingSchema1": true`. Echoing the
string spelling broke the provider's refresh after a create ("cannot unmarshal string into
Go struct field ... priorityResolution of type bool"), which the `terraform` CI job found
(#69). artifactweave writes booleans for every class and accepts both spellings on input, for
clients that followed the documentation. The `pkg/repo` round-trip tests cover every such
field.

## Keys known only from the Terraform provider

These keys do not appear in JFrog's JSON documentation or OpenAPI schema. They are real
REST keys because the provider reads them back after a create, so they must be supported:
`dockerTagRetention`, `pyPIRepositorySuffix`, `symbolServerUrl`, `terraformRegistryUrl`,
`terraformProvidersUrl`, `dockerProjectId`, `useNamespaces`, `hideUnauthorizedResources`,
`resolveDockerTagsByTimestamp`, `rejectInvalidJars`, `forceMetadataNameVersion`,
`forceNonDuplicateChart`. Keys present in the YAML documentation but not the JSON
documentation: `mismatchingMimeTypesOverrideList`, `queryParams`,
`virtualRetrievalCachePeriodSecs`, `chartsBaseUrl`.

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

A `null` value in YAML deletes the element. An empty YAML document deletes every
repository, which the implementation must refuse unless explicitly forced. Example:

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
