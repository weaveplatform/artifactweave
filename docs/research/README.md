# Research: mirroring JFrog Artifactory in an open source Go registry

Research baseline: **2026-10-12**. This set of documents records what JFrog publishes
about its REST, Access and Event APIs and its package protocols, what the JFrog CLI and
the JFrog Terraform providers actually call, and the design that follows from both. It is
the foundation for `artifactweave`: the numbered reports hold facts with citations, the
[decision records](decisions/README.md) hold the decisions and their rationale, and the
[implementation plan](../implementation/plan.md) holds the sequencing.

## Purpose

Teams that want to leave Artifactory need a registry that their existing clients,
pipelines and Terraform modules work against without change. The only way to make that
promise is to mirror the documented wire behaviour and to verify it against the clients
that matter. The research therefore inventories every endpoint family with the exact
paths, JSON shapes and status codes the clients depend on, decides what to implement,
shim, stub or ignore, and names what could not be verified from public sources.

## How to read

1. Start with the [API landscape](01-jfrog-api-landscape.md) for where the definitions
   live and how the inventory was built.
2. Reports [02](02-artifactory-core-api-inventory.md) to
   [07](07-webhooks-retention-projects.md) are the inventory, grouped by API family.
3. Reports [08](08-terraform-providers.md) and [09](09-jfrog-cli-compatibility.md) are
   the client side: what the providers and the CLI call and the status codes they
   hard-check.
4. [Scope and tiers](10-scope-and-tiers.md) decides what is in and out.
   [Prior art](11-prior-art.md) names what to borrow from other registries.
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
| [11-prior-art.md](11-prior-art.md) | Gitea, Athens, distribution, zot, Harbor, Nexus, Pulp, Cloudsmith |
| [12-open-questions.md](12-open-questions.md) | Unresolved questions and the live captures still needed |
| [13-review-log.md](13-review-log.md) | What the three adversarial reviews found and changed |
| [15-data-model.md](15-data-model.md) | Wire entities derived from the cached JFrog definitions, their relationships, and the storage tables they imply |
| [16-enterprise-products.md](16-enterprise-products.md) | Distribution and release bundles v1, cold-storage archive, platform federation and topology, federated repositories and Workers: endpoints, entities, clients, and what is not reproducible with artifactweave-only peers |
| [17-undocumented-shapes.md](17-undocumented-shapes.md) | What SDKs, providers, samples, KB articles and community captures show for the shapes JFrog leaves untyped, with confidence per finding |
| [18-cloud-blob-storage.md](18-cloud-blob-storage.md) | Google Cloud Storage and Azure Blob Storage: SDK capabilities and limits for resumable multipart uploads, finalising at a content address, presigning, authentication, emulators, JFrog's provider settings |
| [glossary.md](glossary.md) | Terms used across the set |
| [decisions/](decisions/README.md) | Decision records 0001–0026 |

## Versions at the research baseline

| Component | Version | Note |
|---|---|---|
| JFrog Artifactory docs | 7.x, read 2026-10-12 | definitions embedded per page on docs.jfrog.com |
| `jfrog/artifactory` Terraform provider | v12.11.14 (2026-09-11) | |
| `jfrog/platform` Terraform provider | v2.2.11 (2026-08-25) | |
| `jfrog/project` Terraform provider | v1.9.9 (2026-08-24) | |
| jfrog-client-go | v1.55.0 | source read for paths, headers and status codes |
| jfrog-cli-core | v2.60.0 | |
| OCI distribution-spec | 1.1 | referrers API, end-4c and end-7b |
| Go | 1.27 | module baseline |
| chi | v5.3.2 | |
| oapi-codegen | v2.8.0 | OpenAPI 3.0.3 specs |
| testcontainers-go | v0.44.0 | Linux jobs only |
| golangci-lint | v2.14.0 | |

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
