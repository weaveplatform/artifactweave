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
| local | | `bazelmodules` | `hex`, `nix`, `machinelearning`, `releasebundles` |
| remote | `terraformbackend`, `vagrant`, `bazelmodules` | `terraformbackend`, `vagrant` | `vcs`, `p2`, `hex`, `nix`, `aieditorextensions`, `jetbrainsplugins` |
| virtual | `cargo`, `cocoapods`, `opkg`, `vagrant`, `terraformbackend` | `cargo`, `vagrant`, `terraformbackend`, `huggingfaceml`, `bazelmodules` | `p2`, `hex`, `nix` |
| federated | `terraformbackend`, `pub`, `swift`, `terraform` | `terraformbackend`, `bazelmodules` | `machinelearning`, `releasebundles` |

Where the sources conflict:
- **Remote `bazelmodules`:** the JSON page leaves it out. The provider has a remote Bazel
  resource (`resource_artifactory_remote_bazel_repository.go`), and Bazel Modules remote
  repositories are in the SaaS release notes.
- **Local and virtual `bazelmodules`:** the JSON page lists them and the provider does not.
  [SaaS 7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas)
  added "local and virtual repositories for Bazel Modules", after the provider's 12.11.14,
  and the [Bazel Modules page](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories.md)
  (updated 2026-10-12) describes all three classes.
- **Virtual `cocoapods` and `opkg`:** the JSON page leaves them out and the provider offers
  them. The [YAML page](https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md)
  lists `cocoapods` and `p2` for virtual repositories.
- **Virtual `huggingfaceml`:** the JSON page lists it and the provider does not. Virtual
  Hugging Face repositories arrived in
  [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed).
- **Federated `pub`, `swift` and `terraform`:** the JSON page leaves them out and the provider
  offers them.
- **Federated `bazelmodules`:** the JSON page lists it, and nothing else does. **[I]**

Five types are newer than the JSON page's lists:
- Machine Learning, local repositories, from [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed);
  the provider's map adds federated ones;
