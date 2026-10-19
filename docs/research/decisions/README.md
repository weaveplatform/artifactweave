# Design decisions

These records set out the intended design of artifactweave, the weaveplatform artifact
registry. Each one gives the context, the contract, the rationale, the constraints and the
evidence that will verify it. Each record states its acceptance status; later phases can be
implemented and locally verified while release acceptance remains pending.

Use the [template](TEMPLATE.md) for new decisions and leave out sections that would be
empty. Integrate amendments into the relevant record, keeping its number and filename so
existing links still resolve.

Three rules keep the set readable:

- **Research files hold facts and citations; decision records hold the decision and the
  rationale.** A record links to the section of the numbered research report that holds the
  endpoint tables, field lists or protocol facts it relies on. It never restates them.
- **Records never contain implementation milestones or sequencing.** Ordering belongs in
  the [implementation plan](../../implementation/plan.md).
- **A deliberate departure from Artifactory is also listed in the
  [deviations register](../../implementation/deviations.md)**, which links back to the
  record. The record's Rationale says why.

Status values: Proposed, Accepted, Superseded.

| Decision | Design |
|---|---|
| 0001 | [Mirror the JFrog API surface](0001-mirror-the-jfrog-api-surface.md) |
| 0002 | [Spec-first for flat CRUD, hand-written handlers for item paths](0002-spec-first-flat-crud-handwritten-item-paths.md) |
| 0003 | [Repository model and Docker access method](0003-repository-model.md) |
| 0004 | [Storage: SQL metadata, item model and content-addressed blobs](0004-storage-sql-metadata-and-blobs.md) |
| 0005 | [Authentication, tokens and compatibility version strings](0005-authentication-and-tokens.md) |
| 0006 | [Authorization: native Artifactory permission model](0006-authorization-native-permission-model.md) |
| 0007 | [Remote repositories: proxy and cache](0007-remote-repositories.md) |
| 0008 | [Virtual repositories](0008-virtual-repositories.md) |
| 0009 | [Configuration as code](0009-configuration-as-code.md) |
| 0010 | [Retention, trash can and garbage collection](0010-retention-trash-and-gc.md) |
| 0011 | [Webhooks](0011-webhooks.md) |
| 0012 | [Search and AQL](0012-search-and-aql.md) |
| 0013 | [Projects and environments](0013-projects-and-environments.md) |
| 0014 | [Build-info](0014-build-info.md) |
| 0015 | [Testing and acceptance](0015-testing-and-acceptance.md) |
| 0016 | [Observability and health](0016-observability-and-health.md) |
| 0017 | [Sealed columns and master key](0017-sealed-columns-and-master-key.md) |
| 0018 | [Package index](0018-package-index.md) |
| 0019 | [Human authentication tiers](0019-human-authentication-tiers.md) |
| 0020 | [Domain model derived from the wire entities](0020-domain-model.md) |
| 0021 | [Peer trust and sync protocol](0021-peer-trust-and-sync-protocol.md) |
| 0022 | [Federated repositories](0022-federated-repositories.md) |
| 0023 | [Release bundles v1 and Distribution](0023-release-bundles-and-distribution.md) |
| 0024 | [Cold storage tier](0024-cold-storage-tier.md) |
| 0025 | [Platform federation and topology](0025-platform-federation-and-topology.md) |
| 0026 | [Workers runtime](0026-workers-runtime.md) |
| 0027 | [Cloud blob backends: Google Cloud Storage and Azure Blob Storage](0027-cloud-blob-backends.md) |
| 0028 | [Mirroring blobs across stores, clouds and regions](0028-blob-mirroring.md) |
| 0029 | [CDN delivery and download redirect](0029-cdn-delivery-and-download-redirect.md) |
| 0030 | [Reference deployments and platform testing](0030-reference-deployments.md) |
| 0031 | [Docker and OCI registry protocol and storage layout](0031-docker-oci-registry.md) |
| 0032 | [Package format handlers](0032-package-format-handlers.md) |
| 0033 | [Persistent work queue](0033-persistent-work-queue.md) |
| 0034 | [npm local repositories](0034-npm.md) |
| 0035 | [Go modules](0035-go-modules.md) |
| 0036 | [Maven, Gradle, Ivy and SBT](0036-maven.md) |
| 0038 | [NuGet local repositories](0038-nuget.md) |
| 0040 | [Terraform registry: modules and providers](0040-terraform.md) |
