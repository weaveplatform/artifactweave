# 0038: NuGet local repositories

Status: Proposed

Checked against: **Artifactory 7.161.32** (SaaS 7.174.0) on 2026-10-19 ([research 29 section 18](../29-nuget.md#18-artifactory-7x-check), [#528](https://github.com/weaveplatform/artifactweave/issues/528)).

## Context

`dotnet`, `nuget.exe`, Chocolatey, PowerShell and the JFrog CLI publish and restore NuGet
packages through two protocols that Artifactory serves side by side: the v3 JSON API under
`/artifactory/api/nuget/v3/<repo>/` and the older v2 OData feed at
`/artifactory/api/nuget/<repo>/`, which also takes pushes and deletes. Artifactory 7.x added a
newer NuGet handler and, on top of it, Enforce Layout: a choice made when a repository is
created that normalises versions, refuses duplicates and opens the flat container
(`PackageBaseAddress`), which modern clients restore from. Decision
[0032](0032-package-format-handlers.md) fixes how a format plugs into the item service and the
router kit, decision [0018](0018-package-index.md) the package index, and decision
[0033](0033-persistent-work-queue.md) the work queue. This decision fixes what a NuGet local
repository stores, what both protocols answer and who may ask.
[Research 29](../29-nuget.md) holds the facts, cited below as R29 and a section number.

## Decision

### The normalised layout

Every NuGet local repository behaves as one created with Enforce Layout, and there is no
switch (DV-111, which #542 registers). JFrog documents no repository field for it: the REST
schema, the configuration JSON and the Terraform provider have none, and the UI sets it only at
creation (R29 10, 14). In practice:
- **Versions are normalised** by NuGet's rules, as JFrog's migration tool and Microsoft
  describe them (R29 10): leading zeros dropped, at least three numeric parts, a fourth part of
  0 dropped, build metadata dropped, the pre-release label kept with its case. `1.0` is
  `1.0.0`, `1.2.3.0` is `1.2.3`, `1.2.3.1` stays, and `1.0.0+abc` is `1.0.0`.
- **One file per version.** A package id, compared ignoring case, and a normalised version
  name at most one `.nupkg` in a repository. A second one is refused with 409, whichever
  route brings it (below).
- **The flat container** is always served (below).
- **Only valid packages are stored.** A `.nupkg` written to the repository must carry a
  readable `.nuspec` at the root of its zip, an id NuGet accepts (`^\w+([.-]\w+)*$`, at most
  100 characters) and a version NuGet can parse. "Packages that do not follow SemVer naming
  conventions cannot be published/uploaded to a normalized NuGet repository" (R29 10).

The read of the package is bounded as npm's is (decision [0034](0034-npm.md)): a zip of more than
2^20 entries, or a nuspec over 1 MiB, is not a valid package.

### Storage and the package index

- **The canonical path** of a package is `[<folder>/]<id>.<version>.nupkg`: the nuspec's id
  with its own case, then the normalised version. A push without a folder stores it at the
  repository root, which is where 7.x puts it and where `jf nuget push` looks for it to stamp
  its build properties (R29 1, 10). A push to `api/nuget/<repo>/<folder>` stores it in that
  folder.
- **Other routes.** A REST deploy, a checksum deploy, an archive's entry, a copy and a move
  that write a `.nupkg` into a NuGet local repository go through the same rules in `Admit`,
  inside the write's transaction:
  - a file that is not a valid package, or whose file name is not its canonical file name
    compared ignoring case, answers 409 with `The repository '<repo>' rejected '<path>':
    <reason>`. JFrog documents 409 for a name that breaks the conventions; the message is
    inferred **[I]**;
  - a version the repository already holds at another path answers 409, `The repository
    '<repo>' already contains <id> <version>`;
  - a write at the version's own path is an overwrite under the item routes' rules: it needs
    Delete, and the new content is read and indexed again. Its properties are cleared as for
    every overwrite (decision [0004](0004-storage-sql-metadata-and-blobs.md)) and set again
    from the new nuspec.
- **`.snupkg`** files follow the same rules, with `<id>.<version>.snupkg` as their canonical
  name, and are recorded as the symbols of their version (under "Symbols").
- **`.symbols.nupkg`** files, the legacy symbol packages, and every other file are stored as
  plain items, never indexed and never refused: 7.x stopped indexing legacy symbol packages
  in 7.90.5 (RTDEV-41067, R29 8).
- **The package index** (decision 0018): the package is the id lower-cased; its display id is
  the nuspec id of its newest version. The version is the normalised version, ordered by
  `types.CompareNuGet`. A version's files are its `.nupkg` and, when present, its `.snupkg`.
- **The per-version document** (`PackageVersion.Metadata`) caches the nuspec as stored, and
  what the feeds need from it: id, title, authors, owners, description, summary, release
  notes, copyright, language, tags, project, icon and licence URLs, licence expression,
  `requireLicenseAcceptance`, `minClientVersion`, dependency groups (target framework, id and
  range as written), package types and readme; the version as the nuspec wrote it; the
  package's SHA-512 and size; whether the version is SemVer 2 (under "Search and
  autocomplete"); and its deploy time. The feeds read the index and this document, never the
  properties.
- **Visibility.** A version appears in every feed once its deploy commits: the hooks keep
  the index current in the write's transaction. 7.x calculates NuGet metadata
  asynchronously today, so a client may not see a push at once (R29 12). JFrog intends a
  pushed package to be resolvable, and the delay is an artefact of its implementation, so
  this is not a deviation.

### Properties

On each indexed `.nupkg`, the format sets the `nuget.*` properties that Artifactory 6.20
defines, from the nuspec (R29 12). They need no Annotate.
- `nuget.id` (the nuspec id), `nuget.version` (the normalised version **[I]**), `nuget.title`,
  `nuget.authors`, `nuget.owners`, `nuget.summary`, `nuget.description`, `nuget.copyright`,
  `nuget.releaseNotes`, `nuget.projectUrl`, `nuget.licenseUrl`, `nuget.iconUrl` and
  `nuget.tags` (the nuspec's space-separated string), each one value;
- `nuget.requireLicenseAcceptance`, `true` or `false`;
- `nuget.digest`, the package's SHA-512 in base64, as the v2 feed's `PackageHash` gives it
  **[I]**;
- `nuget.dependency`, one value per dependency, `<id>:<range>:<target framework>`, with an
  empty framework outside a `<group>`, as RTFACT-31474 shows 7.x writing it.

An element the nuspec lacks leaves its property unset, and a reindex removes one that a
previous content set. Values are cut to 2400 UTF-16 code units, as for every property (#338).
Properties a user sets, the JFrog CLI's `build.*` among them, are left alone. Editing a
`nuget.*` property changes no feed, and a reindex writes it back from the nuspec.

### The v3 service index

`GET` or `HEAD` of `/api/nuget/v3/{repoKey}/index.json`, `/api/nuget/v3/{repoKey}` or
`/api/nuget/v3/{repoKey}/` answers `application/json`
`{"version":"3.0.0","resources":[...]}`, one entry per `@type`, each `{"@id","@type"}`. `<v3>`
below is `<base>/api/nuget/v3/<repo>` and `<v2>` is `<base>/api/nuget/<repo>`, both built from
the request's base URL (decision [0002](0002-spec-first-flat-crud-handwritten-item-paths.md)).

| `@id` | `@type` values |
|---|---|
| `<v3>/flatcontainer/` | `PackageBaseAddress/3.0.0` |
| `<v3>/registration/` | `RegistrationsBaseUrl`, `RegistrationsBaseUrl/3.0.0-beta`, `RegistrationsBaseUrl/3.0.0-rc`, `RegistrationsBaseUrl/3.4.0` |
| `<v3>/registration-semver2/` | `RegistrationsBaseUrl/3.6.0` |
| `<v3>/query` | `SearchQueryService`, `SearchQueryService/3.0.0-beta`, `SearchQueryService/3.0.0-rc`, `SearchQueryService/3.5.0` |
| `<v3>/autocomplete` | `SearchAutocompleteService`, `SearchAutocompleteService/3.0.0-beta`, `SearchAutocompleteService/3.0.0-rc`, `SearchAutocompleteService/3.5.0` |
| `<v2>` | `PackagePublish/2.0.0`, `LegacyGallery/2.0.0` |
| `<v3>/symbols` | `SymbolPackagePublish/4.9.0` |

- The registration hives and the search are at 7.x's paths, and `SymbolPackagePublish` is
  7.x's URL (R29 3). The flat container, autocomplete and publish URLs are not shown by any
  source and are chosen here **[I]**. The publish URL is the v2 root, where JFrog documents
  pushes and deletes (R29 2).
- **Left out:** `Catalog/3.0.0` (nuget.org's own), `RepositorySignatures` (7.x serves it only
  for remote repositories, and nothing here signs), `VulnerabilityInfo` (no data to serve),
  the URI templates (`ReportAbuseUriTemplate`, `PackageDetailsUriTemplate`,
  `ReadmeUriTemplate`, `OwnerDetailsUriTemplate`, which point at web pages artifactweave does
  not have), `PackageStaging/1.0.0` and every `/Versioned` entry. The client needs none of
  them (R29 3).

### Flat container

- `GET {@id}{lower id}/index.json` answers `{"versions":[...]}`: the versions the caller may
  read, normalised and lower-cased, ascending. Every version is listed, since none is ever
  unlisted (under "Delete and relist"). A package with no such version answers 404, which the
  client reads as "no such package" (R29 4).
- `GET {@id}{lower id}/{lower version}/{lower id}.{lower version}.nupkg` serves the version's
  `.nupkg` through `ServeItem`, so the download policy and counts apply.
  `GET {@id}{lower id}/{lower version}/{lower id}.nuspec` answers the cached nuspec as
  `application/xml`.
- The id and version segments are matched ignoring case, and the version is normalised before
  the lookup: the .NET client sends a listed version in the case it was listed (R29 4).
- `HEAD` is served wherever `GET` is.

### Registration

- **Two hives.** `<v3>/registration/` leaves out SemVer 2 versions and `<v3>/registration-semver2/`
  includes them, as Microsoft defines `RegistrationsBaseUrl` and `/3.6.0` (R29 5). Both answer
  with `Content-Encoding: gzip` when the request accepts it, which covers the `/3.4.0` alias;
  the NuGet client accepts gzip on every request (R29 1).
- **The index**, `{hive}{lower id}/index.json`, answers `{"count","items":[...],"@id"}`. Its
  pages are never inlined, as on 7.x (RTFACT-30380, R29 5). Each page holds up to 128
  versions in NuGet order **[I]**, as `{"@id":"{hive}{lower id}/page/{lower}/{upper}.json",
  "count","lower","upper"}`. A package with no version the caller may read in that hive
  answers 404.
- **A page**, `{hive}{lower id}/page/{lower}/{upper}.json`, is answered for any bounds, as 7.x
  answers them (R29 5): every visible version from `lower` to `upper` inclusive, the bounds
  compared as NuGet versions ignoring case, as
  `{"@id","count","lower","upper","parent","items":[...]}`. Bounds that are not versions, or
  that hold no visible version, answer 404.
- **A leaf in a page** is `{"@id","catalogEntry","packageContent","registration"}`:
  - `@id` is `{hive}{lower id}/{lower version}.json`;
  - `packageContent` is `{hive}Download/{id}/{version}`, 7.x's form (R29 5), which serves the
    `.nupkg` through `ServeItem`;
  - `registration` is `{hive}{lower id}/index.json`. The hive is named once: the doubled
    `registration-semver2/registration-semver2/` that RTFACT-30380 shows is a 7.x bug, and
    nothing a client reads depends on it.
- **`catalogEntry`** has 7.x's fields (R29 5): `id`, `version`, `authors` (the nuspec's
  string), `description`, `listed` (always `true`), `requireLicenseAcceptance`,
  `packageContent` and `dependencyGroups`, each group `targetFramework` and `dependencies` of
  `id`, `range` (as the nuspec writes it) and `registration`. It adds `@id` (the leaf URL),
  which Microsoft requires, and, when the nuspec has them, `title`, `summary`, `tags`,
  `iconUrl`, `licenseUrl`, `licenseExpression`, `projectUrl`, `language` and
  `minClientVersion`. It also has `published`, the version's deploy time in ISO 8601 UTC.
  7.x leaves it out today, a gap JFrog tracks as an open request (RTFACT-27465,
  RTFACT-30380), and the field is in Microsoft's schema, so filling it is not a deviation.
- **The leaf document**, `GET {hive}{lower id}/{lower version}.json`, answers
  `{"@id","listed","packageContent","registration","published"}`, the fields 7.x's leaf has
  (RTFACT-27465) and `published`.
- **Computed on read** from the package index and the per-version documents, for the caller
  and the request's base URL. Nothing is stored and no work is queued, so the registration
  needs no `EnsureFresh`. Decisions 0032 and 0033 expected NuGet's registration pages to be
  queued derived work; this decision settles them as computed on read, as npm's packuments
  are.

### Search and autocomplete

- **Visible versions** in search, autocomplete, the registration and the v2 feed are those the
  caller may read, filtered further by the request:
  - search results include a pre-release only when `prerelease=true` (v3) or
    `includePrerelease=true` (v2 `Search()`); restore queries retain prereleases, as below;
  - a SemVer 2 version only when `semVerLevel` is 2.0.0 or more. A version is SemVer 2 when
    the version its nuspec wrote has a dot-separated pre-release label or build metadata, or
    when a dependency range has a bound that does, as Microsoft defines it (R29 6). The SemVer
    2 filter is not optional: 7.x's `artifactory.nuget.disableSemVer2SearchFilterForLocalRepos`
    has no equivalent.
- **`GET <v3>/query`** takes `q`, `skip`, `take`, `prerelease`, `semVerLevel` and
  `packageType`, and ignores `includeDelisted` and `supportedFramework`:
  - each whitespace-separated term of `q` must appear, ignoring case, in the id, title,
    description or tags of the package's newest visible version; an empty `q` matches every
    package **[I]**;
  - `packageType` keeps packages that declare a package type of that name, ignoring case; a
    package that declares none is `Dependency` **[I]**;
  - results are ordered by id ignoring case **[I]**; `skip` defaults to 0, `take` to 20 and is
    capped at 1000, nuget.org's cap **[I]**; a value that is no number takes its default;
  - the answer is `{"totalHits","data":[...]}`, each result with `id`, `version` (the newest
    visible), `description`, `summary`, `title`, `iconUrl`, `licenseUrl`, `projectUrl`,
    `tags` (the nuspec's split at spaces) and `authors` (split at commas) as arrays,
    `registration` (the index in the semver2 hive when
    `semVerLevel` is 2.0.0 or more, else in the other), `totalDownloads`, `packageTypes`
    (`[{"name"}]`) and `versions` (`version`, `downloads`, and `@id`, the leaf). Fields the
    nuspec lacks are left out; `owners` and `verified` are never sent.
- **`GET <v3>/autocomplete`** with `q` answers `{"totalHits","data":[ids]}`: the display ids,
  ordered, of packages with a visible version whose id starts with `q`, ignoring case
  **[I]**, which is how the client uses it (R29 6). With `id` it answers the visible versions
  of that id, ascending, in the same shape **[I]**. `skip` and `take` as for search.
- **No rate limit.** 7.x rate-limits its NuGet search from 7.125.4 without documenting the
  thresholds (R29 6); artifactweave applies none.

### Push

- **Routes:** `PUT /api/nuget/{repoKey}` with or without a trailing slash stores at the
  repository root, and `PUT /api/nuget/{repoKey}/{folder...}/` in that folder, at any depth
  (R29 2).
- **Body:** `multipart/form-data`, read as a stream through the router kit's multipart helper
  (#532), chunked transfer included. The first file part is the package and later parts are
  ignored, as Microsoft specifies; a request with no such part answers 400. The size limit is
  the deploy path's.
- **Order of checks:**
  1. the caller, from `X-NuGet-ApiKey` or the request's credentials (under "Authentication");
  2. Deploy somewhere in the target folder, before the body is read, as npm's publish is
     checked first; a caller who may deploy nothing there is refused, 401 to an anonymous
     caller with the Basic challenge, 403 to another;
  3. a valid package (under "The normalised layout"), else 400, Microsoft's status for an
     invalid package; a symbols package (one whose nuspec declares the `SymbolsPackage` type)
     is refused with 400 here, since it belongs at `<v3>/symbols`;
  4. Deploy on the canonical path;
  5. a version the repository already holds answers 409 to every caller, administrators
     included, inside the write's transaction. It is what `--skip-duplicate` accepts and what
     Enforce Layout answers (R29 7);
  6. the item routes' own refusals (a blacked-out repository, the include and exclude
     patterns) with their statuses.
- **Success** answers 201 with no body, the status 7.x answers (R29 7); its body is inferred
  **[I]**.

### Delete and relist

- **`DELETE /api/nuget/{repoKey}/{id}/{version}`** removes the version: its `.nupkg` and its
  `.snupkg`, through the trash can. The id is matched ignoring case and the version is
  normalised. It is a hard delete, as on 7.x (RTFACT-16148, R29 7).
  - Delete is needed on each file, checked for all before any goes.
  - 204 with no body **[I]**, or 404 when there is no such version the caller may read, so a
    hidden version's existence is not revealed.
  - Any other number of path segments answers 404, as 7.x answers a delete through a folder
    (RTFACT-23310).
- **There is no unlisted state.** 7.x has none: unlisting and deprecation are open requests,
  and its registration always shows `listed: true` (R29 7). Every version is listed, no route
  unlists one, and no property does.
- **`POST /api/nuget/{repoKey}/{id}/{version}`**, the relist, answers 200 with no body to a
  caller with Annotate on the version, since the version is already listed and nothing changes
  **[I]**; 404 when there is no such version the caller may read.

### Symbols

- **`PUT /api/nuget/v3/{repoKey}/symbols`**, with or without a trailing slash, takes a symbols
  package in the push's multipart body. Anything else answers 400.
- It is stored at `<id>.<version>.snupkg` at the repository root, "flat and unrenamed" as 7.x
  stores it (R29 8), with the same checks as a push: Deploy on that path, and 201 with no body.
- Its package need not be present: JFrog documents pushing symbols alone (R29 8) **[I]**.
- A version that already has symbols answers 409 **[I]**. When `--skip-duplicate` skips a
  package, the client does not push its symbols (R29 7).
- **Symbol-server reads** (`<v3>/<file>.pdb/<key>/<file>.pdb`) and the `.symbols/` folder of
  extracted PDBs that 7.x keeps are not served in P8 (R29 8). Those paths answer 404.

### Authentication

- **`X-NuGet-ApiKey`** (#532) is read only on paths under `/artifactory/api/nuget/`, and only
  when the request carries no `Authorization` header. Its value is split at the first `:` into
  a user and a secret, which authenticate exactly as Basic credentials `user:secret` do: a
  password, an API key, or an access or reference token, with the same lock, suspension and
  expiry rules (decision [0005](0005-authentication-and-tokens.md)).
  - A value with no `:`, or with an empty user or secret, answers 401 with the Basic
    challenge, as wrong credentials do (RTDEV-37982). JFrog does not support a key alone
    (RTFACT-20340, R29 9). The client then retries with its stored credentials, if any.
  - The key is never logged. The access log records the user it named.
- **Bearer tokens** authenticate on the NuGet routes as everywhere else. RTFACT-29362 records
  7.50.3 refusing one there as anonymous, a bug closed without a fix (R29 9).
- **Force authentication.** On a repository with `forceNugetAuthentication: true`, an anonymous
  request to any route under `/api/nuget/{repoKey}/` or `/api/nuget/v3/{repoKey}/` answers 401
  with `WWW-Authenticate: Basic realm="Artifactory Realm"` before it is routed, whatever the
  anonymous user may read. That covers the symbols routes (RTFACT-29479). The item routes,
  `/artifactory/{repoKey}/...`, are not affected **[I]**.
- **Without it,** an anonymous request follows the permissions: feeds show what the anonymous
  user may read, and a download it may not read is refused with 401 and the challenge, as the
  item routes refuse it. With anonymous access on, a repository the anonymous user cannot read
  therefore needs force authentication before a NuGet client sends credentials, as JFrog
  documents (R29 9). 6.20's system property fallback, `artifactory.nuget.forceAuthentication`,
  and its anonymous root `GET`, `artifactory.nuget.allowRootGetWithAnon`, have no equivalent.

### The v2 OData feed

The subset `NuGet.Protocol` sends for restore and search (R29 11), with the rest of OData
out of scope (research 10):
- **The service document** at `GET /api/nuget/{repoKey}` and `.../` (`application/xml`), an
  AtomPub workspace with the `Packages` collection.
- **`$metadata`**, an EDMX document with the entity type `V2FeedPackage` and its properties
  (below), the entity set `Packages`, and the function imports `Search` and `FindPackagesById`.
- **`Packages(Id='<id>',Version='<version>')`**, one entry (`application/atom+xml;type=entry`).
  The id is matched ignoring case; the version is normalised, so build metadata after `+`,
  sent encoded or not, is dropped. A version the caller may not read, or none, answers 404,
  which the client treats as empty. Exact lookups include SemVer 2 versions without a
  `semVerLevel` parameter: the client sends none on this request.
- **`FindPackagesById()?id='<id>'`**, a feed (`application/atom+xml;type=feed`) of every
  visible version of the id, ascending. An id with no such version answers 200 with an empty
  feed, since the client fails on any other status here (R29 11) **[I]**. Prereleases are
  included unless `includePrerelease=false` is explicit: the client's restore query sends
  only `id` and `semVerLevel`, and must resolve prerelease ranges. `Packages()` likewise
  retains prereleases, while `Search()` defaults to stable versions.
- **`Search()?searchTerm=&targetFramework=&includePrerelease=`**, the visible versions of the
  packages the v3 search matches; `targetFramework` is ignored **[I]**.
- **`Packages()`** and `Packages`, every visible version.
- **Query options** on those three: `$filter` with `IsLatestVersion` or
  `IsAbsoluteLatestVersion`, and, on `Packages()`, the `substringof` clauses over `Id`,
  `Description` and `Tags` that the client builds; `$orderby=Id`; `$skip`; `$top`;
  `semVerLevel`. The substring expressions accept only the client's `tolower`,
  `ne null`, `and`, `or` and parentheses, with at most 16 KiB and 32 nesting levels. Any other `$filter`,
  `$select`, `$expand` or `$` option, unsupported ordering, repeated query parameter,
  or invalid/negative paging number answers 400 **[I]**.
- **Paging:** at most 100 entries a feed, with `<link rel="next">` carrying the next `$skip`
  and all query options. `$top` limits the whole result: a next link subtracts the entries
  already delivered from its remaining allowance. Without `$top`, links continue until
  no matching versions remain **[I]**.
- **`$count`** after `Packages()`, `FindPackagesById()` and `Search()` answers the count as
  `text/plain`, after visibility and query filters, before `$skip` and `$top` **[I]**.
- **`Download/{id}/{version}`** serves the `.nupkg` through `ServeItem`.
- **`GetUpdates()`** and `GetUpdates()/$count` answer 404: JFrog deprecated them in 7.49.3
  (R29 11).
- **An entry** carries `id` (its `Packages(...)` URL), `title` (the id), `summary`, `updated`,
  one `author/name` per author, `content` (`type="application/zip"`,
  `src="<v2>/Download/<id>/<version>"`), and under `m:properties` the `d:` elements `Id`,
  `Version` and `NormalizedVersion` (both normalised), `Authors`, `Title`, `Description`, `Summary`,
  `ReleaseNotes`, `Copyright`, `Language`, `Tags`, `ProjectUrl`, `IconUrl`, `LicenseUrl`,
  `RequireLicenseAcceptance`, `Dependencies` (`id:range:framework` joined by `|`),
  `MinClientVersion`, `IsPrerelease`, `IsLatestVersion`, `IsAbsoluteLatestVersion`, `Listed`
  (true), `Created`, `LastUpdated` and `Published` (the deploy time), `DownloadCount` (the
  package's) and `VersionDownloadCount`, `PackageHash` (SHA-512 in base64),
  `PackageHashAlgorithm` (`SHA512`) and `PackageSize`.
- **`IsLatestVersion`** is the highest visible release of the id, and `IsAbsoluteLatestVersion`
  the highest visible version, both in NuGet order after permissions, prerelease and
  SemVer filtering, before applying latest/substring predicates and pagination.
- `GET` and `HEAD` are served for the service document, metadata, entries, feeds, counts
  and downloads. Download access uses the item service's checks and statistics.
- `priorityResolution` does not apply to the v2 feed (R29 11), an input for P10.

The restore and fallback query grammar was rechecked against
[`V2FeedQueryBuilder.cs` at 8.1.0.12](https://github.com/NuGet/NuGet.Client/blob/350273adcd4c/src/NuGet.Core/NuGet.Protocol/LegacyFeed/V2FeedQueryBuilder.cs),
and the Atom/EDMX fields against `V2FeedParser.cs` and
`LegacyFeedCapabilityResourceV2Feed.cs` at that same revision. The native .NET 10 v2
acceptance scenario resolves a prerelease range through `FindPackagesById()` with
`semVerLevel=2.0.0`, then downloads and compares the selected package bytes.

**For #216:** compare the inferred query refusals and parser bounds, default prerelease
behavior on `Packages()`, total `$top` allowance across pages, and filtered pre-pagination
counts with a live 7.x feed. R29 section 17 already tracks the unknown-id empty feed,
page size, exact-key case matching and deprecated `GetUpdates()` status.

### `maxUniqueSnapshots`

NuGet repositories apply `maxUniqueSnapshots`: JFrog lists NuGet among the types it supports,
and the setting is on NuGet local repositories in the UI and the Terraform provider (R29 13).
- **A unique snapshot** is a pre-release version. The pre-release versions of one package that
  share their numeric parts, such as `1.0.0-alpha.1` and `1.0.0-ci.7`, are the snapshots of
  one release, as `nuget-default` reads a pre-release label as an integration revision.
  Releases are never counted.
- **The mark.** A deploy of a pre-release `.nupkg` in a local repository whose
  `format.UniqueSnapshots` is above 0 queues the package's cleanup, kind `nuget.snapshots`
  keyed by the lower-cased id, in the deploy's transaction.
- **The job** is the periodic job of #512 (`work.Queue.HandleEvery`), run every
  `nuget.snapshot_cleanup_interval`, five minutes by default and at least one second, a new
  key in `internal/config/nuget.go`, as 7.x runs its cleanup every five minutes (KB
  000005403).
- **The rule.** Per release, while more pre-release versions remain than
  `format.UniqueSnapshots(c)` (at least two), the one deployed first goes, a tie broken by
  NuGet order. Its `.nupkg` and `.snupkg` are deleted through the item service in one
  transaction, into the trash can, as `_system_`'s. The limit is read when the job runs.
- **Wherever the package is stored.** 6.20 applies the limit through the repository's layout,
  and `nuget-default` reads no package at the repository root, where pushes land. RTFACT-31210
  found nothing deleted there, and JFrog closed it as Done (R29 13). Applying it by version
  follows the documented intent, so it is not a deviation.

### Reindex

`POST /api/nuget/{repoKey}/reindex` is the queued, Manage-gated operation of #516. For NuGet
it rebuilds the index and the per-version documents from every `.nupkg` and `.snupkg` the
repository holds, and sets each version's `nuget.*` properties from its nuspec again, as 7.x
"re-annotate[s] the NuGet properties for each NuGet package according to its internal nuspec
file" (R29 12). Legacy `.symbols.nupkg` files stay unindexed (RTDEV-54891).

### Permissions

| Operation | Needs |
|---|---|
| Service index, v2 service document, `$metadata` | Access to the repository: any authenticated caller, or anonymous when anonymous access is on and force authentication is off |
| Flat container version list, registration, search, autocomplete, v2 queries | Read on a version's `.nupkg`; versions the caller cannot read are left out |
| Downloads (flat container, registration `Download`, v2 `Download`), nuspec | Read on the version's `.nupkg` |
| Push | Deploy in the target folder, then on the canonical path |
| Symbols push | Deploy on the `.snupkg` path |
| Delete | Delete on the version's `.nupkg` and `.snupkg` |
| Relist | Annotate on the version's `.nupkg` |
| Reindex | Manage on the repository (#516) |

### Errors, other repositories and paths not served

- **The envelope.** Every NuGet route answers errors in the Artifactory envelope,
  `{"errors":[{"status":<int>,"message":"<string>"}]}`, as 7.x answers its NuGet routes
  (RTFACT-29362, RTFACT-6799); `NuGet.Protocol` reads only the status (R29 7, 11). The
  router kit's default for `/api/nuget/` is therefore right as it is, and research 10's row is
  corrected (R29 16). Every 401 carries the Basic challenge.
- **Other repositories.** A remote or virtual NuGet repository answers 501 in the envelope until
  P9 and P10 (decision 0032).
- **A repository keyed `v3`** is reached through its v3 tree only: `/api/nuget/v3/...` always
  routes to the v3 tree.
- **Not served,** answering 404: the symbol-server reads, the v2 queries under the v3 prefix
  that JFrog's support once suggested (R29 2), `GetUpdates()`, any other OData function, and a
  `package/{id}/{version}` download. A route's other methods answer 405 through the router kit.

## Rationale

**One normalised layout, always on.** `PackageBaseAddress` is one of Microsoft's four required
resources, the client restores from it when it is listed, and without it restores page through
the registration, which ran Artifactory out of memory on packages with many versions; a NuGet
engineer at Microsoft reports `dotnet tool install` from the .NET 9 SDK failing without it
(RTFACT-26507, R29 4). 7.x serves it only under Enforce Layout, which JFrog recommends for
every new repository, offering a migration tool to bring old ones across (R29 10). No source shows a repository field for it, so a switch would be
artifactweave's own invention, and every repository here is new. The non-normalised behaviours
are the ones customers file as bugs: a pre-release pushed over without Delete, versions that
normalise alike stored twice, and 403 where the protocol says 409 (R29 7). DV-111.

**The root path.** `[<folder>/]<id>.<version>.nupkg` is where 7.x stores a push and where the
JFrog CLI looks for it after `jf nuget push` (R29 1, 10). Any other path would break the CLI's
property stamping, and with it the build's link to its packages.

**409 for every duplicate.** It is Microsoft's status for an existing version, the only one
`--skip-duplicate` accepts, and what Enforce Layout answers (R29 7). Refusing administrators too
means a pushed version never changes under a client that has cached it; a REST overwrite at the
version's own path, which needs Delete, remains for repairs, as for npm.

**REST writes under the same rules.** Enforce Layout refuses names that break the conventions
(R29 10), and one canonical file per version is what lets the flat container, the registration
and the v2 `Download` find a version's file without guessing.

**The Artifactory envelope.** It is what 7.x answers on its NuGet routes, and the client never
reads an error body, so a NuGet-specific body would buy nothing (R29 7, 9, 11).

**The registration on read.** The index the deploy keeps current already holds everything a
page says, so a queued copy would add only lag. The answer depends on the caller, whose
unreadable versions are left out (7.x fixed private NuGet metadata reaching other users as
CVE-2026-66378, R29 6), and on the request's base URL, so a stored page would have to be
rebuilt per request anyway. Pages are linked rather than inlined, and served for any bounds,
because 7.x does both (R29 5).

**`published`.** nuget.org supplies the publish date, "which is visible in the Visual Studio
GUI, and useful for downstream tooling"; 7.x shows it in its own UI, and two open requests ask
for it in the API (RTFACT-27465, RTFACT-30380). The field is optional in Microsoft's schema,
so no client can fail on it. Filling a gap JFrog tracks as missing is not a deviation.

**Visible on commit.** The index is kept current in the write's transaction, as for npm, so a
second, delayed calculation would only add the delay that the JFrog CLI retries around (R29 1,
12). A pushed package is meant to be resolvable, so serving it at once follows JFrog's intent
and is not a deviation.

**`maxUniqueSnapshots` by version.** JFrog documents the setting for NuGet (KB 000005403, the
UI), and describes it as removing older versions. Applying it through `nuget-default` would
clean nothing that a client pushed, since every push lands at the root, which the layout cannot
read (R29 10, 13).

**Force authentication before routing.** The NuGet client sends credentials only after a 401
(R29 1). A route that answered an anonymous caller with an empty feed or a 404 would never
receive them, which is the behaviour force authentication exists to change.

**`Authorization` before `X-NuGet-ApiKey`.** A client sends its key on pushes and deletes and
its stored credentials only after a challenge (R29 1, 9). Reading the header first means a
malformed or stale key never hides credentials that would succeed.

**No unlisting.** 7.x has no unlisted state (R29 7), and a relist that changes nothing keeps
the client's command working.

**Symbol-server reads later.** The P8 exit criteria are push and restore; serving PDBs needs a
second protocol (the SSQP key conventions) and PDB reading, which a ticket of its own can take.

**One choice departs from Artifactory:** the normalised layout has no switch. #542 registers
it in the [deviations register](../../implementation/deviations.md) as DV-111.

**Alternatives considered:**
- *A repository field for Enforce Layout:* no source shows its name, and a repository created
  through the provider or the REST API could never set it.
- *The flat container's own path layout*, `<id>/<version>/<id>.<version>.nupkg`: no 7.x source
  shows it, and the JFrog CLI would not find pushed packages.
- *Storing an off-convention REST deploy unindexed*, as npm's strict layout does: Enforce Layout
  refuses such names, and a second file per version could not be told from the first.
- *Registration pages on the work queue*, as decision 0032 first expected: they depend on the
  caller and the base URL, and the index already holds them.
- *Ignoring `maxUniqueSnapshots`*, as npm does: JFrog intends npm to ignore it (decision 0034)
  but documents it for NuGet.
- *An unlisting property:* 7.x has none, so a repository relying on it could not move to 7.x.

## Artifactory 7.x check

The decision follows 7.161.32 and SaaS 7.174.0 ([research 29 section 18](../29-nuget.md#18-artifactory-7x-check)):
- **From the documentation and release notes:** Enforce Layout (7.146.7, SaaS 7.139.1) and its
  409, normalisation by the migration tool's rules (7.161.15, SaaS 7.152.1), strict version
  validation (7.125.4), the symbols URL, force authentication, SemVer 2 hidden from older
  clients, `GetUpdates()` deprecated (7.49.3), reindex re-annotating from the nuspec, and
  `.symbols.nupkg` not indexed (7.90.5, 7.104.14).
- **From Jira and KB:** the push's 201 and root path, the registration's shape and its paths,
  the search path, the hard delete, no unlisting, the `nuget.dependency` format, 401 for wrong
  credentials (7.84.10), and NuGet among the `maxUniqueSnapshots` types (KB 000005403).
- **From the clients:** the requests and statuses `NuGet.Protocol` 8.1.0.12 sends and needs, and
  the paths the JFrog CLI expects.
- **7.x bugs and gaps not reproduced, which are not deviations:** the doubled hive in
  registration links (RTFACT-30380), the bearer token refused on NuGet routes (RTFACT-29362),
  `maxUniqueSnapshots` deleting nothing at the root (RTFACT-31210), the delay before a pushed
  version appears (R29 12), and the registration's missing `published` (RTFACT-27465).
- **7.x features left out:** symbol-server reads, the search rate limit, `repository-signatures`
  (remote only), the global "Normalize package version" setting (every repository is
  normalised), the migration tool (nothing to migrate), and the newer handler's system
  property.

## Constraints

- A package whose version NuGet cannot parse cannot be stored, by any route.
- Pushing a pre-release version again answers 409, where a non-normalised 7.x repository
  overwrites it for a user with Deploy alone (KB 000006062). A CI job that re-pushes one
  version needs `--skip-duplicate` or a new version.
- A legacy `.symbols.nupkg` pushed through the API cannot be told from its package: before
  the package it is stored as the package, after it the push answers 409.
- A repository keyed `v3` has no reachable v2 root.
- Symbol-server reads are not served, so Visual Studio cannot fetch PDBs from artifactweave in
  P8.
- **Unconfirmed:** the items in R29 section 17 are inferred and on the live-comparison list
  ([#216](https://github.com/weaveplatform/artifactweave/issues/216)) for P19.

## Verification

- **Unit tests** in `internal/format/nuget` for nuspec parsing (namespaces, casing, missing
  elements), normalisation tables from Microsoft's and JFrog's examples, the canonical name,
  duplicate refusal by every route, overwrite at the version's own path, symbols linking, the
  `nuget.*` properties, and the `maxUniqueSnapshots` rule; `formattest` on every store, with a
  reindex that reproduces the index (#542).
- **Server tests** driving `NuGet.Protocol`'s request sequences: restore through the flat
  container and through the registration, pages at 129 versions, gzip, both hives (#543);
  search and autocomplete filters and paging (#544); `dotnet nuget push` with
  `--skip-duplicate`, delete, relist, symbols and force authentication (#545); v2
  service/EDMX and Atom parsing,
  exact-key restore, 100-entry continuation, latest/SemVer/visibility filters, count and
  fault cases (#546). The focused native `nuget_v2.feature` proves .NET's NuGet.Protocol
  search and prerelease-range restore; the wider client exit matrix belongs to #547.
- **The NuGet rows** of the status table and the route by principal matrix, the API key as
  credentials and force authentication among them (#543 to #546).
- **Acceptance (#547)** with the .NET SDK images: `dotnet nuget push` and `dotnet restore`
  through v3 and v2 from empty package folders, comparing the consumer lockfile's
  SHA-512 `contentHash` and downloaded bytes to the packed package. Both pinned SDK
  images also cover `--skip-duplicate`, normalised duplicate 409 for admin and a
  publisher without Delete, hard delete and failed restore until re-push, no-op
  relist, companion and standalone `.snupkg` pushes, anonymous forced-authentication
  401, `dotnet package search`, and root-level `maxUniqueSnapshots` retention.
  `jf dotnet nuget push` and v3/v2 `jf dotnet restore` use JFrog CLI 2.126.0's native
  mode and publish artifact/dependency records with `jf rt bp`.
- **Client boundary (#547):** the SDK images contain neither `nuget.exe` nor Mono;
  `nuget.exe list` and `jf nuget restore` are not claimed as passing exit checks.
  Microsoft's [client installation documentation](https://learn.microsoft.com/nuget/install-nuget-client-tools)
  requires Mono for the legacy executable on Linux. The two-SDK matrix exercises
  NuGet.Protocol's v2 feed through `dotnet package search` and `dotnet restore`,
  and the supported JFrog dotnet wrapper, without adding another runtime/image.

## References

- [Research 29](../29-nuget.md), [research 04](../04-package-protocols.md#nuget),
  [research 03](../03-repository-configuration-model.md)
- [0018-package-index](0018-package-index.md), [0032-package-format-handlers](0032-package-format-handlers.md),
  [0033-persistent-work-queue](0033-persistent-work-queue.md), [0034-npm](0034-npm.md)
- NuGet/NuGet.Client 8.1.0.12 (`350273adcd4c`), `NuGet.Protocol` and `NuGet.Commands`
- jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/dotnet` and `artifactory/commands/nuget`;
  build-info-go `f9d40441f262`, `flexpack/nuget`
- <https://docs.jfrog.com/artifactory/docs/nuget-repositories>,
  <https://docs.jfrog.com/artifactory/docs/migration-tool>
- <https://learn.microsoft.com/en-us/nuget/api/overview>
