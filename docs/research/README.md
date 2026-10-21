# Research: mirroring JFrog Artifactory in an open source Go registry

Research baseline: **2026-10-12**. This set of documents records what JFrog publishes
about its REST, Access and Event APIs and its package protocols, what the JFrog CLI and
the JFrog Terraform providers actually call, and the design that follows from both. It is
the foundation for `artifactweave`: the numbered reports hold facts with citations, the
[decision records](decisions/README.md) hold the decisions and their rationale, and the
[implementation plan](../implementation/plan.md) holds the sequencing.

Implementation status reviewed on **2026-10-21**, after P8 merged. P0 through P8,
including cloud storage and the reference deployments, are complete. The local
package protocols and Terraform remote backend have native-client exit checks;
package protocol proxying and aggregation remain P9 and P10. Repository configuration,
wire schemas and storage tables exist for some later phases before their runtime
handlers do. An inventory entry or a generated route therefore does not establish
feature support; use the plan's [build status](../implementation/plan.md#build-status)
and the [deviations register](../implementation/deviations.md) for that boundary.

The **2026-10-21 scope direction** replaces the former "cut from v1" policy: only
capabilities superseded by newer ones may be excluded. All other omissions now have
owners in the [capability roadmap](../implementation/capability-roadmap.md) and P20–P41.
Decision [0042](decisions/0042-capability-scope-and-supersession.md) governs that rule;
historical research snapshots do not override the current scope.

