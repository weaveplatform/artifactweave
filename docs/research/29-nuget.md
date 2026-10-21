# NuGet

Research baseline: **2026-10-19**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-19, by [#528](https://github.com/weaveplatform/artifactweave/issues/528); [section 18](#18-artifactory-7x-check) records the check.

Phase P8 implements the NuGet v2 and v3 protocols for local `nuget` repositories.
The research covers `dotnet`, `nuget.exe`, Chocolatey, PowerShell and the JFrog CLI;
the native exit matrix below proves the pinned .NET SDK and `jf dotnet` clients. [Research 04](04-package-protocols.md#nuget)
sketched the v3 and v2 protocol from Microsoft's documentation and the client's V2 parser, and
its 7.x check asked P8 to confirm it claim by claim. This report does that from JFrog's
documentation, release notes, KB and Jira, from what the NuGet client and the JFrog CLI send
and need, and from Artifactory 6.20 where nothing else speaks. It records what 7.x changed (the
newer NuGet handler, Enforce Layout with its normalised versions, the migration tool) and
corrects research 03 and 04 (section 16). Decision [0038](decisions/0038-nuget.md) records the
choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-19 |
| **[S]** | seen in source code at the cited commit |
| **[KB]** | stated in a JFrog knowledge-base article at the cited URL, read on 2026-10-19 |
| **[J]** | recorded in JFrog's public Jira, read on 2026-10-19; it counts as 7.x behaviour under the rules in the [research README](README.md) |
| **[C]** | a request log from a live Artifactory published outside JFrog, in a public issue of the NuGet client, read on 2026-10-19 |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **JFrog's documentation**, under `https://docs.jfrog.com/`, read as `.md` with the date each
  page gives: `artifactory/docs/nuget-repositories` (updated 2026-09-21),
  `artifactory/docs/local-repositories` (2026-09-15), `artifactory/docs/migration-tool`
  (2026-10-12), `artifactory/reference/calculatenugetrepositorymetadata` (2026-06-15),
  `artifactory/docs/jf-nuget` and `artifactory/docs/jf-dotnet` (2026-10-05),
  `artifactory/docs/repository-layouts` (2026-09-09),
  `artifactory/docs/package-manager-compatibility-matrix` (2026-10-19),
  `artifactory/reference/createrepository` and `getrepositoryconfigurationv2` (2026-05-28),
  `integrations/docs/configuration-json-files` (2026-07-26), and the release pages
  `releases/docs/artifactory-self-managed-releases` (2026-10-05),
  `artifactory-saas-releases` (2026-10-19), `artifactory-known-issues` (2026-09-23) and
  `artifactory-deprecations` (2025-12-29).
- **JFrog's knowledge-base articles**:
  [000006062](https://jfrog.com/help/r/artifactory-why-does-the-deployment-of-nuget-pre-release-versions-allow-overwriting-without-delete-overwrite-permissions)
  (pre-release overwrites, modified 2025-07-20) and
  [000005403](https://jfrog.com/help/r/artifactory-how-does-the-max-unique-snapshots-parameter-work-in-artifactory/package-types-supported-for-max-unique-snapshots)
  (`maxUniqueSnapshots`, modified 2025-05-15).
- **JFrog's public Jira**, read through `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`:
  the RTFACT keys cited below. Most were filed by JFrog support engineers with a reproduction;
  where a claim rests on a customer's report alone, the row says so.
- **The NuGet client** `NuGet/NuGet.Client` at tag **8.1.0.12** (`350273adcd4c`, 2026-10-16),
  `src/NuGet.Core/NuGet.Protocol/`: `ServiceTypes.cs`, `ProtocolConstants.cs`,
  `Resources/PackageUpdateResource.cs`, `Providers/PackageUpdateResourceV3Provider.cs`,
  `Providers/SymbolPackageUpdateResourceV3Provider.cs`, `Providers/ServiceIndexResourceV3Provider.cs`,
  `Resources/RegistrationResourceV3.cs`, `DependencyInfo/RegistrationUtility.cs`,
  `Model/RegistrationPage.cs`, `RemoteRepositories/HttpFileSystemBasedFindPackageByIdResource.cs`
  and the three `*FindPackageByIdResourceProvider.cs`, `Resources/RawSearchResourceV3.cs`,
  `Resources/AutoCompleteResourceV3.cs`, `LegacyFeed/V2FeedParser.cs`,
  `LegacyFeed/V2FeedQueryBuilder.cs`, `LegacyFeed/V2FeedPackageInfo.cs`,
  `LegacyFeed/ODataServiceDocumentUtils.cs`, `LegacyFeed/LegacyFeedCapabilityResourceV2Feed.cs`,
  `HttpSource/HttpSource.cs`, `HttpSource/HttpSourceAuthenticationHandler.cs`,
  `HttpSource/HttpSourceCredentials.cs`, `HttpSource/HttpHandlerResourceV3Provider.cs`; and
  `src/NuGet.Core/NuGet.Commands/CommandRunners/PushRunner.cs`,
  `src/NuGet.Core/NuGet.Packaging/PackageCreation/Utility/PackageIdValidator.cs`.
- **Microsoft's NuGet server API reference** (`NuGet/docs.microsoft.com-nuget` at
  `2523f70766b2`, 2026-10-15), published at `https://learn.microsoft.com/en-us/nuget/api/`:
  [overview](https://learn.microsoft.com/en-us/nuget/api/overview),
  [implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide),
  [service index](https://learn.microsoft.com/en-us/nuget/api/service-index),
  [package base address](https://learn.microsoft.com/en-us/nuget/api/package-base-address-resource),
  [registration](https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource),
  [search](https://learn.microsoft.com/en-us/nuget/api/search-query-service-resource),
  [autocomplete](https://learn.microsoft.com/en-us/nuget/api/search-autocomplete-service-resource),
  [package publish](https://learn.microsoft.com/en-us/nuget/api/package-publish-resource),
  [symbol package publish](https://learn.microsoft.com/en-us/nuget/api/symbol-package-publish-resource),
  and [package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning).
- **The JFrog CLI**: jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/dotnet/dotnetcommand.go`
  and `artifactory/commands/nuget/command.go`; build-info-go `f9d40441f262`,
  `flexpack/nuget/nuget_artifacts.go` and `build/utils/dotnet/configfiletemplate.go`.
- **terraform-provider-artifactory** v12.11.14,
  `pkg/artifactory/resource/repository/local/resource_artifactory_local_nuget_repository.go`
  and `docs/resources/local_nuget_repository.md`.
- **Artifactory OSS 6.20** (`knottnhupfer/artifactory-oss` at `b99e5649bcad`), for what no 7.x
  source states: `backend/core/src/main/java/org/artifactory/addon/nuget/NuGetProperties.java`,
  `.../addon/NuGetAddon.java`, `.../repo/interceptor/NuGetCalculationInterceptor.java`,
  `.../repo/interceptor/IntegrationCleanerInterceptor.java`,
  `.../repo/cleanup/IntegrationCleanupServiceImpl.java`,
  `.../metadata/service/provider/NugetMetadataProvider.java`,
  `base/common/src/main/java/org/artifactory/addon/nuget/NuGetAuthUtil.java`,
  `base/config/src/main/java/org/artifactory/common/ConstantValues.java`; and JFrog's 7.38.8
  descriptor export in jfrog-cli-artifactory `4a0f3827cfa0`,
  `artifactory/commands/testdata/artifactory_export/etc/artifactory.config.latest.xml`.
- **Captures outside JFrog**: [NuGet/Home#11600](https://github.com/NuGet/Home/issues/11600)
  (2022-02-16) and [NuGet/Home#12517](https://github.com/NuGet/Home/issues/12517)
  (2023-03-30), which quote an Artifactory request log.

## 1. Clients and the JFrog CLI

| Fact | Basis |
|---|---|
| Artifactory documents four clients: PowerShell 1.0.5 and later, Chocolatey 1.2.0 and later, the NuGet CLI 3.4.4 and later, and the .NET CLI | [D] nuget-repositories, "Supported NuGet clients" |
| Chocolatey "only supports NuGet API V2"; JFrog recommends its v2 source `<base>/api/nuget/<repo>` and also shows a v3 source ending in `index.json` | [D] nuget-repositories, "Chocolatey Client" |
| PowerShell registers the v2 root `<base>/api/nuget/<repo>` as both its source and its publish location, and publishes with `-NuGetApiKey <USER>:<API-KEY>` | [D] nuget-repositories, "PowerShell Client" |
| Chocolatey and PowerShell support came in [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed) (SaaS 7.111.1), and .NET CLI support in SaaS [7.118.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71182-saas) and self-managed [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) | [D] release notes |
| The JFrog CLI's officially supported .NET SDK is 8.0.417; 10.0.401 is the newest that passed its tests, and 8.0.408 failed them | [D] [package-manager-compatibility-matrix](https://docs.jfrog.com/artifactory/docs/package-manager-compatibility-matrix.md) |
| `jf nuget` and `jf dotnet` write a temporary `NuGet.Config` with one source, `JFrogCli`, at `<url>/api/nuget/v3/<repo>/index.json` with `protocolVersion="3"`, or at `<url>/api/nuget/<repo>` with `protocolVersion="2"` under `--nuget-v2`, and pass it with `-configfile`, unless the command already names a config file or a source | [S] jfrog-cli-artifactory `dotnet/dotnetcommand.go:232-367` |
| That source's credentials are `packageSourceCredentials` `Username` and `ClearTextPassword`. With an access token and no user, the user name is read from the token and the token is the password | [S] `dotnetcommand.go:339-366`; build-info-go `configfiletemplate.go` |
| NuGet sends a source's credentials only after a 401: they are the `HttpClientHandler`'s `Credentials`, held in a `CredentialCache`, with `UseDefaultCredentials` false. On a 401, or on a 403 to a request that carried no API key, the handler asks its credential service and retries | [S] `HttpSourceAuthenticationHandler.cs`, `HttpSourceCredentials.cs`, `PackageUpdateResource.cs:650-655` |
| From NuGet 6.7 the client pre-authenticates a URL in the service index's directory or a subdirectory of it, once that directory has authenticated; any other URL still starts anonymously | [D] Microsoft implementation guide, "URL structure for authenticated feeds" |
| An Artifactory request log quoted by the NuGet team shows each restore request answered 401, then 200 with credentials: `GET /artifactory/api/nuget/v3/nuget`, `.../v3/nuget/registration-semver2/awssdk.core/index.json` and `.../v3/nuget/registration-semver2/Download/awssdk.core/3.3.25.1` | [C] NuGet/Home#11600, #12517 |
| The client decompresses gzip and deflate responses itself | [S] `HttpHandlerResourceV3Provider.cs:66` |
| `jf nuget push` and `jf dotnet nuget push` (native mode, JFrog CLI 2.123.0 and later) run the client's own push, then search for each pushed file at `<repo>/<file name>` and stamp `build.name`, `build.number`, `build.timestamp` and the CI properties on it, retrying the search because "Artifactory indexes NuGet packages asynchronously". A virtual repository is resolved to its `defaultDeploymentRepo` first | [S] jfrog-cli-artifactory `nuget/command.go:486-652`; [D] jf-nuget |
| The path the CLI expects is the pushed file's own name at the repository root, `<id>.<version>.nupkg`; a `.snupkg` also at the root under its own name; a legacy `<id>.<version>.symbols.nupkg` at `<id>.<version>.nupkg`, "the `.symbols` segment is dropped" | [S] build-info-go `nuget_artifacts.go:55-98` |

## 2. Routes

| Fact | Basis |
|---|---|
| The v2 root `<base>/api/nuget/<repo>` "handles all NuGet related requests (search, download, upload, delete) and supports both V1 and V2 requests"; every NuGet URL must be prefixed with `api/nuget` | [D] nuget-repositories, "Access NuGet Repositories from Visual Studio", "Use the NuGet Command Line" |
| The v3 source is `<base>/api/nuget/v3/<repo>/index.json`; Visual Studio is given `<base>/api/nuget/v3/<repo>` without the file name | [D] nuget-repositories |
| NuGet treats a source as v3 when its `protocolVersion` is 3 or its URL ends in `.json`, and accepts a service index only when `version` is SemVer with major version 3 | [S] `ServiceIndexResourceV3Provider.cs:62-64,240-277` |
| A 7.x request log shows the service index answered at `GET .../api/nuget/v3/<repo>` | [C] NuGet/Home#12517 |
| A push may name a folder, `nuget push <pkg> -Source <base>/api/nuget/<repo>/<PATH_TO_FOLDER>`, so that packages follow a custom layout; "The NuGet install/delete will not work if there is a subfolder in the command (even if the NuGet push has a subfolder)" | [D] nuget-repositories, "Push NuGet Packages", "Use the NuGet Command Line"; [J] RTFACT-5912 (3.0.3) |
| Through `.../api/nuget/<repo>/611`, `FindPackagesById()` answered 405 and `DELETE .../611/AppLogger/1.0.2` 404, while the same requests at the root worked; JFrog closed it as not a bug | [J] RTFACT-23310 (7.7.3) |
| JFrog's support suggested `.../api/nuget/v3/<repo>/FindPackagesById()?id='<id>'&semVerLevel=2.0.0` in 2024, so 7.x also answers the v2 query under the v3 prefix | [J] RTFACT-30380, the workaround |
| 6.20 roots NuGet at `nuget` and v3 at `nuget/v3` | [S] 6.20 `NuGetResourceConstants.java`, `NuGetV3ResourceConstants.java` |

## 3. The v3 service index

| Fact | Basis |
|---|---|
| The client looks resources up by these `@type` values, the first listed winning: `SearchQueryService` (`/Versioned`, `/3.4.0`, `/3.0.0-beta`; `/3.5.0` for the package type filter); `RegistrationsBaseUrl` (`/Versioned`, `/3.6.0`, `/3.4.0`, `/3.0.0-rc`, `/3.0.0-beta`, plain); `SearchAutocompleteService` (`/Versioned`, `/3.0.0-beta`); `PackageBaseAddress` (`/Versioned`, `/3.0.0`); `PackagePublish` (`/Versioned`, `/2.0.0`); `SymbolPackagePublish/4.9.0`; `LegacyGallery` (`/Versioned`, `/2.0.0`); `RepositorySignatures` (`/5.0.0`, `/4.9.0`, `/4.7.0`); `VulnerabilityInfo/6.7.0`; `ReportAbuseUriTemplate`, `ReadmeUriTemplate`, `PackageDetailsUriTemplate/5.1.0`, `OwnerDetailsUriTemplate/6.11.0`, `PackageStaging/1.0.0` | [S] `ServiceTypes.cs` |
| Microsoft lists four resources as required, `PackageBaseAddress`, `PackagePublish`, `RegistrationsBaseUrl` and `SearchQueryService`; the rest are optional. `LegacyGallery` is undocumented: it "allows a V3 service index to point to a corresponding V2 source URL" and "supports the `nuget.exe list`" | [D] Microsoft overview, "Resources and schema" |
| Restore uses `PackageBaseAddress` whenever the index lists it, and the registration only otherwise | [S] `HttpFileSystemBasedFindPackageByIdResourceProvider.cs:16`, `RemoteV3FindPackageByIdResourceProvider.cs` |
| A push takes its URL from `PackagePublish`; an index without one fails the push | [S] `PackageUpdateResourceV3Provider.cs:31-48` |
| `dotnet nuget push` looks for `SymbolPackagePublish/4.9.0` in the source's index; when it is there, the push sends each `.snupkg` beside its `.nupkg` to that URL with the same API key, and an explicit `.snupkg` push goes there too. Without it and without `--symbol-source`, a `.snupkg` is not sent | [S] `PushRunner.cs:96-136`, `PackageUpdateResource.cs:100-125,300-345` |
| 7.x's `SymbolPackagePublish` is `<base>/api/nuget/v3/<repo>/symbols`, the URL JFrog also gives as a symbols source | [S] build-info-go `nuget_artifacts.go:60-66`; [D] nuget-repositories, "NuGet CLI Symbol configuration" |
| 7.x's registration hives are `<base>/api/nuget/v3/<repo>/registration-semver2/` and `.../registration/` | [J] RTFACT-30380 (2024), RTFACT-29322, RTFACT-29190, RTFACT-26500 (7.27.6, `.../registration/Download/...`); [C] |
| 7.x's search is `<base>/api/nuget/v3/<repo>/query` | [J] RTFACT-29322 (SaaS, 2023, a local repository) |
| No source shows 7.x's `PackageBaseAddress`, `PackagePublish` or autocomplete URLs, the full resource list, or whether it lists `LegacyGallery` | [I] |
| `PackageBaseAddress` "is supported in normalized NuGet repositories (where Enforce Layout is enabled). It is not supported in non-normalized NuGet Repositories" | [D] nuget-repositories |
| `repository-signatures` is served only for remote v3 repositories whose upstream serves it | [D] [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed), SaaS 7.155.3 |
| For remote v3 repositories 7.x once looked only for the plain `SearchQueryService` type in the upstream index | [J] RTFACT-26794 (Done, 2022) |

## 4. The flat container (`PackageBaseAddress`)

| Fact | Basis |
|---|---|
| `GET {@id}/{LOWER_ID}/index.json` answers `{"versions":[...]}`: "all lowercased, normalized NuGet version strings" without build metadata, "both listed and unlisted package versions", each one available for download; 404 when the source has no version of the id | [D] Microsoft package base address |
| `GET {@id}/{LOWER_ID}/{LOWER_VERSION}/{LOWER_ID}.{LOWER_VERSION}.nupkg` answers the package and `.../{LOWER_ID}.nuspec` its manifest; 404 when absent. Both support `GET` and `HEAD` | [D] Microsoft package base address |
| The client treats a 404 on `index.json` as "no such package"; any other failure is an error | [S] `HttpFileSystemBasedFindPackageByIdResource.cs:486-579` (`IgnoreNotFounds = true`) |
| The client builds the package URL from the version string the index listed, as written on .NET and lower-cased on .NET Standard, so the server must accept the version in the case it listed it | [S] `HttpFileSystemBasedFindPackageByIdResource.cs:435-451,601-608` |
| Only Enforce Layout opens it on 7.x: "The only way to use the NuGet V3 PackageBaseAddress API in Artifactory is by using NuGet Enforce Layout repositories" | [D] nuget-repositories; [D] [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), SaaS 7.139.1 |
| Before that, restores paged through the registration, which exhausted memory on packages with many versions. A NuGet engineer at Microsoft adds that `dotnet tool install` from the .NET 9 previews failed against Artifactory, and that the resource is one of the four required. The issue is still open | [J] RTFACT-26507 (open, affects 7.21.0 and 7.27.10) |
| No source shows its URL on 7.x | [I] |

## 5. Registration

| Fact | Basis |
|---|---|
| The client asks for `{@id}/{lower id}/index.json`, and builds a leaf's URL as `{@id}/{lower id}/{lower normalised version}.json` | [S] `RegistrationResourceV3.cs:52-96` |
| A 404 on the index means the package does not exist. Each page object carrying `items` is used as it is; one without is fetched from its `@id`. `lower` and `upper` must parse as NuGet versions | [S] `RegistrationUtility.cs:31-170`, `Model/RegistrationPage.cs` |
| Microsoft defines three hives: `RegistrationsBaseUrl`, with `/3.0.0-beta` and `/3.0.0-rc` as aliases, uncompressed and without SemVer 2.0.0 packages; `/3.4.0`, gzip, without them; `/3.6.0`, gzip, with them | [D] Microsoft registration, "Versioning" |
| Index: `count` and `items`, each page `@id`, `count`, `lower`, `upper`, optional `items` and `parent`. nuget.org inlines every leaf below 128 versions and otherwise links pages of 64 | [D] Microsoft registration |
| Leaf in a page: `@id`, `catalogEntry`, `packageContent`. `catalogEntry` requires `@id`, `id` and `version`, and may carry `authors`, `dependencyGroups`, `deprecation`, `description`, `iconUrl`, `language`, `licenseUrl`, `licenseExpression`, `listed` ("listed if absent"), `minClientVersion`, `packageContent`, `projectUrl`, `published`, `readmeUrl`, `requireLicenseAcceptance`, `summary`, `tags`, `title`, `vulnerabilities` | [D] Microsoft registration |
| A 7.x local repository's index lists one page by link, without `items`, even for a single version: `{"count":1,"items":[{"count":1,"lower":"1.0.0","upper":"1.0.0","@id":".../registration-semver2/dotnet-package/page/1.0.0/1.0.0.json"}],"@id":".../registration-semver2/dotnet-package/index.json"}` | [J] RTFACT-30380 (JFrog's reproduction, 2024) |
| The page has `count`, `lower`, `parent`, `upper`, `items` and `@id`. Each item has `catalogEntry` with `authors` (a string), `description`, `listed: true`, `packageContent`, `requireLicenseAcceptance`, `version`, `dependencyGroups` (`targetFramework`, `dependencies` of `id`, `range` and `registration`) and `id`; and beside it `packageContent`, `registration` and `@id`. There is no `published` and no `catalogEntry.@id` | [J] RTFACT-30380 |
| A dependency's `range` is the nuspec's version as written, such as `"6.7.0"` | [J] RTFACT-30380 |
| `packageContent` is `<base>/api/nuget/v3/<repo>/registration-semver2/Download/<id>/<version>` there, and `.../registration/Download/<id>/<version>` in the other hive; older captures point at the v2 `<base>/api/nuget/<repo>/Download/<id>/<version>` | [J] RTFACT-30380, RTFACT-26500 (7.27.6); RTFACT-18567 (6.8.2), RTFACT-29190 (7.46.11, remote), RTFACT-31126 (SaaS 2025, remote); [C] |
| The leaf `@id` and the `registration` links there read `.../registration-semver2/registration-semver2/<id>/...`, the hive named twice | [J] RTFACT-30380 |
| A remote repository's leaf document is `{listed, packageContent, registration, @id}`, without `published`; nuget.org's adds `published` and the catalog link | [J] RTFACT-27465 (open) |
| The publish date is missing from the API though the UI shows it; both requests are open. artifactweave writes `published`, filling the gap JFrog tracks, which is not a deviation (decision [0038](decisions/0038-nuget.md#registration)) | [J] RTFACT-27465, RTFACT-30380 |
| JFrog's support offers `.../registration-semver2/<id>/page/<first>/<last>.json` for any range of versions, so 7.x answers a page for bounds it did not list | [J] RTFACT-30380, the workaround |
| Page URLs once lower-cased mixed-case pre-release bounds and then found no page; fixed in 2022 | [J] RTFACT-27116 (Done) |
| A `packageContent` with the id lower-cased failed on 6.x; fixed in 6.17.0. Lower-cased download URLs were fixed in 7.31.10 | [J] RTFACT-18567 (6.17.0), RTFACT-26500, RTFACT-15356 (7.31.10) |
| No source shows 7.x's page size, or whether it ever inlines leaves | [I] |

## 6. Search and autocomplete

| Fact | Basis |
|---|---|
| The client sends `q=<term>&skip=&take=&prerelease=`, optionally `&includeDelisted=true`, `&supportedFramework=...` and `&packageType=`, and always `&semVerLevel=2.0.0`. `totalHits` must be an integer and `data` an array | [S] `RawSearchResourceV3.cs:41-77`, `Converters/V3SearchResultsConverter.cs` |
| Microsoft: `q`, `skip` (default 0), `take`, `prerelease` (absent means excluded), `semVerLevel` (absent means 1.0.0) and `packageType`. "An unlisted package should never appear in search results"; no `q` returns every package; the server may cap `take`, and nuget.org caps `skip` at 3,000 and `take` at 1,000 | [D] Microsoft search |
| A result has `id`, `version` and `versions` (`version`, `downloads`, `@id`), with optional `description`, `authors`, `iconUrl`, `licenseUrl`, `owners`, `projectUrl`, `registration`, `summary`, `tags`, `title`, `totalDownloads`, `verified`, `deprecation`, `vulnerabilities`, and `packageTypes` from `/3.5.0` | [D] Microsoft search |
| A 7.x local repository answered `query?q=...&skip=0&take=20&prerelease=false&semVerLevel=2.0.0` with `{"totalHits":1,"data":[{"registration":".../registration-semver2/<lower id>/index.json","version","description","licenseUrl","projectUrl","authors":[...],...}]}`, the newest version | [J] RTFACT-29322 (SaaS 2023; the capture is cut after `authors`) |
| A package is SemVer 2.0.0 when its version has a dot-separated pre-release label or build metadata, or, for nuget.org, when a dependency range has such a bound; such packages are hidden unless `semVerLevel` is 2.0.0 or more | [D] Microsoft package versioning, "Semantic Versioning 2.0.0"; Microsoft implementation guide |
| On Artifactory, SemVer 2 packages are hidden from clients older than 4.3.0; `artifactory.nuget.disableSemVer2SearchFilterForLocalRepos` keeps the older behaviour | [D] nuget-repositories, "NuGet SemVer 2.0 Package Support"; [S] 6.20 `ConstantValues.java:253` |
| 7.x rate-limits its NuGet search APIs; no threshold is documented | [D] SaaS [7.125.3](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71253-saas), self-managed 7.125.4 |
| Search performance fixes: RTDEV-91910 (MySQL, SaaS 7.164.0), RTDEV-93752 (Oracle, 7.161.19), RTDEV-93602 (SQL Server, 7.146.29) | [D] release notes |
| "Authenticated users may access private NuGet metadata", fixed in 7.146.35, 7.161.16 and SaaS 7.164.0 | [D] release notes, CVE-2026-66378 |
| Search once missed partial words of an id, such as `Shared` for `Pbp.API.Shared`, which nuget.org finds | [J] RTFACT-18567, a customer's comment |
| The client's autocomplete asks for ids by prefix, `q=<prefix>&prerelease=&semVerLevel=2.0.0`, and reads versions from the registration, not from autocomplete | [S] `AutoCompleteResourceV3.cs:41-197` |
| Autocomplete answers `{totalHits, data:[ids]}`; "A package with only unlisted versions will not appear" | [D] Microsoft autocomplete |
| No source shows 7.x's autocomplete URL, or how its search and autocomplete match terms | [I] |

## 7. Push, delete and relist

| Fact | Basis |
|---|---|
| A push is `PUT` to the `PackagePublish` URL with a trailing slash added, or to `<host>/api/v2/package/` when the source names a host alone. The body is `multipart/form-data` with one part named `package`, file name `package.nupkg`, type `application/octet-stream`, sent with `Transfer-Encoding: chunked`. `X-NuGet-ApiKey` is added when a key is set | [S] `PackageUpdateResource.cs:30,642-676,849-874` |
| Without an API key the client only warns, and the push relies on the 401 challenge for credentials | [S] `PackageUpdateResource.cs:287-292`; section 9 |
| `--skip-duplicate` tolerates a 409 and nothing else; on a 409 without it the client advertises the flag. When a package is skipped as a duplicate its symbols are not pushed | [S] `PackageUpdateResource.cs:296-345,535-639` |
| The client never reads the body of an error. A failed request reports its status and reason phrase, such as "Response status code does not indicate success: 404 ()." from 7.27.9, and a skipped duplicate logs the reason phrase | [S] `PackageUpdateResource.cs:535-600`, `V2FeedParser.cs:610-618`, `HttpSource.cs:143-158`; [J] RTFACT-26564 |
| Microsoft: a push answers 201 or 202, 400 for an invalid package and 409 for an id and version that exist; the first multipart item is the package and the rest are ignored | [D] Microsoft package publish |
| A delete is `DELETE {@id}/{id}/{version}` with the API key; any 2xx succeeds. Microsoft gives 204 and 404, and lets a server treat it as "a hard delete, soft delete, or unlist". A relist is `POST` to the same URL: 200, also when already listed, or 404 | [S] `PackageUpdateResource.cs:751-793`; [D] Microsoft package publish |
| A push to Artifactory answers 201: `PUT .../api/nuget/jana-local/` "Created" | [J] RTFACT-26078 (6.23.21); RTFACT-5847 (a 3.0.2 log, 201) |
| Pushing a release version that exists again through `api/nuget` to a local repository answers 403 to a user without Delete/Overwrite; that is expected | [KB] 000006062 |
| A pre-release version (one with a dash and a suffix) "goes through smoothly, even if there's already a package with the same name". JFrog calls it a deliberate design choice from RTFACT-7636 in 2015, since NuGet repositories have no cleanup policy for pre-releases | [KB] 000006062; [J] RTFACT-7636 (3.9.5, 4.2.0), RTFACT-14442 (5.6.0, the dash is looked for in the version only) |
| The KB article does not say whether the exemption holds under Enforce Layout, which refuses duplicates (below) | [I] |
| Customers report that exemption as a bug; none is fixed | [J] RTFACT-19557, RTFACT-17123, RTFACT-26078 (deferred), RTFACT-30027 (7.59.9) |
| A REST deploy over an existing file without Delete answers `{"errors":[{"status":403,"message":"Not enough permissions to overwrite artifact 'nuget-local:<file>' (user '<user>' needs DELETE permission)."}]}` | [J] RTFACT-14442, RTFACT-17123 |
| A request to answer a duplicate with 409 instead of 403 is marked Done, with no fix version | [J] RTFACT-26347 (2022) |
| With Enforce Layout a duplicate, including one that normalises to an existing version, is refused: "Artifactory will normalize the new package name ..., consider the two packages to be identical, prevent the upload, and return an error", listed as 409 | [D] nuget-repositories, "NuGet Enforce Layout", "Error Message" |
| `nuget delete` removes the file; a request to unlist instead was deferred | [J] RTFACT-16148 (deferred 2022) |
| 7.x has no unlisting: "Allow nuget.org style unlisting" and NuGet deprecation are open requests, and a 7.x registration shows `listed: true` | [J] RTFACT-19806 (open), RTFACT-21457 (reopened), RTFACT-30418 (open), RTFACT-30380 |
| No source shows the bodies of push and delete answers, the status of a delete, or what 7.x answers a relist | [I] |

## 8. Symbols

| Fact | Basis |
|---|---|
| From 7.36 Artifactory indexes symbol packages pushed with or apart from their packages and resolves `.pdb` files for Visual Studio from local, remote and virtual repositories; it supports Microsoft PDB v7 and Portable PDB v1.0 | [D] nuget-repositories, "Artifactory as Your Symbol Server" |
| `nuget push <pkg>.nupkg` with a `.snupkg` beside it pushes both; `nuget push <pkg>.snupkg` pushes the symbols alone | [D] nuget-repositories, "NuGet Symbol Packages" |
| 7.x stores what the symbols endpoint receives "flat and unrenamed", `<id>.<version>.snupkg` at the root. A `/api/nuget/v2/<repo>/symbolpackage` endpoint stores `symbolpackage/<id>.<version>.nupkg` | [S] build-info-go `nuget_artifacts.go:60-70` |
| Microsoft: a symbols push answers 201, 400 for an invalid package, 404 when no package of that id and version exists, 409 for one still being published, 413 when too large; nuget.org accepts the same symbols more than once | [D] Microsoft symbol package publish |
| A legacy `.symbols.nupkg` should not be indexed as a package: 7.90.5 stopped indexing them (RTDEV-41067) and 7.104.14 stopped a reindex doing so (RTDEV-54891). A deployed one is not indexed until a reindex | [D] release notes; [J] RTFACT-30983 (closed) |
| Visual Studio reads `<base>/api/nuget/v3/<repo>/<file>.pdb/<key>/<file>.pdb`; 7.x keeps extracted symbols at `<repo>/.symbols/<file>.pdb/<guid>/<file>.pdb`, and accepts the portable-PDB key with an upper-case `FFFFFFFF` age since 2023 | [J] RTFACT-30069 (Done), RTFACT-30020, RTFACT-29650 (7.55.13) |
| Force Authentication covers the symbol server too | [J] RTFACT-29479 (Done, 2023) |
| No source shows whether a symbols push needs its package, or what a second push of the same symbols answers | [I] |
| Microsoft symbol-store keys use the PDB v7 GUID and unpadded hexadecimal age; Portable PDB uses the GUID component of its ID plus `FFFFFFFF` (case-insensitive) | [S] [SSQP key conventions](https://github.com/dotnet/symstore/blob/main/docs/specs/SSQP_Key_Conventions.md), [Portable PDB metadata specification](https://github.com/dotnet/runtime/blob/main/docs/design/specs/PortablePdb-Metadata.md); [S] [LLVM MSF layout](https://llvm.org/docs/PDB/MsfFile.html), [PDB info stream](https://llvm.org/docs/PDB/PdbStream.html) |
| Artifactweave #571 derives `.symbols/` files from local `.snupkg` sources, serves GET/HEAD through item downloads, and rebuilds/removes them on reindex/deletion. Source ownership lives in trusted package-version metadata; shared keys survive deletion of one owner. Normal uploads scan manifests and only decompress their own or a changed winning source archive | Implementation, decision [0038](decisions/0038-nuget.md#symbols) |
| The exact 7.x rules for conflicting symbol bytes, filename case, malformed PDBs, extraction limits and source deletion with another owner are not documented. Artifactweave chooses lexical source path (then bytes within an archive), canonical lower-case filenames, ignores unsupported PDB identities, and bounds actual extraction at 64 MiB/file and 256 MiB/package | [I], compare on #216; no additional deviation registered |

## 9. Authentication

| Fact | Basis |
|---|---|
| The API key is `<USER-NAME>:<API-KEY>` (`nuget setapikey`, `choco apikey`, `-NuGetApiKey`); JFrog's examples use a reference token as the key and as the source password | [D] nuget-repositories |
| A key alone, without the user, is not supported; the request is open | [J] RTFACT-20340 (open, 2019) |
| The client sends the key only on push and delete; other requests need the source's credentials | [J] RTFACT-5847 (JFrog's staff), RTFACT-13905; [S] `PackageUpdateResource.cs:670-673,778-781` |
| 6.20 names the header `X-NuGet-ApiKey` and the separator `:` | [S] 6.20 `NuGetAddon.java` |
| Wrong credentials from the .NET client answer 401, not 500 | [D] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed), RTDEV-37982 |
| A bearer access token on `GET .../api/nuget/v3/<repo>` was refused on 7.50.3 as `non_authenticated_user` with `{"errors":[{"status":403,"message":"Forbidden"}]}`; the issue was closed without a fix | [J] RTFACT-29362 |
| Anonymous access is on by default. With it on, "Artifactory will not query the NuGet client for authentication parameters by default"; a repository's Force Authentication makes it "first request authentication parameters from the NuGet client" | [D] nuget-repositories, "Anonymous Access to NuGet Repositories" |
| With anonymous access on, Force Authentication is what makes a repository the anonymous user cannot read work for NuGet clients | [J] RTFACT-13905, a customer's comment |
| 6.20 reads `forceNugetAuthentication` from the local, remote or virtual repository, and otherwise the system property `artifactory.nuget.forceAuthentication` (`true`, `all`, or repository keys with `*` patterns). `artifactory.nuget.allowRootGetWithAnon` lets an anonymous `GET` of a repository's root through unless Force Authentication applies | [S] 6.20 `NuGetAuthUtil.java`, `ConstantValues.java:251-252`; [J] RTFACT-7475 (4.1.0) |
| Force Authentication applies to local, remote and virtual NuGet repositories | [D] local-repositories, "Additional Settings for NuGet Local Repositories"; research 03 |

## 10. Storage, versions and Enforce Layout

| Fact | Basis |
|---|---|
| A push lands at the repository root as `<id>.<version>.nupkg`: JFrog's 7.46.11 reproduction pushes with `dotnet nuget push ... -k user:pass` to a v3 source and sets properties at `<repo>/ArtPropsTestLib.<version>.nupkg`. 6.17 logs `Deploy to 'nuget-local:Hangfire.Core.1.7.7.nupkg'` | [J] RTFACT-29222, RTFACT-21236, RTFACT-5733; [S] build-info-go (section 1) |
| The id keeps its case in the file name (`EntityFramework`, deployed as such) | [J] RTFACT-18567 |
| The version in the file name is the nuspec's, build metadata included, without Enforce Layout: `Org.Module.1.0.0+20200829102020.nupkg` | [J] RTFACT-23229 (6.16.0) |
| 6.20 indexes every `.nupkg` in a NuGet repository when it is created, imported, copied or moved there, wherever it is | [S] 6.20 `NuGetCalculationInterceptor.java` |
| 7.x's `nuget-default` layout is `[orgPath]/[module]/[module].[baseRev](-[fileItegRev]).nupkg` with both integration regexes `.*`, no distinct descriptor | [S] JFrog's 7.38.8 export, `artifactory.config.latest.xml:566-572` |
| JFrog's 2013 advice was a layout of two folder levels for NuGet, since a layout needs an organisation, a module and a base revision; pushes still went to the root | [J] RTFACT-5733 (JFrog's staff, and a customer in 2014) |
| Enforce Layout gives access to `PackageBaseAddress`; normalises "the names of the packages that you deploy" by NuGet's normalised version numbers; and "prevents the upload of duplicate versions or several package versions that are identical according to the Normalized Version Numbers rules" | [D] nuget-repositories; [D] 7.146.7, SaaS 7.139.1 |
| A normalised version has at least three parts, no leading zeros, no fourth part of 0 and no build metadata: `1.2.3.0` → `1.2.3`, `1.2.3.1` kept, `1.0` → `1.0.0`, `01.02.03` → `1.2.3`, `1.2.3+build.5` → `1.2.3`. Build metadata is removed, not refused, and a version that is not SemVer at all is refused | [D] migration-tool, "Understanding Normalized Versions" |
| The nuget-repositories page's own example deploys `Package-1.0.0.0+asdfs.nupkg` as `Package-1.0.0.0.nupkg`, keeping the fourth zero that the migration tool's table removes, and writes `{NAME}-{VERSION}-{OPTIONAL-PARAMETERS}.nupkg` with hyphens | [D] nuget-repositories |
| NuGet's rules are the migration tool's; "NuGet package repositories must treat these values in the same way as NuGet to prevent package version duplication" | [D] Microsoft package versioning, "Normalized version numbers" |
| A NuGet id matches `^\w+([.-]\w+)*$`, ignoring case, and is at most 100 characters | [S] `PackageIdValidator.cs:17-28` |
| A name that breaks the conventions answers 409, "Response status code does not indicate success: 409 (Conflict)." | [D] nuget-repositories, "Error Message" |
| Enforce Layout is chosen when a repository is created and cannot be changed afterwards; a global "Normalize package version" setting applies it to every NuGet repository created after it is set, which cannot then turn it off | [D] nuget-repositories, "Activate NuGet Enforce Layout"; migration-tool |
| Packages that are not SemVer cannot be uploaded to a normalised repository; copies and moves are allowed only between repositories of the same kind; federation, replication and smart remote pairs must match | [D] nuget-repositories, "Limitations on NuGet Enforce Layout" |
| JFrog recommends Enforce Layout for every new NuGet repository; clients that speak only v2, such as Chocolatey and PowerShellGet, do not need it | [D] migration-tool, "Best Practices for NuGet Normalization" |
| Enforce Layout needs the newer NuGet handler, `artifactory.package.handler.nuget=true`, opt-in on self-managed from 7.133.3 and already the SaaS handler | [D] [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed), 7.146.7 |
| The 7.146.7 known issues give `artifactory.package.handler.nuget=false` as a workaround for upgrades to 7.146.x, so the handler is on by default there; no note says when it changed | [D] known issues (RTDEV-84846); [I] the default |
| Strict SemVer validation of every NuGet package came in SaaS 7.125.3 and self-managed 7.125.4, linking NuGet's SemVer 2.0 sort order | [D] release notes |
| The migration tool normalises existing local, remote and smart remote repositories (SaaS [7.152.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71521-saas), self-managed 7.161.15) and virtual ones (SaaS 7.172.0, 7.172.1). It runs a dry run, moves packages that cannot be normalised to an isolation repository under their original names, needs Admin or Project Admin, and makes the repository unavailable while it runs | [D] migration-tool; release notes |
| No source shows the path a package is stored at under Enforce Layout, or whether a REST deploy under another name is renamed or refused | [I] |
| Downloads of a local repository's packages are case-insensitive after a restart since 7.31.10 | [D] known issues (fixed in 7.31.10) |
| A `+` in a version answered 404 on install until 7.104.5 (behind a system property from 7.90.17) | [D] 7.104.5, 7.90.17, RTDEV-49462; [J] RTFACT-30858 (remote), RTFACT-21126 (6.17.0, `Packages(Id=,Version=)`) |

## 11. The v2 OData feed

| Fact | Basis |
|---|---|
| The client reads the source root once, `GET <source>`, takes the URL it ended at after redirects, trims the query and trailing slash, and uses that as the feed's base; it does not look at the status unless the request fails outright | [S] `ODataServiceDocumentUtils.cs` |
| It reads `<base>/$metadata` for the `Packages` entity set, its `IsAbsoluteLatestVersion` property and a `Search` function import; a 404 or a document it cannot read leaves both assumed present | [S] `LegacyFeedCapabilityResourceV2Feed.cs` |
| Restore asks `Packages(Id='<id>',Version='<normalised version>')`, treating 404 as empty, then `FindPackagesById()?id='<id>'&semVerLevel=2.0.0`, where any status other than 200 or 204 is fatal | [S] `V2FeedParser.cs:110-182,518-624`, `V2FeedQueryBuilder.cs` |
| `BuildFindPackagesByIdUri` sends only `id` and `semVerLevel=2.0.0`, and exact `Packages(Id,Version)` sends no query parameters; prerelease range resolution therefore needs the former to retain prereleases, and an exact SemVer 2 lookup cannot require `semVerLevel` | [S] `V2FeedQueryBuilder.cs`, rechecked at 8.1.0.12 on 2026-10-21; native .NET 10.0.401 v2 prerelease range restore verified by #546 |
| Search is `Search()?[$filter=IsLatestVersion\|IsAbsoluteLatestVersion&][$orderby=Id&]searchTerm='<term>'&targetFramework='<tfms>'&includePrerelease=<bool>&$skip=&$top=&semVerLevel=2.0.0`; `Packages()` takes the same `$filter`, `$orderby`, `$skip` and `$top`, with a substring search over `Id`, `Description` and `Tags` when search is not supported | [S] `V2FeedQueryBuilder.cs` |
| The client follows `<link rel="next">` until none is left, and fails on a repeated `next` | [S] `V2FeedParser.cs:425-511,626-636` |
| It reads `entry`, `title`, `summary`, `author/name`, `content@src` (the download URL) and, under `m:properties`, the `d:` elements `Id`, `Version`, `Description`, `IconUrl`, `LicenseUrl`, `ProjectUrl`, `Tags`, `GalleryDetailsUrl`, `ReportAbuseUrl`, `Dependencies`, `RequireLicenseAcceptance`, `DownloadCount`, `Created`, `LastEdited`, `Published`, `PackageHash`, `PackageHashAlgorithm`, `MinClientVersion` | [S] `V2FeedParser.cs:28-58,328-387` |
| A version is unlisted when `Published` falls in 1901 or earlier | [S] `V2FeedPackageInfo.cs:219-225` |
| 7.x's entries download from `<base>/api/nuget/<repo>/Download/<Id>/<version>`, the id in its stored case | [J] RTFACT-18567, RTFACT-26555, RTFACT-31126 |
| `FindPackagesById()` pages with `$skip` and `$top`; a `next` link exists, and 7.38.0 fixed it vanishing under priority resolution | [J] RTFACT-21838, RTFACT-26719 (7.38.0) |
| `IsLatestVersion` on a local repository once picked the wrong four-part version; fixed in 2020 | [J] RTFACT-21838 (Done) |
| 6.20 defines `artifactory.nuget.ignoreIsLatestVersionFilter`, true by default; only the closed NuGet add-on reads it, so what it ignores is not shown | [S] 6.20 `ConstantValues.java:255`; [I] its effect |
| A missing package answered `{"errors":[{"status":404,"message":"Not Found"}]}` | [J] RTFACT-6799 (3.3.1) |
| `GetUpdates()` and `GetUpdates()/$count` were deprecated from 7.49.3; "you can use the `/search` API instead" | [D] [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations), "NuGet V2 APIs Deprecation Notice" |
| `priorityResolution` applies to every repository type except, among others, NuGet V2 | [D] configuration-json-files |
| Known issue RTDEV-84690: JAXB lock contention in v2 XML on 7.146.x caused NU1301 timeouts, "no impact on NuGet v3"; fixed in 7.146.13, workaround `artifactory.nuget.v2.search.cache.enabled = false` | [D] known issues; 7.146.13 |
| A v2 query through a virtual repository with an unreadable member answers 404, not 403 | [D] 7.146.25, RTDEV-88743 |
| v2 responses that "omitted required package metadata" broke PowerShell; which metadata is not said | [D] 7.161.26 and SaaS 7.168.0, RTDEV-95512 |
| A deleted package stayed in `FindPackagesById()` until a restart | [J] RTFACT-23229 (open, 6.16.0) |

## 12. Properties and reindex

| Fact | Basis |
|---|---|
| 6.20 names sixteen properties: `nuget.id`, `nuget.version`, `nuget.digest`, `nuget.title`, `nuget.authors`, `nuget.owners`, `nuget.summary`, `nuget.description`, `nuget.copyright`, `nuget.requireLicenseAcceptance`, `nuget.releaseNotes`, `nuget.projectUrl`, `nuget.licenseUrl`, `nuget.iconUrl`, `nuget.tags` and `nuget.dependency`, each the nuspec element of that name | [S] 6.20 `NuGetProperties.java` |
| The package view reads the name from `nuget.id`, the version from `nuget.version`, and the tags from `nuget.tags` split on spaces | [S] 6.20 `NugetMetadataProvider.java` |
| 7.x stores each dependency in `nuget.dependency` as `<id>:<version>:<target framework>`, the framework taken from the `<group>` only | [J] RTFACT-31474 (a customer's report, Done 2026) |
| `nuget.releaseNotes` was cut to 4000 characters | [J] RTFACT-21236 (a 6.17 log) |
| NuGet metadata "is automatically calculated when adding, moving, copying, or removing NuGet packages", asynchronously, after the action completes; properties set straight after a push can be lost. artifactweave makes a version visible once its deploy commits, following JFrog's intent that a pushed package resolves, which is not a deviation (decision [0038](decisions/0038-nuget.md#storage-and-the-package-index)) | [D] nuget-repositories, "Metadata Updates"; [J] RTFACT-29222 (open, 7.46.11) |
| A deploy by a user without Read was "Removed from indexing" for want of a NuGet id | [J] RTFACT-26971 (deferred) |
| JFrog documents no list of package properties; the request is open | [J] RTFACT-31054 |
| `POST /api/nuget/{repoKey}/reindex` "Recalculates all the NuGet packages for this repository (local/cache/virtual), and re-annotate the NuGet properties for each NuGet package according to its internal nuspec file", for local, local-cache, remote and virtual repositories; 200 `text/plain`, 401, 403, 500 | [D] [calculatenugetrepositorymetadata](https://docs.jfrog.com/artifactory/reference/calculatenugetrepositorymetadata.md) |
| No source shows the values 7.x writes for `nuget.digest` and `nuget.version`, whether it removes a property whose element a later nuspec lacks, or whether its feeds read the properties or the nuspec | [I] |

## 13. `maxUniqueSnapshots`

| Fact | Basis |
|---|---|
| A NuGet local repository has "Max Unique Snapshots: The maximum number of unique snapshots of a single artifact to store. When the number of snapshots exceeds this value, older versions are removed. A value of 0 (default) indicates there is no limit" | [D] local-repositories, "Additional Settings for NuGet Local Repositories" |
| The Terraform provider's `artifactory_local_nuget_repository` has `max_unique_snapshots` and `force_nuget_authentication` | [S] provider v12.11.14 |
| Max Unique Snapshots supports Maven, NuGet, Gradle, Ivy, Docker (as Max Unique Tags) and SBT, keeps at least two, and deletes in a background process every five minutes | [KB] 000005403 |
| 6.20 applies the limit to every package type through the repository's layout: a created file whose layout reading is an integration revision marks its organisation, module and base revision; the cleanup deletes the oldest files by creation, per classifier, while more than the limit remain | [S] 6.20 `IntegrationCleanerInterceptor.java`, `IntegrationCleanupServiceImpl.java` |
| Under `nuget-default` a pre-release label is the file integration revision, so a NuGet pre-release is a unique snapshot of its id and release version (section 10) | [S] the 7.38.8 export; [I] that 7.x reads it so |
| 6.16 deleted NuGet pre-releases in a two-folder layout by it | [J] RTFACT-23229 |
| JFrog's 2025 SaaS reproduction pushed `HelloWorldLib.1.0.0-alpha.1.nupkg` to `.4` to the root of a repository with a limit of 2 and nothing was deleted; the issue is Done with no fix version | [J] RTFACT-31210 |
| Resetting the limit to 0 deleted queued Maven snapshots until SaaS 7.164.0 | [D] SaaS 7.164.0, RTDEV-93991 |

## 14. Repository configuration

| Fact | Basis |
|---|---|
| A local repository's JSON has `maxUniqueSnapshots` (default 0) and `forceNugetAuthentication` (default `false`) | [D] configuration-json-files; createrepository |
| No REST schema, configuration JSON or Terraform field carries Enforce Layout | [D] createrepository, getrepositoryconfigurationv2, configuration-json-files; [S] provider v12.11.14 |
| The provider's local NuGet resource sends `maxUniqueSnapshots` and `forceNugetAuthentication` beside the common local fields and `repoLayoutRef` | [S] `resource_artifactory_local_nuget_repository.go:49-126` |
| A NuGet virtual repository on 7.x shows `repoLayoutRef: nuget-default` and `forceNugetAuthentication` | [J] RTFACT-29322 |

## 15. Release-note history from 7.84

| Release | NuGet change | Basis |
|---|---|---|
| 7.84.10 | Up to 23% faster; 401 instead of 500 to wrong .NET credentials (RTDEV-37982); indexing failures (RTDEV-41880); PowerShell search in virtual repositories (RTDEV-35895) | [D] |
| 7.90.5 | NuGet joins package archiving; `*.symbols.nupkg` no longer indexed (RTDEV-41067) | [D] |
| 7.104.5 | `+` in a version no longer 404s on install (RTDEV-49462) | [D] |
| 7.104.14 | `symbols.nupkg` no longer indexed by a reindex and restart (RTDEV-54891) | [D] |
| 7.111.4 | Chocolatey and PowerShell clients | [D] |
| 7.117.5 | `nuget search` through a virtual repository with a GitHub remote (RTDEV-57071) | [D] |
| 7.125.4 | .NET CLI support, curation for virtual repositories, strict SemVer validation, a search rate limit | [D] |
| 7.133.3 | The newer NuGet handler, opt-in (`artifactory.package.handler.nuget=true`) | [D] |
| 7.146.7 | Enforce Layout; known issues RTDEV-84690 and RTDEV-84846 | [D] |
| 7.146.13 | Database lock guidance (`hqc:nuget_hqc_`, `artifactory.db.lock.native.pool.size`); RTDEV-84690 fixed | [D] |
| 7.146.22 to 7.146.35 | Version sorting memory (RTDEV-88396); virtual v2 404 (RTDEV-88743); SQL Server queries (RTDEV-93602); CVE-2026-66378 | [D] |
| 7.161.15 | Migration tool; remote `repository-signatures`; Chocolatey cache and four-hour minimum | [D] |
| 7.161.16 to 7.161.26 | CVE-2026-66378; Oracle search (RTDEV-93752); curation log noise (RTDEV-97689); v2 metadata for PowerShell (RTDEV-95512) | [D] |
| SaaS 7.164.0 to 7.174.0 | CVE-2026-66378 and MySQL search (7.164.0); RTDEV-95512 (7.168.0); virtual repositories in the migration tool (7.172.1). 7.174.0 changes nothing for NuGet | [D] |

## 16. Corrections to earlier research

| Was | Now |
|---|---|
| Research 04: `SearchQueryService` with `/3.0.0-beta`, `/3.0.0-rc` and `/3.5.0` "required" | The client asks for `/Versioned`, `/3.4.0` and `/3.0.0-beta`, and `/3.5.0` only for the package type filter (section 3) |
| Research 04: the flat container's versions are the package's | They include unlisted versions (section 4) |
| Research 04: registration "pages once a package has more than 128 versions" | That is nuget.org's rule; 7.x links pages even for one version (section 5) |
| Research 04: publish answers "201 or 202, 409 on duplicate" | 201. Without Enforce Layout a duplicate release answers 403 to a user without Delete, and a pre-release is overwritten with Deploy alone; under Enforce Layout a duplicate answers 409 (section 7) |
| Research 04: V2 downloads at `/package/<id>/<ver>` | 7.x's are `/Download/<id>/<version>` (section 11) |
| Research 04: "the storage layout is free unless enforce layout is on (`<id>/<version>/<id>.<version>.nupkg`)" | A push stores `[<folder>/]<id>.<version>.nupkg`; no source shows a different path under Enforce Layout (section 10) |
| Research 04: properties `nuget.id` and `nuget.version` | Sixteen `nuget.*` keys from the nuspec, `nuget.dependency` as `<id>:<version>:<framework>` (section 12) |
| Research 04: "Nothing in this section is unverified" | Section 17 lists what is |
| Research 03: `maxUniqueSnapshots` belongs to NuGet **[I]** | Confirmed: the UI, the provider and KB 000005403 (section 13) |
| Research 03: `nuget-default` and the paths it reads | A package pushed to the root matches no `nuget-default` path (section 10) |
| Research 10: the NuGet error envelope is "text or XML" | 7.x answers its NuGet routes in the Artifactory envelope (sections 9 and 11); the client reads no error body (section 7) |

## 17. Inferred and undocumented

Each item below is on #216 for the live comparison in P19:

1. The service index: the `PackageBaseAddress`, `PackagePublish` and autocomplete URLs, the
   full resource list and whether it lists `LegacyGallery` (section 3).
2. Under Enforce Layout, the stored path of a pushed package and whether a REST deploy whose
   name is not normalised is renamed or refused, and with what status and message (section 10).
3. Registration: the page size, when 7.x inlines leaves, whether it ever writes `published`,
   and whether the `registration` hive hides SemVer 2 packages (section 5).
4. Search and autocomplete: how terms are matched, the order, the `take` cap, the rate limit's
   thresholds and what it answers when hit, and the autocomplete URL and answer (section 6).
5. The bodies of push and delete answers, the status of a delete, what a relist answers, and
   whether a pre-release can still be pushed over under Enforce Layout (section 7).
6. Whether a symbols push needs its package, and what a second push of the same symbols
   answers; symbol-key collision ownership and deletion, malformed PDB handling, filename
   case and extraction limits (section 8).
7. What 7.x answers a key without a user, `X-NuGet-ApiKey` beside an `Authorization` header,
   and a bearer token on its NuGet routes today (section 9; RTFACT-20340, RTFACT-29362).
8. Whether Force Authentication covers every route under `api/nuget/<repo>` and
   `api/nuget/v3/<repo>`, and whether the system property `artifactory.nuget.forceAuthentication`
   still exists on 7.x (section 9).
9. When the newer handler became the self-managed default (section 10).
10. Whether 7.x writes all sixteen of 6.20's `nuget.*` keys, the values of `nuget.digest` and
    `nuget.version`, whether a property a later nuspec lacks is removed, and whether the feeds
    read the properties or the nuspec (section 12); and the `nuget.dependency` format, which
    only a customer's report shows (RTFACT-31474).
11. Whether 7.x applies `maxUniqueSnapshots` to NuGet packages at the repository root, which
    RTFACT-31210 found it does not and which JFrog closed as Done (section 13).
12. What `FindPackagesById()` answers for an id with no versions, the v2 page size, and the
    `d:` properties RTDEV-95512 added (section 11). Also compare #546's bounded query
    refusals, default prereleases on `Packages()`, total `$top` allowance across pages,
    and the filtered count before paging (decision 0038's v2 section).
13. Whether `Packages(Id,Version)` and `FindPackagesById()` match ids and versions ignoring
    case on 7.x, as the 7.31.10 download fixes suggest (sections 5 and 11).
14. What 7.x answers `GetUpdates()` since its deprecation (section 11).

## Native exit evidence (#547)

The pinned .NET 10 and 11 SDK images run NuGet.Protocol through `dotnet`; neither
image includes the separate `nuget.exe`/Mono runtime. Consequently `nuget.exe list`
and `jf nuget restore` remain outside this native matrix, rather than being
represented by a renamed dotnet command. Microsoft documents the Linux legacy
client's [Mono requirement](https://learn.microsoft.com/nuget/install-nuget-client-tools).

JFrog CLI 2.126.0's installed help and [dotnet documentation](https://docs.jfrog.com/artifactory/docs/jf-dotnet)
confirm `JFROG_RUN_NATIVE=true` supports `jf dotnet nuget push` and
`jf dotnet restore --repo-resolve`. The acceptance feature uses these actual
commands on both SDK images and reads the subsequently published artifact and
dependency build records from the build API. In a local 2.126.0 native-mode probe,
`--nuget-v2` still issued v3 requests; the v2 proof therefore names an explicit
`NuGet.Config` with `protocolVersion="2"` and asserts the canonical v2 download
request. A successful restore alone is not evidence of which protocol ran.

## 18. Artifactory 7.x check

Checked against Artifactory 7.161.32 and SaaS 7.174.0 on 2026-10-19 ([#528](https://github.com/weaveplatform/artifactweave/issues/528)).

**Sources.** JFrog's documentation pages named at the top of this report, read on 2026-10-19;
the release notes, self-managed 7.84.3 to 7.161.32 and SaaS 7.80.0 to 7.174.0; the known issues
and the deprecations; the KB articles and Jira issues cited; and the JFrog clients' and the
NuGet client's sources at the commits given. JFrog no longer publishes the notes for 7.0 to
7.83.

**Confirmed for 7.x** (research 04's claims):
- The v3 service index at `api/nuget/v3/<repo>/index.json` and without the file name, and the
  v2 feed at `api/nuget/<repo>` (section 2).
- Push with `multipart/form-data` and `X-NuGet-ApiKey: <user>:<password or token>` read as
  Basic credentials (sections 7 and 9).
- Symbols at `api/nuget/v3/<repo>/symbols` (section 8).
- Force Authentication per repository (section 9), SemVer 2 hidden from clients before 4.3.0
  (section 6), and `PackageBaseAddress` only under Enforce Layout (section 4).
- `nuget.id` and `nuget.version` among the properties (section 12).

**Changed or added in 7.x:**
- The newer handler (7.133.3) and Enforce Layout (7.146.7, SaaS 7.139.1), which normalises
  versions, refuses duplicates with 409 and opens `PackageBaseAddress` (section 10).
- The migration tool (7.161.15, SaaS 7.152.1; virtual repositories SaaS 7.172.1).
- Strict SemVer validation and a search rate limit (7.125.4, SaaS 7.125.3).
- `repository-signatures`, the Chocolatey cache minimum and remote metadata caching
  (7.161.15): inputs for P9 and P10 only.
- `GetUpdates()` deprecated (7.49.3).

**Corrected:** research 04's search types, flat container listing, registration paging, push
statuses, download path, storage path and properties, and research 03's `maxUniqueSnapshots`
note and `nuget-default` reading (section 16).

**Unconfirmed for 7.x:** the items in section 17, which are on
[#216](https://github.com/weaveplatform/artifactweave/issues/216). The 6.20 claims on the
property keys, the cleanup through the layout and the Force Authentication fallbacks are
among them where no 7.x source repeats them.