- Hex, with virtual repositories in the same release, from [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed);
  the [Hex page](https://docs.jfrog.com/artifactory/docs/hex-repositories.md) describes local,
  remote and virtual repositories, and no source has a federated one;
- Nix, local, remote, virtual and federated, from [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed),
  and the [Nix page](https://docs.jfrog.com/artifactory/docs/nix-repositories.md) says
  "Artifactory supports federated Nix repositories". The provider has no federated Nix;
- AI Editor Extensions and JetBrains Plugins, "two new remote repository types" in the SaaS
  release notes, and remote only in the provider.

`pkg/repo.AllowedPackageTypes` accepts a type on a class when any of these sources offers it
there ([#396](https://github.com/weaveplatform/artifactweave/issues/396)). The sources lag
each other: the provider's map predates the local and virtual Bazel Modules repositories, and
the JSON page predates Hex and Nix. Refusing a type 7.x accepts fails a client's create, while
accepting one it refuses stores a configuration that no package handler serves. The sets are:

| Class | Refused |
|---|---|
| local | `vcs`, `p2`, `aieditorextensions`, `jetbrainsplugins` |
| remote | `terraformbackend`, `vagrant`, `machinelearning` |
| virtual | `cargo`, `vagrant`, `terraformbackend`, `machinelearning`, `vcs`, `aieditorextensions`, `jetbrainsplugins` |
| federated | `terraformbackend`, `vcs`, `p2`, `hex`, `aieditorextensions`, `jetbrainsplugins` |

Each class accepts every type it does not refuse, of the local list above and of `vcs`, `p2`,
`hex`, `nix`, `machinelearning`, `aieditorextensions` and `jetbrainsplugins`. `releasebundles`, which the provider's
map offers for local and federated repositories, stays refused on every class until release
bundles v1 ([decision 0023](decisions/0023-release-bundles-and-distribution.md)) settles how its
storing repository is created.

This project implements `docker`, `oci`, `helm`, `helmoci`, `npm`, `maven`, `gradle`,
`ivy`, `sbt`, `go`, `pypi`, `nuget`, `terraform` and `generic`. Every other type that
`pkg/repo/packagetypes.go` allows for the class is accepted, and its configuration is
persisted and echoed with no package handler of its own. That includes `terraformbackend`,
which would need the Terraform Enterprise state API to serve; see
[12-open-questions.md](12-open-questions.md).

## Package type names in listings

`GET /api/repositories` does not list a repository's `packageType` as its configuration
spells it. It sends the name of the type in Artifactory's `RepoType` enumeration:
`Generic` and `Maven` in JFrog's sample ([D] [getrepositoriesbytypeandproject](https://docs.jfrog.com/artifactory/reference/getrepositoriesbytypeandproject.md)),
and `Alpine` in the Terraform provider's acceptance test of its repositories data source
([S] provider 12.11.14 [`datasource_artifactory_repositories_test.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/datasource/repository/datasource_artifactory_repositories_test.go),
`repos.0.package_type = "Alpine"`). jfrog-client-go's tests read `Generic` back from the
list for local, remote and federated repositories ([S] [`tests/artifactorylocalrepository_test.go`](https://github.com/jfrog/jfrog-client-go/blob/master/tests/artifactorylocalrepository_test.go),
`getAllLocalRepoDetailsTest`). In 6.20 the list puts the enumeration constant itself in the
response ([I] `RepositoriesResource.addNonRemoteRepoDetails`, `.packageType(repoToAdd.getType())`,
where `RepoDetails.packageType` is a `RepoType`), and the constant for rpm is
`YUM("yum", "RPM", "rpm")`. The `packageType` filter goes through `RepoType.fromType`, which
reads `rpm` as `yum`, so either finds rpm repositories [I]. artifactweave matches the filter
without regard to case against the configured type and the listed name, which covers both.

Most names come from a 7.x capture of the metrics endpoint,
[`REA/metrics.txt`](https://github.com/sumanthsv-jfrog/project_works/blob/9c33a399725bbdbfb296ad875b04b7d6e0abc4ec/REA/metrics.txt),
committed on 2026-07-09 to a JFrog engineer's repository. Its `jfrt_repo_by_type_total`
series ("The number of repositories by type and package type") labels each repository with
its enumeration name. The same capture's storage-summary series, `jfrt_stg_summary_*`,
label the same rpm and npm repositories `RPM` and `npm`, which are display names. That is
how the capture shows that `YUM` and `Npm` are the enumeration names. jfrog-cli-core compares
the storage summary's `packageType` with `Maven`, `Gradle`, `Ivy`, `Docker`, `Conan`, `NuGet`
and `SBT`
([S] [`transferfiles/delayedartifactshandler.go`](https://github.com/jfrog/jfrog-cli-core/blob/master/artifactory/commands/transferfiles/delayedartifactshandler.go)).

The storage summary (`GET /api/storageinfo`) sends display names, as the metrics capture
suggests: two captures of the summary itself show npm and rpm repositories as `npm` and
`RPM`, and every other type they show as the table below spells it
([research 15, section 2.2](15-data-model.md#22-item-artifactory-artifacts-storage-api)).
`pkg/repo.PackageTypeDisplayName` holds those two and otherwise defers to
`PackageTypeName`.

`pkg/repo.PackageTypeName` holds these spellings:

| Type | Listed as | Source |
|---|---|---|
| `alpine` | `Alpine` | [S] provider data source test |
| `ansible` | `Ansible` | [I] no source names it |
| `bazelmodules` | `BazelModules` | [I] no source names it |
| `bower` | `Bower` | [I] 6.20 enumeration only |
| `buildinfo` | `BuildInfo` | capture; the jartifactory storage summary ([research 15](15-data-model.md)); jfrog-cli-core `transfer_test.go` |
| `cargo` | `Cargo` | [I] no source names it |
| `chef` | `Chef` | [I] 6.20 enumeration only |
| `cocoapods` | `CocoaPods` | [I] 6.20 enumeration only |
| `composer` | `Composer` | capture |
| `conan` | `Conan` | capture; jfrog-cli-core |
| `conda` | `Conda` | [I] 6.20 enumeration only |
| `cran` | `CRAN` | [I] 6.20 enumeration only |
| `debian` | `Debian` | capture |
| `docker` | `Docker` | capture; jfrog-cli-core |
| `gems` | `Gems` | [I] 6.20 enumeration only |
| `generic` | `Generic` | [D] list sample; [S] jfrog-client-go; capture |
| `gitlfs` | `GitLfs` | [I] 6.20 enumeration only |
| `go` | `Go` | capture |
| `gradle` | `Gradle` | capture; jfrog-cli-core |
| `helm` | `Helm` | capture |
| `helmoci` | `HelmOCI` | capture |
| `huggingfaceml` | `HuggingFaceML` | capture |
| `ivy` | `Ivy` | [S] jfrog-cli-core (storage summary); the 6.20 enumeration |
| `maven` | `Maven` | [D] list sample; capture; jfrog-cli-core |
| `npm` | `Npm` | capture |
| `nuget` | `NuGet` | capture; jfrog-cli-core |
| `oci` | `OCI` | capture |
| `opkg` | `Opkg` | [I] 6.20 enumeration only |
| `p2` | `P2` | capture |
| `pub` | `Pub` | [I] no first-party source names it |
| `puppet` | `Puppet` | [I] 6.20 enumeration only |
| `pypi` | `Pypi` | capture |
| `rpm` | `YUM` | capture; the 6.20 enumeration |
| `sbt` | `SBT` | [S] jfrog-cli-core (storage summary); the 6.20 enumeration |
| `swift` | `Swift` | [I] no source names it |
| `terraform` | `Terraform` | [I] no source names it |
| `terraformbackend` | `TerraformBackend` | capture |
| `vagrant` | `Vagrant` | [I] 6.20 enumeration only |
| `vcs` | `VCS` | capture |
| `machinelearning` | `MachineLearning` | capture |
| `hex` | `Hex` | [I] no source names it |
| `nix` | `Nix` | [I] no source names it |
| `aieditorextensions` | `AIEditorExtensions` | [I] no source names it |
| `jetbrainsplugins` | `JetBrainsPlugins` | [I] no source names it |
| `releasebundles` (not accepted) | `ReleaseBundles` | capture |
| any other | the type with its first letter capitalised | [I] |

The [I] rows are on the P19 list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

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

`repoLayoutRef` names a layout. The layout a create without one gets is in "Common fields"
(the web UI defaults to `simple-default`). Bundled layout identifiers, confirmed from the
provider's default layout map:

| Identifier | Default for |
|---|---|
| `maven-2-default` | `maven`, `gradle` |
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

`pkg/layout/bundled.go` holds each bundled definition with its source (#277, #376). They are
7.x's, in the order a 7.x descriptor lists them:

- **JFrog's own 7.38.8 export** [S] is the first source. It is a test fixture of the JFrog
  CLI: [`artifactory/commands/testdata/artifactory_export/etc/artifactory.config.latest.xml`](https://github.com/jfrog/jfrog-cli-artifactory/blob/4a0f3827cfa0/artifactory/commands/testdata/artifactory_export/etc/artifactory.config.latest.xml)
  in jfrog-cli-artifactory at `4a0f3827cfa0`, schema 3.1.29, with
  `artifactory.version=7.38.8` beside it. It defines every bundled layout but
  `gradle-default`, `ansible-default`, `sbt-ivy-default` and `nix-default`, and the four
  exported descriptors below agree with it wherever they hold the same layout.
- **`npm-default`** has been `[orgPath]/-/[module]-[baseRev](-[fileItegRev]).tgz` since 7.5.0
  ([J] [RTFACT-21456](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21456), "Change
  the default layout for NPM repositories", fixed in 7.5.0). It reads `<name>/-/<name>-<version>.tgz`
  as organisation and module `<name>`, but `[module]` stops at a slash, so it matches no
  scoped tarball, `@scope/name/-/@scope/name-<version>.tgz`. `pkg/layout` adds an optional
  `([scope<@[^/]+>]/)` group before `[module]`, which reads one as module `name` of
  organisation `@scope/name` and changes nothing else the layout reads or builds (DV-100).
  `[module]` is greedy, so `lodash-5.0.0-rc.1.tgz` reads as module `lodash-5.0.0` and base
  revision `rc.1`, in 7.x and here.
- **`go-default`** is `[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]` on a fresh
  7.x instance; one upgraded from 6.x keeps 6.20's `[orgPath]/[module]/@v/v[refs].zip`
  ([J] [RTFACT-31118](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31118), a
  customer's report, no JFrog reply). It reads every file the Go format stores: release,
  pre-release, `+incompatible` and pseudo-versions ([research 26 section 4.2](26-go-modules.md#42-storage-and-the-go-default-layout)).
- **`conan-default`** has no `?` after its optional package group in any 7.x source, so it
  matches recipe and package files. 6.20's descriptor wrote the `?`, which its parser reads
  as a literal, so that layout matched no Conan file; a customer's 6.10.1 configuration on
  [RTFACT-19290](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-19290) has it too.
- **The Terraform, Swift and Ansible layouts** are 7.x's, and use tokens outside the
  documented set: `[namespace]`, `[module-name]`, `[provider]`, `[provider-name]`,
  `[version]`, `[os]`, `[arch]`, `[scope]` and `[name]`. A bundled layout drops each, as
  Artifactory's parser does when it matches a path (`RepoLayoutUtils.generateRegExpFromPattern`
  in 6.20 [S]), so these four match no path, and layout-based searches and layout
  translation find nothing in their repositories [I]. `ansible-default` comes from the 3.4.x
  exports only; JFrog's 7.38.8 export predates it.
- **`sbt-ivy-default`** is the Maven pattern with the Ivy descriptor
  `ivy-[baseRev](-[fileItegRev])(-[classifier]).xml` in the revision folder and `\d{14}`
  integration revisions, from the 3.4.5 and 3.4.8 exports only [I].
- **`gradle-default`** keeps 6.20's definition and place, third. JFrog's repository-layouts
  page still lists it as bundled (updated 2026-09-09: "Gradle (Wharf cache default layout)"),
  and the scala export, from an instance upgraded from 6.x, keeps it. JFrog's 7.38.8 export
  has none: [RTFACT-21231](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21231),
  "Deprecate the current gradle-default layout, and switch to using maven-2-default by
  default", is done but carries no fix version [I].
- **`nix-default`** is named only by the Terraform provider's default map and is in no 7.x
  source's list, so it is reconstructed as `simple-default`'s pattern [I] (DV-055) and comes
  last.
- **The other layouts** match 6.20's descriptor
  (`base/config/src/main/resources/META-INF/default/artifactory.config.xml:112-221` in the
  `knottnhupfer/artifactory-oss` mirror at `b99e5649bcad`) as well as the 7.x sources.
  Maven 1 uses `.+` for both integration revisions and every later non-JVM layout `.*`.
- **Order.** The 7.x sources list `cargo-default` right after `simple-default`, then
  `composer-default` to `build-default` in 6.20's order, then the Terraform, Swift and
  Ansible layouts, and `sbt-ivy-default` last. `pkg/layout` keeps that order, with
  `gradle-default` third and `nix-default` after them all. It matters once
  `GET /api/system/configuration` serves the descriptor (P12).
- **`cargo-default`** is `crates/[module]/[module]-[baseRev].[ext]` with `.*` integration
  revisions (#354), in every 7.x source. It describes the one path JFrog's
  [Cargo page](https://docs.jfrog.com/artifactory/docs/cargo-repositories.md) accepts,
  `crates/<name>/<name>-<version>.crate`; a crate stored anywhere else is not indexed. The
  provider's default for a `cargo` repository is still `simple-default`.
- **"npm Test" is not bundled** [I]. The layouts page lists it among the default layouts,
  but its only trace in JFrog's documentation is the same page's screenshot of the layout
  list, which shows a layout named `npm-test-layout` beside `npm-default`. Every bundled
  layout is named `<type>-default`, no 7.x source has it, and the provider's map does not
  name it, so it reads as a custom layout on the instance the screenshot came from. On the
  P19 list.

**Module identification.** JFrog's repository-layouts page (updated 2026-09-09): "At a
minimum, there are three mandatory fields required for module identification:
Organization, Module, Base Revision" [D], as 6.20's `ModuleInfo.isValid` requires [S]. So
`pkg/layout` reads coordinates only from a path that gives all three, and a layout without
`[org]` or `[orgPath]`, such as `cargo-default`, reads none: layout-based searches find no
module in it.

**Unknown tokens.** A layout the descriptor stores may use only the documented tokens and
custom `[name<regex>]` ones; any other token is refused with 400. JFrog's layout editor
reports such a token as an error ([J] [RTFACT-4395](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-4395),
fixed in 2.4.0; [RTFACT-8199](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-8199),
fixed in 4.6.0, a 400 from the layout test), and a 2024 report says 7.x will not let a user
create a layout with the Terraform and Swift layouts' tokens
([RTFACT-30300](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30300)). Whether 7.x
also refuses one through `PATCH /api/system/configuration` is [I], so a YAML `repoLayouts`
entry copied from an export with such tokens is refused here [I].

#### Exported 7.x descriptors

Four configuration descriptors exported from running 7.x instances are published in public
repositories. They are an instance's `artifactory.config.latest.xml`, or an import made from
one, not JFrog documentation, so what they show alone is [I]:

| Descriptor | Schema |
|---|---|
| [octopusden/octopus-dms-service](https://github.com/octopusden/octopus-dms-service/blob/9500dcb54b52/test-common/src/main/artifactory/dump/20260120.084302/etc/artifactory.config.latest.xml), dumped 2026-01-20 | 3.4.8 |
| [devarsh10/jfrog-artifactory](https://github.com/devarsh10/jfrog-artifactory/blob/1f7393f59ef0/artifactory/var/etc/artifactory/artifactory.config.latest.xml) | 3.4.5 |
| [scala/community-build](https://github.com/scala/community-build/blob/0a9bdc4e9f61/artifactory.xml) | 3.4.2 |
| [Panda-Academy-Workshops/Infrastructure](https://github.com/Panda-Academy-Workshops/Infrastructure/blob/bf56ad425899/artifactory/artifactory.config.import.xml) | 3.1.26 |

What each holds, beside JFrog's 7.38.8 export (3.1.29) and `pkg/layout`:

| Layout | JFrog's 7.38.8 export | The four exports | `pkg/layout` |
|---|---|---|---|
| `npm-default` | `[orgPath]/-/[module]-[baseRev](-[fileItegRev]).tgz` | the same, in all four | the same with `([scope<@[^/]+>]/)` before `[module]` (DV-100) |
| `go-default` | `[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]` | the same in three; the scala one keeps 6.20's `v[refs].zip` | the same |
| `conan-default` | without 6.20's `?` | the same, in all four | the same |
| `terraform-module-default` | `[namespace]/[module-name]/[provider]/[version].[ext]` | the same, in all four | the same |
| `terraform-provider-default` | `[namespace]/[provider-name]/[version]/terraform-provider-[provider-name]_[version]_[os]_[arch].[ext]` | the same, in all four | the same |
| `swift-default` | `[scope]/[name]/[name]-[version].zip` | the same in three; the oldest has `[scope]/[name]/[version].zip` | the same |
| `ansible-default` | none | `collections/[namespace]/[name]/[version]/[namespace]-[name]-[version].tar.gz`, in the three 3.4.x ones | the same |
| `sbt-ivy-default` | none | in the two newest | the same |
| `gradle-default` | none | in the scala one only | 6.20's |
| `nix-default` | none | in none | `simple-default`'s pattern (DV-055) |

Artifactory turns a pattern into an expression in `RepoLayoutUtils.generateRegExpFromPattern`
[S]: `[module]` is greedy (`[^/]+`), so where it appears only in a file name, as in
`maven-1-default` and `build-default`, `commons-lang-2.1.jar` is module `commons-lang`; a `?`
outside a token is escaped as a literal; a repeated token is a back-reference to its first
value; and a token that is neither documented nor custom adds nothing. `pkg/layout` matches
all four, the last for bundled layouts only.

What the bundled layouts do not describe, shown by real paths in
`pkg/layout/bundled_test.go` [S]:

| Layout | Path it does not match | Why |
|---|---|---|
| `ivy-default` | `org.gradle.sample/project1/1.0/jars/project1.jar` | Ivy's own default pattern has no revision in the file name |
| `gradle-default` | `org.gradle.sample/project1/1.0/ivy-1.0.xml` | Gradle's ivy-publish writes the descriptor in the revision folder; the layout expects it beside the module folders, as in Gradle's old Wharf cache |
| `sbt-default` | `org.example/lib_2.13/1.0/jars/lib_2.13.jar` | the slashes after the optional `scala_` and `sbt_` groups are mandatory, so only plugin paths match; JFrog's sbt page says the layout does not support cleanup |
| `sbt-ivy-default` | `org.example/lib_2.13/1.0/jars/lib_2.13.jar` | sbt's Ivy-style pattern is not 7.x's |
| `terraform-module-default`, `terraform-provider-default`, `swift-default`, `ansible-default` | any | their unknown tokens are dropped |

**Existing repositories.** A repository names its layout, so a changed definition applies to
it at once. Neither the npm nor the Go format reads a layout to find its files, so nothing
they store depends on the old definitions; a file a layout-translating copy wrote with one
stays where it was written.

## Common fields

| Field | Default | Notes |
|---|---|---|
| `key` | | Mandatory on create; the URL path segment wins over the body. Follows the key rules below. |
| `rclass` | | Mandatory. |
| `packageType` | `generic` | |
| `projectKey` | | JFrog marks `environments` "mandatory when assigning repo to project" ([D] configuration-json-files); artifactweave stores `["DEV"]` when it is missing ([decision 0013](decisions/0013-projects-and-environments.md)). The web UI prepends the project key to the key of a repository created inside a project ([D] [federated form reference](https://docs.jfrog.com/artifactory/docs/federated-repository-form-field-reference.md)); artifactweave does not check the prefix. |
| `environments` | | Array; `DEV` and `PROD` are built in. |
| `description`, `notes` | `""` | Every read sends both, as `""` when unset ([D] the samples of [getrepositoryconfiguration](https://docs.jfrog.com/artifactory/reference/getrepositoryconfiguration.md), [getbatchofrepositoriesbyname](https://docs.jfrog.com/artifactory/reference/getbatchofrepositoriesbyname.md) and [getrepositoryconfigurationv2](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md)). A read by anyone but an administrator shows `notes` as `""`. |
| `includesPattern` | `**/*` | An empty string also means everything. |
| `excludesPattern` | `""` | |
| `repoLayoutRef` | `sbt-default` for `sbt`, `ivy-default` for `ivy`, `maven-2-default` for every other type | Applied when a create leaves it out ([#396](https://github.com/weaveplatform/artifactweave/issues/396)). `sbt-default` "is automatically applied when creating a repository without specifying a layout" ([D] [sbt-repositories](https://docs.jfrog.com/artifactory/docs/sbt-repositories.md), updated 2026-01-05); "Gradle repositories use the `maven-2-default` repository layout" ([D] [gradle-repositories](https://docs.jfrog.com/artifactory/docs/gradle-repositories.md), updated 2026-10-18); `maven-2-default` is the JSON page's default and the layout of the `genericLocalV2` sample. JFrog has no Ivy page; `ivy-default` is the provider map's layout for Ivy, by analogy with sbt **[I]**. The provider sends its map's layout for every type, so a repository it creates has that layout instead. |
| `blackedOut` | `false` | |
| `xrayIndex` | `false` | Persisted and echoed; no scanner behind it. |
| `propertySets` | `[]` | Names of property sets. An administrator's read of a local, remote or federated repository sends none as `[]` ([D] the `genericLocalV2` sample and the [getallrepositoryconfigurations](https://docs.jfrog.com/artifactory/reference/getallrepositoryconfigurations.md) sample). |
| `priorityResolution` | `false` | |
| `downloadRedirect` | `false` | |
| `cdnRedirect` | `false` | JFrog Cloud Enterprise+ only. |
| `primaryKeyPairRef`, `secondaryKeyPairRef` | | Signing key pairs (Helm, Debian, Terraform providers, and npm's ECDSA package signing, [research 25](25-npm.md#5-storage-layout-and-properties)). |
| `signedUrlTtl`, `xrayDataTtl` | | Read-only in responses. |

### Key rules

JFrog's "Repository Naming Rules and Limitations" ([D] [repository-management-overview](https://docs.jfrog.com/artifactory/docs/repository-management-overview.md),
updated 2026-09-10) and the Terraform provider's key validator ([S] terraform-provider-shared@adad42471470
[`repo_key.go`](https://github.com/jfrog/terraform-provider-shared/blob/adad42471470/validator/fw/string/repo_key.go))
set these rules. Keys are also unique without regard to case.

- **Length.** At most 64 characters for local, federated and virtual repositories, 58 for
  remote ones and 63 for Docker, OCI and Helm OCI local and federated ones ([D] the
  overview's table). The provider checks "1 - 64", and the federated form reference says
  "Must be 64 characters or fewer" ([D] [federated form reference](https://docs.jfrog.com/artifactory/docs/federated-repository-form-field-reference.md)).
  A remote's 58 leaves room for the `-cache` suffix of its cache, as 6.20's validator
  computed it, and 63 is the hard limit the Docker page gives for keys used as host names
  under the subdomain method ([D] [docker-repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories.md),
  "Docker Limitations in Artifactory"). Before 6.7.0 a longer key broke the database
  ([J] [RTFACT-18090](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-18090)).
- **Characters.** The overview forbids `/ \ : | ? * " < >`, the class pages forbid
  "spaces or special characters" ([D] [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md),
  with the same text on the remote and virtual pages), and the provider refuses a space and
  `! @ # $ % ^ & * ( ) + = { } [ ] : ; < > , / ? ~ | \` and the backtick. The lists do not
  conflict: the provider spells out the special characters and leaves out only the double
  quote, so a key may contain neither list's characters. artifactweave refuses the union,
  and white space of every kind as spaces.
- **Reserved names.** A key cannot be `.`, `..`, `&`, `Jfrog-usage-logs`,
  `Jfrog-billing-logs`, `Jfrog-logs`, `artifactory-build-info`, `artifactory-pipe-info`,
  `Auto-trashcan`, `jfrog-support-bundle`, `_intransit`, `Artifactory-edge-uploads` or
  `release-bundles`, cannot start with `Jfrog-system-reserved` or
  `Jfrog-artifactory-system`, and cannot end with `-cache`. The list capitalises some
  entries as list items, where the repositories Artifactory creates are lower case
  (`auto-trashcan`), and keys are unique without regard to case, so artifactweave compares
  the names and prefixes without case, as 6.20's validator compared its reserved names.
- **Docker, OCI and Helm OCI.** These keys "cannot contain uppercase letters or an
  underscore" (the overview), on every class. [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed)
  fixed the web UI, which had accepted an upper-case OCI key that the REST API then refused
  (RTDEV-41390).
- **Leading digit.** Allowed. The class pages still say a key "cannot begin with a number",
  but 7.53.1 lifted that rule: "Artifactory repositories can now have a name that starts
  with a numeric character" (Artifactory 7.53.1 Cloud release notes, RTFACT-27514, no
  longer on docs.jfrog.com; [J] [RTFACT-27514](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27514)
  "Allow repo name to start with numeric character", Done, and
  [RTFACT-25604](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-25604), "Implemented
  in Artifactory 7.53.x"). The overview's rules, the newest list, have no such rule.

`pkg/repo` applies the rules to every key a client chooses: on create, batch create and a
repository the configuration descriptor adds. An update never changes a key: a body `key`
that differs from the stored one in any way, letter case included, answers 400 "The
repository key cannot be changed" ([#472](https://github.com/weaveplatform/artifactweave/issues/472)).
An update therefore does not check the key rules again, so a
repository the server creates under a reserved name, such as `artifactory-build-info`, is not
refused for its name, and that repository keeps its package type `buildinfo`, which no class
lets a client choose. No 7.x error text is documented; artifactweave answers 400 with messages
of its own, in the order of 6.20's `RepositoryNameValidator` **[I]**.

## Local fields

| Field | Default | Notes |
|---|---|---|
| `checksumPolicyType` | `client-checksums` | Maven family and npm. |
| `handleReleases`, `handleSnapshots` | `true` | Maven family and npm, where a pre-release version counts as a snapshot (#481). |
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
| `yumGroupFileNames`, `ddebSupported`, `enableConanSupport`, `cargoAnonymousAccess`, `cargoInternalIndex` | | RPM, Debian, Conan and Cargo keys the clients send ("Keys known only from the Terraform provider"); accepted and persisted. |
| `terraformType` | | `MODULE` or `PROVIDER`. Local and federated repositories. |
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
| `retrievalCachePeriodSecs` | `7200`; `21600` for `docker`, `oci`, `helmoci` | How long non-unique metadata is served from cache. At least 30, and at least 14,400 for a NuGet remote that proxies `community.chocolatey.org` ([D] [remote-repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories.md), updated 2026-10-19). Smart remote repositories are exempt from the 30 ([D] [SaaS 7.144.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71442-saas) notice). SaaS enforces the 30 from [7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas), and the 14,400 from [SaaS 7.153.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71532-saas) and [self-managed 7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed). `pkg/repo.Validate` refuses a smaller value with 400 on create and on every update, since the page keeps an existing shorter period only "until you edit" the repository ([#396](https://github.com/weaveplatform/artifactweave/issues/396)). A smart remote is one with `contentSynchronisation.enabled`, which the provider describes as proxying "a local or remote repository from another instance of Artifactory" ([S] provider 12.11.14 `remote.go`). **[I]** Whether self-managed 7.161.32 enforces the 30 (its notes do not list it), the 400 message, whether a host under `chocolatey.org` other than `community.chocolatey.org` counts, and whether an update that leaves an existing shorter period alone is refused. |
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
| `listRemoteFolderItems` | `false` | Documented as a string, accepted in either spelling and written as a boolean; see "Typing traps". |
| `queryParams` | `""` | Appended to upstream requests. |
| `propagateQueryParams`, `retrieveSha256FromServer` | | Generic remotes; keys the clients send, accepted and persisted. |
| `curated`, `passThrough` | | Curation flags the provider sends on its AI Editor Extensions, Conan, Conda, Debian, Docker, Gems, Go, Gradle, Hugging Face, Maven, npm, NuGet and PyPI remotes; accepted and persisted, with no curation behind them. |
| `cargoAnonymousAccess`, `cargoInternalIndex` | | Cargo remotes, as on local repositories. |
| `podsSpecsRepoUrl` | | CocoaPods remotes; `cocoaPods.cocoaPodsSpecsRepoUrl` in the YAML form. |
| `hexPublicKey` | | Hex remotes: the upstream registry's public key. |
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
| `terraformRegistryUrl`, `terraformProvidersUrl` | `https://registry.terraform.io`, `https://releases.hashicorp.com` | Applied to `terraform` remotes ([S] provider 12.11.14 `resource_artifactory_remote_terraform_repository.go` defaults). |
| `vcsType`, `vcsGitProvider`, `vcsGitDownloadUrl` | `GIT`, `GITHUB`, `""` | Go VCS remotes (out of scope initially). |
| `dockerProjectId` | | Google Artifact Registry upstreams. |
| `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl` | `https://registry.bower.io`, `https://github.com/rust-lang/crates.io-index`, `https://packagist.org` | Out-of-scope ecosystems; accepted and persisted. Defaults from [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md) [D], applied to `bower`, `cargo` and `composer` remotes respectively; `gitRegistryUrl` is Cargo's index (jfrog-client-go puts it on Cargo remotes only). The page lists the three among every remote's fields; that Artifactory fills them only for their own type is **[I]**. |

## Virtual fields

| Field | Default | Notes |
|---|---|---|
| `repositories` | `[]` | Ordered member keys; local, remote or virtual. Optional ([D] configuration-json-files; the provider's `virtual.go` makes it optional too), and a read lists none as `[]`. From [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) (RTDEV-62756) a member must have the virtual's package type, unless the virtual is `generic`; Maven, Gradle, Ivy, SBT and P2 count as one type **[I]** ("Settled since the audit"). |
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
| `p2Urls` | | P2 virtual repositories: the update sites aggregated; `p2.urls` in the YAML form. Accepted and persisted. |

## Federated fields

Everything a local repository has, `terraformType` included, plus `members`, an array of
`{"url","enabled"}` where `enabled` is a boolean (default `true`; documented as a string, see
below) and `url` may be derived from the base URL and the key, and `proxy` and `disableProxy`.
The provider's federated Terraform resource sends `terraformType` with `rclass` federated
([S] provider 12.11.14 [`resource_artifactory_federated_terraform_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/federated/resource_artifactory_federated_terraform_repository.go)).

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
clients that followed the documentation (DV-027). The `pkg/repo` round-trip tests cover every
such field. `listRemoteFolderItems` is one of them: jfrog-client-go and the provider decode it
as a boolean too (`remoterepository.go`, `remote.go`), and `pkg/repo` reads the documented
`"false"` as well ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).

## Keys known only from the Terraform provider

These keys do not appear in JFrog's JSON documentation or OpenAPI schema. They are real
REST keys because the provider reads them back after a create, so they must be supported:
`dockerTagRetention`, `pyPIRepositorySuffix`, `symbolServerUrl`, `terraformRegistryUrl`,
`terraformProvidersUrl`, `dockerProjectId`, `useNamespaces`, `hideUnauthorizedResources`,
`resolveDockerTagsByTimestamp`, `rejectInvalidJars`, `forceMetadataNameVersion`,
`forceNonDuplicateChart`. Keys present in the YAML documentation but not the JSON
documentation: `mismatchingMimeTypesOverrideList`, `queryParams`,
`virtualRetrievalCachePeriodSecs`, `chartsBaseUrl`.

The JFrog clients send more keys of this kind [S], and `pkg/repo` keeps each of them, so a
plan that sets one converges ([#396](https://github.com/weaveplatform/artifactweave/issues/396)):
- `curated` and `passThrough` on the remote repositories that embed the provider's curation
  model (provider 12.11.14, [`remote/curation.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/curation.go));
- `propagateQueryParams` and `retrieveSha256FromServer` on remote generic repositories
  (provider 12.11.14, [`resource_artifactory_remote_generic_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/resource_artifactory_remote_generic_repository.go));
- `cargoAnonymousAccess`, `yumGroupFileNames`, virtual `p2Urls` and remote
  `podsSpecsRepoUrl` (jfrog-client-go, [`artifactory/services`](https://github.com/jfrog/jfrog-client-go/tree/master/artifactory/services));
- `cargoInternalIndex` on local and remote Cargo repositories, `ddebSupported` on local Debian
  repositories, `enableConanSupport` on Conan repositories and `hexPublicKey` on remote Hex
  repositories (provider 12.11.14, the Cargo, Debian, Hex resources and `conan.go`). A
  comparison of every JSON key the provider's repository models and jfrog-client-go's
  services send with `pkg/repo.Config` finds no other.

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

artifactweave reads each YAML key in the table into its JSON field, so the descriptor and
the repositories API store the same value in the same place (`internal/descriptor/parse.go`):

- `downloadRedirect` takes the `{enabled: <bool>}` block or the flat flag the JSON API
  uses.
- `remoteRepoLayout` is stored as `remoteRepoLayoutRef`, although RTFACT-29858 reports 7.49.8
  leaving the field unchanged. **[I]** No source shows a current 7.x setting the field; the
  report was closed without a fix or a reason.
- `primaryKeyPairDescriptor` is stored as `primaryKeyPairRef`, which the local sample's
  `secondaryKeyPairRef` beside it and the federated sample's `primaryKeyPairRef` suggest.
  **[I]** The page gives the key with no description.
- `type: terraformbe` is stored as `packageType: terraformbackend`.
- `yumGroupFileNames`, `cocoaPods.cocoaPodsSpecsRepoUrl` and `p2.urls` are stored as
  `yumGroupFileNames`, `podsSpecsRepoUrl` and `p2Urls`, where the repositories API stores them
  ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- `cocoaPods.specRepoProvider{provider, downloadUrl}`, in the remote sample, has no JSON
  field: the repositories API reads a CocoaPods remote's Git provider from
  `vcsGitProvider` and `vcsGitDownloadUrl`, which the YAML writes under `vcs.git`
  ([S] provider 12.11.14
  [`resource_artifactory_remote_cocoapods_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/remote/resource_artifactory_remote_cocoapods_repository.go)).
  It is accepted and not stored. **[I]** The 6.20 `HttpRepositoryConfigurationImpl` has
  no JSON field for it either, and no 7.x source shows one.

A `null` value in YAML deletes the element. Inside a repository element, only string fields
other than `type` may be set to `null`
([D] [delete an element](https://docs.jfrog.com/installation/docs/delete-an-existing-element-in-artifactory-yaml.md)).
artifactweave clears a string field set to `null`, counting a field with a fixed set of
values such as `checksumPolicyType` as a string, and answers 400 for `null` on `type`, on a
block and on any field that does not hold a string ([research
06](06-system-configuration-and-descriptor.md#what-artifactweave-serves)). The repositories
API's update, by contrast, reads `null` on any field as a reset to its default
(`internal/repos/repos.go`, `Merge`). An empty YAML document
deletes every repository, which artifactweave refuses with 400 (DV-023). Example:

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
| npm | `checksumPolicyType`, `handleReleases`, `handleSnapshots` (local and federated) | | `externalDependenciesEnabled`, `externalDependenciesPatterns`, `externalDependenciesRemoteRepo`, `virtualRetrievalCachePeriodSecs` |
| maven, gradle, ivy, sbt | `handleReleases`, `handleSnapshots`, `snapshotVersionBehavior`, `checksumPolicyType`, `maxUniqueSnapshots`, `suppressPomConsistencyChecks` | same plus `remoteRepoChecksumPolicyType`, `fetchJarsEagerly`, `fetchSourcesEagerly`, `rejectInvalidJars`, `listRemoteFolderItems` | `pomRepositoryReferencesCleanupPolicy`, `keyPair`, `forceMavenAuthentication` |
| pypi | | `pyPIRegistryUrl`, `pyPIRepositorySuffix` | |
| nuget | `forceNugetAuthentication`, `maxUniqueSnapshots` | `feedContextPath`, `downloadContextPath`, `v3FeedUrl`, `symbolServerUrl`, `forceNugetAuthentication` | `forceNugetAuthentication` |
| go | | `vcsGitProvider`, `vcsGitDownloadUrl`, `missedRetrievalCachePeriodSecs` 600 | `externalDependenciesEnabled`, `externalDependenciesPatterns` |
| helm | `forceMetadataNameVersion`, `forceNonDuplicateChart` | `chartsBaseUrl`, `externalDependenciesEnabled`, `externalDependenciesPatterns` | `virtualRetrievalCachePeriodSecs`, `useNamespaces` |
| terraform | `terraformType` | `terraformRegistryUrl`, `terraformProvidersUrl` | |
| generic | | `propagateQueryParams`, `retrieveSha256FromServer` | |
| cargo | `cargoAnonymousAccess`, `cargoInternalIndex` | `gitRegistryUrl`, `cargoAnonymousAccess`, `cargoInternalIndex` | |
| rpm | `calculateYumMetadata`, `yumRootDepth`, `enableFileListsIndexing`, `yumGroupFileNames` | | |
| debian | `debianTrivialLayout`, `optionalIndexCompressionFormats`, `ddebSupported` | | `debianDefaultArchitectures` |
| conan | `forceConanAuthentication`, `enableConanSupport` | `forceConanAuthentication`, `enableConanSupport` | |
| p2 | | `forceP2Authentication` | `p2Urls` |
| cocoapods | | `podsSpecsRepoUrl` | |
| hex | | `hexPublicKey` | |
| bower, composer | | `bowerRegistryUrl`, `composerRegistryUrl` | |

`pkg/repo.Relevant` keeps these groups for their package types and drops them for the others
(the v2 read and the configurations list, "Field sets of the reads"). A field several
ecosystems share, such as `curated`, `passThrough` or the `externalDependencies` fields, is
kept for every type. `maxUniqueSnapshots` belongs to NuGet as well as the Maven family, as the
table has it and 6.20's `@IncludeTypeSpecific` does **[I]**, so a NuGet repository's v2 read
keeps it. A local or federated npm repository applies the checksum policy and the release and
snapshot handling to what is published to it ([KB] 000006800; [J]
[RTFACT-14280](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-14280), the checksum policy
for npm publishes, Done; [#481](https://github.com/weaveplatform/artifactweave/pull/481) for
the npm format), so its v2 read keeps `checksumPolicyType`, `handleReleases` and
`handleSnapshots`; an npm remote or virtual repository's does not
([#457](https://github.com/weaveplatform/artifactweave/issues/457)).

## Field sets of the reads

Each read of a repository's configuration sends one of these field sets:

| Read | Caller | Fields |
|---|---|---|
| `GET /api/repositories/{key}` | an administrator | every stored field, with the remote password and sensitive header values masked (`pkg/repo.Masked`); no `environments`, no `propertySets` (except on a virtual repository) and a virtual repository's no `repositories` as `[]` |
| `GET /api/repositories/{key}` | anyone else who can read the repository | `key`, `rclass`, `packageType`, `description`, `notes` as `""`, `repoLayoutRef`, `includesPattern` and `excludesPattern` (`pkg/repo.Public`) |
| `GET /api/v2/repositories/{key}` | as above | the same set, without the fields of other package types (`pkg/repo.Relevant`, "Per-ecosystem summary") |
| `GET /api/v2/repositories/batch` | as above | each named repository as `GET /api/repositories/{key}` gives it to the caller |
| `GET /api/repositories/configurations` | administrators only | each repository as the v2 read gives it to an administrator, grouped by class ([#457](https://github.com/weaveplatform/artifactweave/issues/457)) |
| `GET /api/repositories` | anyone, for the repositories they can see | `key`, `type` (the class upper case), `packageType` (the listing name, "Package type names in listings"), `url`, and `description` when it is not empty |
| `GET /api/repositories/{key}/configuration` | an administrator, or anyone for a remote with `shareConfiguration` | the administrator's v1 set, or for anyone else `key`, `rclass`, `packageType`, `description`, `url`, `repoLayoutRef`, `remoteRepoLayoutRef`, `includesPattern` and `excludesPattern` |

The configurations list follows JFrog's sample of a generic local repository on that endpoint,
which has the v2 read's fields and no `checksumPolicyType`, `handleReleases` or other Maven field
([D] [getallrepositoryconfigurations](https://docs.jfrog.com/artifactory/reference/getallrepositoryconfigurations.md),
updated 2026-05-28). The sample also has `signedUrlTtl`, `xrayDataTtl` and a `projectKey`, which
artifactweave sends only when they are set. The batch read's sample, two Docker local
repositories, has only `key`, `packageType`, `description`, `notes`, `includesPattern`,
`excludesPattern` and `rclass` ([D] [getbatchofrepositoriesbyname](https://docs.jfrog.com/artifactory/reference/getbatchofrepositoriesbyname.md));
whether an administrator's batch read is that short is **[I]**.

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
- `GET /api/repositories` lists `packageType` as the `RepoType` name, such as `Generic`,
  `Maven`, `Alpine` and `YUM` for rpm ("Package type names in listings").
- Reads send an empty `description` and `notes` as `""`, and an administrator's read sends
  no `propertySets` as `[]` ("Common fields").
- The v2 read of a generic local repository has no `checksumPolicyType` and no
  `forceMetadataNameVersion` ([D] [getrepositoryconfigurationv2](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md),
  sample `genericLocalV2`; the [getallrepositoryconfigurations](https://docs.jfrog.com/artifactory/reference/getallrepositoryconfigurations.md)
  sample of a generic local repository has no `checksumPolicyType` either). `pkg/repo.Relevant`
  keeps `checksumPolicyType` for the Maven family and local and federated npm repositories, and
  `forceMetadataNameVersion` for Helm.

**Changed or added in 7.x:**
- **A key may start with a digit** from 7.53.1 (RTFACT-27514), though the class pages still
  forbid it ("Key rules").
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
- **Local and virtual Bazel Modules repositories** from [SaaS 7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas),
  found by #396 after the audit; no self-managed release up to 7.161.32 lists them ("Package
  types").

Settled since the audit:
- Each class accepts every package type that a 7.x source offers for it: the JSON page, the provider's default layout map, a release note or the type's own page. That adds remote `bazelmodules`, virtual `cocoapods`, `opkg` and `p2`, federated `swift`, and `hex`, `nix`, `machinelearning`, `aieditorextensions` and `jetbrainsplugins` on their classes, all stored and echoed with no package handler ("Package types"; [S] provider map, [D] release notes 7.104.5, 7.111.4, 7.146.7 and SaaS 7.173.4) ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- A federated repository accepts `terraformType` ([S] provider 12.11.14 [`resource_artifactory_federated_terraform_repository.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/federated/resource_artifactory_federated_terraform_repository.go)); a remote or virtual one still answers 400 ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- Remote `listRemoteFolderItems` is read in the documented string spelling `"false"` as well as a boolean, and written as a boolean ([D] configuration-json-files; "Typing traps", DV-027) ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- `pkg/repo` keeps `curated`, `passThrough`, `propagateQueryParams`, `retrieveSha256FromServer`, `cargoAnonymousAccess`, `cargoInternalIndex`, `yumGroupFileNames`, `ddebSupported`, `enableConanSupport`, `p2Urls`, `podsSpecsRepoUrl` and `hexPublicKey`, which the clients send and read back ([S], "Keys known only from the Terraform provider"), so a plan that sets one converges ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- `bowerRegistryUrl`, `gitRegistryUrl`, `composerRegistryUrl`, `terraformRegistryUrl` and `terraformProvidersUrl` take the defaults in "Remote fields" on remotes of their type ([D] configuration-json-files; [S] provider remote Terraform resource) ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- `retrievalCachePeriodSecs` is at least 30 seconds on a remote repository other than a smart remote, and at least 14,400 on a NuGet remote of chocolatey.org ([D] [remote-repositories](https://docs.jfrog.com/artifactory/docs/remote-repositories.md); [D] [SaaS 7.173.4](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71734-saas), SaaS 7.144.2 and 7.153.2, self-managed 7.161.15) ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- A repository created without `repoLayoutRef` gets `sbt-default` for sbt, `ivy-default` for Ivy and `maven-2-default` for every other type ("Common fields") ([#396](https://github.com/weaveplatform/artifactweave/issues/396)).
- `GET /api/repositories/configurations` lists each repository with the v2 read's fields for its package type, so a generic local repository has no `checksumPolicyType` or `handleReleases`, as JFrog's sample shows ([D] [getallrepositoryconfigurations](https://docs.jfrog.com/artifactory/reference/getallrepositoryconfigurations.md)). The v2 read of a NuGet repository keeps `maxUniqueSnapshots`, and that of a local or federated npm repository `checksumPolicyType`, `handleReleases` and `handleSnapshots` **[I]** ("Per-ecosystem summary", "Field sets of the reads") ([#457](https://github.com/weaveplatform/artifactweave/issues/457)).
- `repositories` is optional: a virtual repository may have no members, sent as an absent or an empty list, and it still takes updates after its last member is deleted. The evidence is [D] [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md), which marks `repositories` optional; [S] provider 12.11.14 [`virtual.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/repository/virtual/virtual.go), which makes it optional with a default of `[]` and leaves it out of the request when it is empty (`omitempty`), and whose `TestAccVirtualRepository_basic` creates a virtual repository with `repositories = []` against live Artifactory and checks that `repositories.#` is 0; and [J] [RTFACT-12840](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-12840) and [RTFACT-14101](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-14101), virtual repositories without members in 4.13 and 5.2. The create page's OpenAPI lists `repositories` as required, which the provider's empty list contradicts. A read lists no members as `[]`: the provider reads an absent key as null against its `[]` default **[I]**. `defaultDeploymentRepo` must name a member, so a virtual repository without members has none. It answers as an empty repository: downloads find nothing, listings and searches are empty, and a deploy answers 405 for want of a deployment repository ([#394](https://github.com/weaveplatform/artifactweave/issues/394)).
- From [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) (SaaS 7.126.0, RTDEV-62756) the repositories API refuses a member, local, remote or virtual, whose package type does not match a non-generic virtual repository's. A generic virtual repository aggregates any type ([D] [virtual-repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories.md), "Generic Virtual Repositories"), and Helm and Helm OCI do not mix ([D] [kubernetes-helm-chart-repositories](https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories.md), "Repository Compatibility"). Maven, Gradle, Ivy, SBT and P2 count as one type, as 6.20's virtual repository editor offers them together (`GetAvailableRepositories`, `RepoType.isMavenGroup` in `knottnhupfer/artifactory-oss` at `b99e5649bcad`) and [J] [RTFACT-10202](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-10202) shows a Maven virtual repository with a Gradle member **[I]**. artifactweave refuses a mismatch with 400 on create, update, both batch operations and the configuration descriptor. JFrog documents no message, so artifactweave's is its own **[I]**, and the release note names only the create API, so the other write paths are **[I]** too. A repository's package type cannot change, and deleting a repository takes it out of every virtual repository, so the rule still holds after the write ([#394](https://github.com/weaveplatform/artifactweave/issues/394)).
- A key follows the rules in "Key rules": a length for each class (64, 58 for remote, 63 for Docker, OCI and Helm OCI local and federated), none of the documented or provider-refused characters, no reserved name or prefix in any case, no `-cache` suffix, and lower case without `_` for Docker, OCI and Helm OCI. The evidence is [D] [repository-management-overview](https://docs.jfrog.com/artifactory/docs/repository-management-overview.md) and [S] terraform-provider-shared [`repo_key.go`](https://github.com/jfrog/terraform-provider-shared/blob/adad42471470/validator/fw/string/repo_key.go). artifactweave refuses a key that breaks them with 400 on create, batch create and the configuration descriptor, and accepts a leading digit, as 7.x has since 7.53.1 ([#395](https://github.com/weaveplatform/artifactweave/issues/395)).
- An update never changes a key. A body `key` that differs from the stored key, letter case included, answers the key mismatch's 400 on the repositories API for every package type; the batch update finds a repository only by its key as stored, and the descriptor refuses an entry `key` that differs from the entry's name. The seeded `artifactory-build-info` repository takes updates on every write path and keeps its package type `buildinfo`, which no other repository may have. 7.x documents neither: its delete pages refuse only the build-info repository's deletion ([#397](https://github.com/weaveplatform/artifactweave/issues/397)), which suggests it is otherwise managed like a local repository **[I]** ([#472](https://github.com/weaveplatform/artifactweave/issues/472)).
- The support bundle repository's key is `jfrog-support-bundle`, singular, as the reserved names list spells it ([D] [repository-management-overview](https://docs.jfrog.com/artifactory/docs/repository-management-overview.md)) and as 7.x's own log lines quoted in its Jira do ([J] [RTFACT-22258](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-22258), [RTFACT-21053](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21053), [RTFACT-20834](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20834)). A delete of it, in any case, answers 400. The [deleterepository](https://docs.jfrog.com/artifactory/reference/deleterepository.md) page's example names it `JFrog-Support-Bundles` instead, which is on [#216](https://github.com/weaveplatform/artifactweave/issues/216) ([#472](https://github.com/weaveplatform/artifactweave/issues/472)).

**Unconfirmed for 7.x:**
- The bundled layouts ("Bundled definitions"): JFrog's documentation publishes only their
  names. Their patterns come from JFrog's 7.38.8 export and the exported 7.x descriptors,
  and whether 7.161 ships the same, and how its parser reads the Terraform, Swift and
  Ansible layouts' tokens, is on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- 6.20's default for `snapshotVersionBehavior` and `suppressPomConsistencyChecks`, which the
  table notes.
- The listing names marked [I] in "Package type names in listings", and that the list uses
  the names the metrics capture shows: the list itself is seen only with `Generic`, `Maven`
  and `Alpine` ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).
- From #472, on [#216](https://github.com/weaveplatform/artifactweave/issues/216): that 7.x
  lets the build-info repository be updated through the repositories API and the descriptor;
  what 7.x answers to an update whose body key differs from the path's only in case; and the
  support bundle key, which the delete page's example spells `JFrog-Support-Bundles`.
- From #457, on [#216](https://github.com/weaveplatform/artifactweave/issues/216): that a
  NuGet repository's v2 read keeps `maxUniqueSnapshots`, that a local or federated npm
  repository's keeps `checksumPolicyType`, `handleReleases` and `handleSnapshots`, and whether
  an administrator's batch read sends the short field set of its sample ("Field sets of the
  reads").
- The v2 read's other fields per package type. The `genericLocalV2` sample also lacks
  `priorityResolution` and `environments`, which artifactweave's v2 read still sends for
  every type: the sample may predate both fields, and the configurations sample of the same
  kind of repository sends them [I] ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).
- Whether a read by anyone but an administrator shows the stored `notes`. The samples whose
  shape looks like that partial read show `"notes": ""`, as artifactweave always does for
  such a reader [I] ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).
- Where the YAML keys `remoteRepoLayout`, `primaryKeyPairDescriptor` and
  `cocoaPods.specRepoProvider` land, and whether `null` is accepted on a field with a fixed
  set of values ("JSON and YAML name differences"), on #216.
- From #396, on [#216](https://github.com/weaveplatform/artifactweave/issues/216): federated
  `bazelmodules`, which only the JSON page offers; the listing names `Hex`, `Nix`,
  `AIEditorExtensions` and `JetBrainsPlugins`; `ivy-default` as Ivy's create layout; that the
  bower, cargo and composer upstream defaults apply to their own type only; and, for the
  `retrievalCachePeriodSecs` minimum, whether self-managed enforces the 30, the 400 message,
  which chocolatey.org hosts count, and whether an update that leaves a shorter stored period
  alone is refused ("Remote fields").
- Parts of the key rules, on [#216](https://github.com/weaveplatform/artifactweave/issues/216):
  the 400 messages and the order of the checks, which no 7.x page documents; whether 7.x
  compares the reserved names and prefixes without case, and refuses tabs and other white
  space as spaces; whether a Docker, OCI or Helm OCI virtual key may have 64 characters,
  which the overview's table allows and the Docker page's 63-character "hard limit" does
  not; whether the length counts characters or bytes; and whether a leading digit is
  accepted, which 7.53.1's release note allows and the class pages forbid.