The dates and versions in these reports identify the research that informed a decision.
They are not blanket claims that dependencies or vendor documentation were refreshed during
this review; new supersession evidence is cited separately. A resolved design question can still have an unimplemented handler, and a
working artifactweave handler does not settle an **[I]** claim about Artifactory. Those
claims remain on [#216](https://github.com/weaveplatform/artifactweave/issues/216) for P19;
the expanded scope has its own capture work and P41 qualification. P18 reviews the
remaining decisions for the initial sequence, with later designs reviewed in their phases.

## Purpose

Teams that want to leave Artifactory need a registry that their existing clients,
pipelines and Terraform modules work against without change. The only way to make that
promise is to mirror the documented wire behaviour and to verify it against the clients
that matter. The research therefore inventories every endpoint family with the exact
paths, JSON shapes and status codes the clients depend on, records current behavior,
assigns implementation work and names what could not be verified from public sources.

## How to read

1. Start with the [API landscape](01-jfrog-api-landscape.md) for where the definitions
   live and how the inventory was built.
2. Reports [02](02-artifactory-core-api-inventory.md) to
   [07](07-webhooks-retention-projects.md) are the inventory, grouped by API family.
3. Reports [08](08-terraform-providers.md) and [09](09-jfrog-cli-compatibility.md) are
   the client side: what the providers and the CLI call and the status codes they
   hard-check.
4. [Scope and tiers](10-scope-and-tiers.md) records current availability and phase ownership.
   [Reference materials](11-reference-materials.md) names what to borrow from other registries.
5. [Open questions](12-open-questions.md) lists what is unresolved, including the
   captures still needed against a live Artifactory. The [review log](13-review-log.md)
   records what the adversarial review changed.
6. The [decision records](decisions/README.md) state the contracts and rationale.

## Document map

| Document | Purpose |
|---|---|
| [01-jfrog-api-landscape.md](01-jfrog-api-landscape.md) | Where JFrog's OpenAPI definitions live, how they were read, licence caveat, error envelopes, auth schemes, method |
| [02-artifactory-core-api-inventory.md](02-artifactory-core-api-inventory.md) | Repositories, artifacts and storage, searches and AQL, replication, trash and GC, package-type APIs, with status codes |
| [03-repository-configuration-model.md](03-repository-configuration-model.md) | The repository configuration JSON: classes, package types, layouts, fields, defaults, typing traps |
| [04-package-protocols.md](04-package-protocols.md) | Docker/OCI, npm, Go, Maven, PyPI, NuGet, Helm and Terraform: protocol, URL and storage layouts, metadata, unverified items |
| [05-platform-identity-and-security-apis.md](05-platform-identity-and-security-apis.md) | Legacy security API, Access users, groups, permissions, tokens, roles, OIDC, SCIM, auth mechanisms |
| [06-system-configuration-and-descriptor.md](06-system-configuration-and-descriptor.md) | System endpoints, version strings, the YAML and XML configuration descriptor |
| [07-webhooks-retention-projects.md](07-webhooks-retention-projects.md) | Webhooks, package cleanup policies, projects and environments |
| [08-terraform-providers.md](08-terraform-providers.md) | The `jfrog/artifactory`, `jfrog/platform` and `jfrog/project` providers: resources, endpoints, version gates, status codes |
| [09-jfrog-cli-compatibility.md](09-jfrog-cli-compatibility.md) | What `jf rt` calls, the AQL it generates, headers and status codes |
| [10-scope-and-tiers.md](10-scope-and-tiers.md) | Implement, shim, stub or ignore per JFrog product |
| [11-reference-materials.md](11-reference-materials.md) | Gitea, Athens, distribution, zot, Harbor, Nexus, Pulp, Cloudsmith |
| [12-open-questions.md](12-open-questions.md) | Unresolved questions and the live captures still needed |
| [13-review-log.md](13-review-log.md) | What the three adversarial reviews found and changed |
| [14-data-model-inventory.md](14-data-model-inventory.md) | Generated by `make oracle-inventory`: every cached JFrog operation and named schema, facts only |
| [15-data-model.md](15-data-model.md) | Wire entities derived from the cached JFrog definitions, their relationships, and the storage tables they imply |
| [16-enterprise-products.md](16-enterprise-products.md) | Distribution and release bundles v1, cold-storage archive, platform federation and topology, federated repositories and Workers: endpoints, entities, clients, and what is not reproducible with artifactweave-only peers |
| [17-undocumented-shapes.md](17-undocumented-shapes.md) | What SDKs, providers, samples, KB articles and community captures show for the shapes JFrog leaves untyped, with confidence per finding |
| [18-cloud-blob-storage.md](18-cloud-blob-storage.md) | Google Cloud Storage and Azure Blob Storage: SDK capabilities and limits for resumable multipart uploads, finalising at a content address, presigning, authentication, emulators, JFrog's provider settings; mirroring across stores: Artifactory sharding, cloud-native replication |
| [19-cdn-and-download-redirect.md](19-cdn-and-download-redirect.md) | Artifactory's direct cloud storage download and CDN distribution, how the JFrog CLI and other clients follow a redirect, CloudFront, Cloud CDN and Front Door for signed delivery from private buckets, costs, and artifactweave's redirect today |
| [20-reference-deployments.md](20-reference-deployments.md) | Reference deployments: multi-region object storage on AWS, GCP and Azure and how it meets signed URLs and the mirror; Terraform layout, mocked tests, linting, scanning and generated docs; GitHub OIDC to each cloud; labels, secrets in state and costs |
| [21-oidc-token-exchange.md](21-oidc-token-exchange.md) | OIDC token exchange for CI workload identity: the Access operations, what the JFrog CLI, setup-jfrog-cli and the platform provider send and read, claim matching, transient and synthetic users |
| [22-aql-searches-build-info.md](22-aql-searches-build-info.md) | AQL domains, fields, output rules, limits, errors and permissions; remote caches and virtual repositories in queries (#68); the actions; the GET searches; the Builds API, the build-info document, run identity and its storage as an item, and what the JFrog CLI checks |
| [23-docker-oci-registry.md](23-docker-oci-registry.md) | Docker and OCI registry: the specification's endpoints and error codes, Artifactory's routes and access methods, tokens and permissions, its storage layout and properties, what the JFrog CLI reads, the JFrog Docker operations, tag retention and the conformance suite |
| [24-package-format-handlers.md](24-package-format-handlers.md) | Package format handlers: Artifactory's storage interceptors, the historical pre-P7 framework and the implemented P7/P8 contract, rebuilding indexes from items, derived metadata and the work queue, and the remaining P9/P10 source adapters |
| [25-npm.md](25-npm.md) | npm: registry URLs and scoped names, publish, dist-tags, deprecate and unpublish with `_rev`, packuments, search, ping and whoami, legacy login and `api/npm/auth`, the tarball layout and `npm.*` properties, strict layout and its conversion, `latest`, the 403 on republish, and what `jf npm` sends |
| [26-go-modules.md](26-go-modules.md) | Go modules: the GOPROXY protocol and how the go command reads it, case encoding, versions and zips, the checksum database probe, what `jf go-publish` and `jf go` send, Artifactory's Go repositories, its `go-default` layout, and what remains inferred |
| [27-maven.md](27-maven.md) | Maven, Gradle, Ivy and SBT: layouts and coordinates, unique, non-unique and deployer snapshots and `build.timestamp`, when and how `maven-metadata.xml` is written at the snapshot, artifact and plugin levels, uploaded metadata and checksums, release and snapshot policy, POM checks, `maxUniqueSnapshots`, version tokens, `calculateMetadata` and `generatePom` |
| [28-pypi.md](28-pypi.md) | PyPI: the clients and what they send, the simple index in HTML and JSON and its redirects, PEP 658 core metadata, the upload API and its answers, storage, the `pypi.*` properties and yanking, enforce layout and name normalisation, downloads, the JSON API, reindex, what `jf pip`, `jf twine`, `jf uv` and `jf poetry` send, and the release notes from 7.84 |
| [29-nuget.md](29-nuget.md) | NuGet: the v3 service index and the resource types the client asks for, the flat container, registration hives and pages, search and autocomplete, push, delete and relist with `X-NuGet-ApiKey`, symbols, force authentication, Enforce Layout and normalised versions, the v2 OData feed `NuGet.Protocol` reads, `nuget.*` properties, `maxUniqueSnapshots`, and what `jf nuget` and `jf dotnet` send |
| [30-helm.md](30-helm.md) | Helm and Helm OCI: what `helm`, `jf helm` and `cm-push` send, `index.yaml` and its URLs, when 7.x regenerates it, charts, `.prov` files and their properties, Enforce Layout, full and partial reindex, Helm OCI media types, tags and Helm 3.18's two pushes, what P6 already serves, and the Terraform provider's Helm resources |
| [31-terraform.md](31-terraform.md) | Terraform modules and providers: service discovery and Artifactory's document, the `__` and `-rt-ns-` namespace encodings and the repository-scoped form, module and provider deploys and layouts, signed download URLs, GPG signing and `SHA256SUMS`, the network mirror and `h1:` hashes, `terraformType`, `terraform login` and tokens, `jf terraform publish`, reindex, and testing against a plain-HTTP host |
| [32-terraform-backend.md](32-terraform-backend.md) | Terraform backend: the `remote` backend's discovery, the go-tfe calls each command makes with their bodies and the fields read back, the TFE API rules, what Artifactory serves under `/api/terraform/remote/v2/`, its `workspaces/<ws>/` storage, `locked.by`, `encryptStates`, the fixed 20 MB bug, cleanup, and the interim `terraform login` |
| [glossary.md](glossary.md) | Terms used across the set |
| [decisions/](decisions/README.md) | Decision records, with their index |

## Versions at the research baseline

| Component | Version | Note |
|---|---|---|
| JFrog Artifactory docs | 7.x, read 2026-10-12 | definitions embedded per page on docs.jfrog.com |
| Artifactory target | 7.161.32 self-managed (SaaS 7.174.0), the latest on 2026-10-17 | see [Target version and sources](#target-version-and-sources) |
| `jfrog/artifactory` Terraform provider | v12.11.14 (2026-09-11) | |
| `jfrog/platform` Terraform provider | v2.2.11 (2026-08-25) | |
| `jfrog/project` Terraform provider | v1.9.9 (2026-08-24) | |
| jfrog-client-go | v1.55.0 | source read for paths, headers and status codes |
| jfrog-cli-core | v2.60.0 | |
| OCI distribution-spec | v1.1.1 | referrers API; the conformance suite is pinned to this tag. `end-4c`, `end-7b` and `end-14` are on `main` only ([23](23-docker-oci-registry.md)) |
| npm CLI | v12.2.1 | with npm-profile v13.0.1 and pacote v22.0.0 ([25](25-npm.md)) |
| NuGet client | 8.1.0.12 (`350273adcd4c`) | `NuGet.Protocol` and `NuGet.Commands`, read for the v3 and v2 requests ([29](29-nuget.md)) |
| Go | 1.27 | module baseline |
| chi | v5.3.2 | |
| oapi-codegen | v2.8.0 | OpenAPI 3.0.3 specs |
| testcontainers-go | v0.44.0 | Linux jobs only |
| golangci-lint | v2.14.0 | |

## Target version and sources

artifactweave targets the latest self-hosted Artifactory 7.x release, as listed in the
[Artifactory release notes](https://docs.jfrog.com/releases/docs/artifactory-release-notes)
(maintainer decision, 2026-10-17). On that date it was 7.161.32 (self-managed; SaaS is at
7.174.0). Research and implementation use these sources, in this order:

1. **JFrog's current documentation and OpenAPI definitions** ([01](01-jfrog-api-landscape.md)).
2. **The Artifactory 7.x release notes**
   ([self-managed](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases),
   [SaaS](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), for every
   behaviour that changed after 6.x.
3. **JFrog KB articles**, labelled **[KB]** (`https://jfrog.com/help/r/<slug>`). They are
   JFrog's own answers to support cases and often state 7.x behaviour that neither the
   documentation nor the release notes record.
4. **JFrog's public Jira**, labelled **[J]**. Read each issue's fix versions, affected
   versions and JFrog staff comments.
5. **The JFrog clients' sources and issues:** jfrog-cli, jfrog-client-go and the Terraform
   providers. What a JFrog client sends, or accepts in a response, shows what 7.x answers.
6. **The Artifactory OSS 6.20 source** (`knottnhupfer/artifactory-oss` at `b99e5649bcad`),
   only for what the others leave out. Artifactory 7.x is closed source, and 6.20 (2020)
   is the last release whose source was published.

The audit of P1 to P7 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))
added sources 3 to 5 (maintainer decision, 2026-10-18). Earlier checks read only the
documentation and the release notes. They missed behaviour that KB articles and Jira record,
such as npm `latest` being stored as `npm.disttag=latest`.

**Rules for Jira claims.** A [J] claim counts as 7.x behaviour in either of two cases:
- JFrog fixed the issue in a 7.x release;
- a JFrog staff comment on the issue states the behaviour.

Any other claim that rests on Jira alone is still listed on
[#216](https://github.com/weaveplatform/artifactweave/issues/216) for the live check.

**Rules for 6.20 claims.**
- A claim taken from 6.20 says that the 7.x sources above were checked for the same
  behaviour.
- When 7.x documents the behaviour differently, 7.x wins.
- A 6.20 claim nothing confirms for 7.x stays **[I]** and is listed on
  [#216](https://github.com/weaveplatform/artifactweave/issues/216).

**Recording what was checked.** Each research document and decision record states the
release it was last checked against, as `Checked against: Artifactory 7.161.32`. P19 reads
the release notes onwards from there. A document's label table adds **[KB]** and **[J]**
when it cites them.

**P19** brings everything up to the latest 7.x release current when it runs
([plan](../implementation/plan.md#p19-live-artifactory-comparison-and-tidy-up)).

### Where to check 7.x

**Reading the documentation:**
- Append `.md` to any docs.jfrog.com page to get its raw text with an `updatedAt` date.
  API reference pages embed their OpenAPI 3.1 definition there.
- Page indexes: <https://docs.jfrog.com/llms.txt>, <https://docs.jfrog.com/artifactory/llms.txt>
  and <https://docs.jfrog.com/artifactory/reference/llms.txt>.
- The spec oracle (`make oracle-fetch`) fetches the reference pages through those indexes.

Pages by area, all under `https://docs.jfrog.com/`:

| Area | Pages |
|---|---|
| Package types | `artifactory/docs/` pages: `docker-repositories`, `additional-docker-information`, `oci-repositories`, `npm-repositories`, `go-modules`, `maven-repositories`, `gradle-repositories`, `cargo-repositories`, `repository-layouts`, `supported-package-types` |
| Generic repositories | `artifactory/docs/repository-management-overview#generic-repositories` (`/generic-repositories` is a 404) |
| Repository configuration | `artifactory/docs/` pages: `local-repositories`, `remote-repositories`, `virtual-repositories`, `federated-repositories`. Also `integrations/docs/configuration-json-files` |
| System properties | `installation/docs/artifactory-system-properties`, `installation/docs/supported-access-configurations` |
| Artifacts and storage API | `artifactory/reference/` pages: `deployartifact`, `deployartifactbychecksum`, `retrieveartifact`, `getstorageitem`, `setitemproperties`, `deleteitem`, `copyitem`, `moveitem`, `getstoragesummaryinfo` |
| Repositories API | `artifactory/reference/` pages: `createrepository`, `getrepositoryconfiguration`, `getrepositoryconfigurationv2`, `getrepositoriesbytypeandproject`, `updaterepositoryconfiguration`, `deleterepository` |
| AQL and searches | `artifactory/reference/searchaql`. The `artifactory/docs/` pages: `artifactory-query-language`, `aql-entities-fields-reference`, `aql-search-criteria`, `aql-query-output`, `aql-performance` |
| Build-info | The `integrations/reference/` pages `uploadbuild`, `getbuildinfo`, `promotebuild` and `deletebuilds`. The section is published twice; the second copy's pages end in `-1`, and the two can disagree |
| Access tokens, users, groups, permissions | `administration/reference/` pages: `createtoken`, `gettokens`, `revoketokenbyid`, `oidctokenexchange`, `createuser`, `creategroup`, `createpermission`, `getpermissions`. Also the `administration/docs/` pages `access-tokens` and `permissions` |
| Legacy security API | `administration/reference/` pages: `getuserdetails-1`, `getpermissiontargetsv2`, `createorreplacepermissiontargetv2`. Also `integrations/docs/deprecated-jfrog-apis` |
| System configuration | `administration/reference/patchsystemconfiguration` and `getsystemconfiguration` (self-managed only) |
| Release information | `releases/docs/` pages: `artifactory-self-managed-releases`, `artifactory-saas-releases`, `artifactory-known-issues`, `artifactory-deprecations`, `artifactory-end-of-life` |

**Release notes.** The self-managed notes start at 7.84.3; the 7.84.10 entry rolls up
changes since 7.77. JFrog no longer publishes the notes for 7.0 to 7.83. Some changes appear
only in a KB article and never in the notes. For example, KB 000007043 records the npm
stable-first `latest` in virtual repositories from 7.120.0.

**Jira.** The browse pages need JavaScript. Read issues through the REST API instead:
- `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>` for one issue;
- `https://jfrog.atlassian.net/rest/api/2/search?jql=...` for a search.

**Dead links.**
- `www.jfrog.com/confluence/...` pages are gone.
- `jfrog.com/help/r/<book>/<page>` links redirect through docs-redirect.jfrog.com. Some land
  on the wrong page, and `jfrog-rest-apis/artifactory-rest-apis` ends at a 404. Cite the
  docs.jfrog.com page instead.
- When a page has gone, the Internet Archive's copy of the old `jfrog.com/help/r` page is
  acceptable as **[D]**, citing the archive URL and its date.

**SaaS or self-managed only.**
- SaaS notes sometimes list a change that the self-managed notes do not, such as the npm
  strict-layout switch in SaaS 7.170.0. Record such a change as SaaS-only until a
  self-managed release lists it.
- Get System Configuration, the support-bundle APIs and the Access service's Swagger UI are
  self-managed only.

## Conventions

- Every external claim cites a URL. Claims about this repository's code cite a path.
- Items that could not be confirmed against a primary source are marked *(unverified)*
  and collected in [12-open-questions.md](12-open-questions.md).
- JFrog's definitions are proprietary: they are cited, never copied into the repository.
  Paths, field names and status codes are facts about the wire protocol.
- Status values for decisions: Proposed, Accepted, Superseded.
- No em-dashes; plain sentences; tables for inventories.

## Decisions fixed before writing

1. Wire compatibility with Artifactory 7.x, the JFrog CLI and the JFrog Terraform
   providers is the product contract.
2. One static Go binary; SQLite or PostgreSQL for metadata; local disk or S3 for blobs.
3. Configuration as code in the Artifactory YAML descriptor format and through Terraform.
4. The quality gate from the weaveplatform template: 95% merged coverage, 90% per package,
   blocking lint, acceptance tests against real clients.
