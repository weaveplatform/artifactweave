# Glossary

Checked against: Artifactory 7.161.32

Terms used across this research set, in the sense Artifactory and its clients use them.
Where a term has a different meaning in an upstream protocol, the Artifactory sense is
given first.

This glossary defines protocols and the target model, including later-phase features.
For implemented support after P8, see the
[build status](../implementation/plan.md#build-status); a term's presence here does not
establish that its handler exists.

| Term | Meaning |
|---|---|
| `rclass` | The repository class in the repository configuration JSON: `local`, `remote`, `virtual` or `federated`. Listing endpoints report the same thing upper-case as `type` (`LOCAL`, `REMOTE`, `VIRTUAL`, `FEDERATED`, `DISTRIBUTION`). |
| repository key, `repoKey` | The unique name of a repository, used as the first path segment under `/artifactory/` and as the `key` field in configuration. Inside a project the key carries the project key as a prefix, for example `myproj-npm-local`. |
| `packageType` | The ecosystem a repository serves (`docker`, `npm`, `maven`, `go`, `pypi`, `nuget`, `helm`, `terraform`, `generic`, and others). Called `type` in the YAML descriptor. |
| `<repo>-cache` | The name under which the cached content of a remote repository appears in storage, search and AQL responses. It is not a repository of its own: it cannot be created, listed or permissioned separately, and `ANY REMOTE` permission targets cover it. |
| virtual repository | A repository that aggregates other repositories in a fixed order, serves merged metadata, and forwards writes to its `defaultDeploymentRepo`. |
| repository layout, `repoLayoutRef` | A named set of path patterns (`maven-2-default`, `npm-default`, `simple-default`, and so on) that lets Artifactory parse organisation, module, version and classifier out of an artifact path. |
| layout tokens | The placeholders in a layout pattern: `[org]`, `[orgPath]`, `[module]`, `[baseRev]`, `[folderItegRev]`, `[fileItegRev]`, `[classifier]`, `[ext]`, `[type]` and `[custom<regex>]`; parenthesised groups are optional. |
| integration revision | The part of a version that marks it as a snapshot or integration build, for example `SNAPSHOT` or a timestamp and build number. Matched by the layout's folder and file integration-revision regular expressions. |
| checksum deploy | A `PUT` with the header `X-Checksum-Deploy: true` and a checksum header. If a blob with that checksum already exists and the caller may read it, the server links it without a body transfer and answers 201; otherwise it answers 404 and the client uploads normally. |
| `checksums` and `originalChecksums` | In file info, `checksums` are the values the server computed; `originalChecksums` are the values the client declared on deploy. A mismatch is reported by the bad-checksum search. |
| checksum sidecar | The `.sha1`, `.md5`, `.sha256` and `.sha512` files that Maven clients request and deploy next to each artifact. Artifactory serves them from metadata rather than storing them as items. |
| matrix parameters | Properties attached to a request path with `;key=value` segments, used to set properties on deploy and to resolve conditionally on download. |
| property | A key with one or more string values attached to an item. Stored and queried through the storage API, the metadata API and AQL (`@key`). |
| property set | A named group of predefined properties, with optional closed value lists, attached to repositories. |
| AQL | Artifactory Query Language: `items.find({...}).include(...).sort(...).offset(n).limit(n)` sent as `text/plain` to `POST /api/search/aql`. |
| AQL domain | The primary entity an AQL query returns: `items`, `builds`, `releases`, `release_artifacts`, `item.infos`, with secondary domains reached by dotted paths such as `property` and `stat`. |
| AQL criteria | The JSON filter in `find`: `{"field":{"$op":value}}` with operators `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$match`, `$nmatch`, combined with `$and`, `$or` and `$msp`. |
| trash can | Where deleted items go for `retentionPeriodDays` before garbage collection, restorable through the trash API. |
| garbage collection | The background task that removes binaries no item references any more. |
| cleanup policy | A package cleanup (retention) policy: a cron-scheduled rule over package types, repositories, ages, download times and `keepLastNVersions`, with dry runs and run reports. |
| build-info | The JSON record of a CI build that `jf rt build-publish` uploads to `PUT /api/build`; stored in the `artifactory-build-info` repository and searchable through the `builds` AQL domain. |
| permission target | The Artifactory authorisation object: a set of repositories (or builds, or release bundles), include and exclude patterns, and the actions granted to users and groups. Three wire formats exist: v1 codes, v2 names, and the Access permission model. |
| scope | The space-separated string on an access token that limits what it may do, for example `applied-permissions/user`, `applied-permissions/groups:readers` or `artifact:npm-local/**:r,w`. |
| access token | A JSON Web Token issued by Access with a subject such as `jfac@<service-id>/users/<name>`, a scope, an expiry and a persisted `token_id` for listing and revocation. |
| reference token | A 64-character opaque alias for an access token, matching `cmVmd[A-Za-z0-9]{59}`, usable wherever the token is. |
| identity token | An access token whose scope is `applied-permissions/user`: it acts as the user who created it. |
| project, project key | A platform tenancy unit. The key is 2 to 32 lower-case characters starting with a letter, immutable, and prefixed onto the keys of repositories created inside the project. |
| environment | A label (`DEV`, `PROD`, or custom) attached to repositories and used in project roles. |
| packument | The npm registry's JSON document for a package: `dist-tags`, `versions` with `dist.tarball`, `time` and the rest. The abbreviated form is selected with `Accept: application/vnd.npm.install-v1+json`. |
| dist-tags | npm's named pointers to versions, `latest` by default; managed through `/-/package/<pkg>/dist-tags`. |
| GOPROXY protocol | The Go module proxy HTTP layout: `/<module>/@v/list`, `.info`, `.mod`, `.zip` and `/@latest`, with upper-case letters escaped as `!x`. |
| sumdb | The Go checksum database (`sum.golang.org`) that a proxy may pass through under `/sumdb/`. |
| `maven-metadata.xml` | The Maven index file at artifact level (versions, latest, release) and at snapshot-version level (timestamp, build number, snapshot versions) that the server must generate and, for virtual repositories, merge. |
| simple index | The PyPI repository API of PEP 503 (HTML) and PEP 691 (JSON) at `/simple/` and `/simple/<normalised-name>/`. |
| normalised name | A PyPI project name lower-cased with runs of `-`, `_` and `.` replaced by one `-`. |
| service index | The NuGet v3 `index.json` that lists resources by `@type` (`PackageBaseAddress`, `RegistrationsBaseUrl`, `SearchQueryService`, `PackagePublish`). |
| flat container | The NuGet v3 `PackageBaseAddress` resource: `/<id>/index.json` version lists and `/<id>/<version>/<id>.<version>.nupkg` downloads, all lower-case. |
| registration | The NuGet v3 per-package metadata pages (`RegistrationsBaseUrl`) that `dotnet restore` reads for dependency groups. |
| `index.yaml` | The Helm chart repository index: `entries` per chart with `version`, `digest`, `urls` and the Chart.yaml fields. |
| OCI distribution | The registry HTTP API under `/v2/`: blobs, manifests, uploads, tags, referrers. Docker and Helm OCI charts use it. |
| referrers | The OCI 1.1 API that lists manifests whose `subject` is a given digest, used for signatures and attestations. |
| provider registry protocol | Terraform's HTTP API for discovering provider versions and platform downloads, reached through `/.well-known/terraform.json` and `providers.v1`. |
| module registry protocol | Terraform's HTTP API for module versions and downloads, reached through `modules.v1`, returning the archive location in the `X-Terraform-Get` header. |
| network mirror | The alternative Terraform provider installation protocol served per repository under `/api/terraform/<repo>/providers/`. |
| Terraform backend, `terraformbackend` | A separate local repository type that stores Terraform state and locks. Terraform's `backend "remote"` uses its TFE-compatible workspace/state API under `/artifactory/api/terraform/remote/v2/`; it is distinct from the module/provider registry and from Terraform's simpler `http` backend ([decision 0041](decisions/0041-terraform-backend.md)). |
| webhook subscription | An Event service object with a key, an event filter (domain, event types, criteria) and one handler (`webhook` or `custom-webhook`). |
| descriptor | Artifactory's global configuration document, served as XML and patched as YAML, holding repositories, proxies, property sets, layouts, backups and security settings. |
| federated repository, RTFS | A repository whose members on up to ten JFrog Platform Deployments accept writes and converge. In 7.x the Artifactory Federation Service (RTFS), a standalone service at `/rtfs`, carries the events; the earlier federation embedded in Artifactory is "legacy federation", sunset from 7.133.3. |
| cold storage, smart archiving | Two ways JFrog archives rarely used content: cold storage moves it to a separate self-hosted cold instance; smart archiving moves package versions to an archive instance in JFrog Cloud, for SaaS and, from 7.117.14, hybrid self-hosted deployments. artifactweave archives to a cold blob tier instead. |
