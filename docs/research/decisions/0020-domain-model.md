# 0020: Domain model derived from the wire entities

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

The product contract is wire compatibility with Artifactory ([0001](0001-mirror-the-jfrog-api-surface.md)).
The entities that contract exchanges are now inventoried from the cached JFrog OpenAPI
definitions in [research 15](../15-data-model.md): repositories, items and their
projections, search results, replication, build-info, system records, legacy security,
Access identity, projects, webhooks, retention policies and runs, and the package-type
requests, and since 2026-10-13 the entities of Distribution and release bundles v1,
cold-storage archive, platform federation and topology, federated repositories and Workers
([research 16](../16-enterprise-products.md), research 15 sections 2.13 to 2.17). The same entity often has several wire forms (legacy camelCase, Access
snake_case, the YAML descriptor, the storage API projections), and JFrog types some fields
in ways no Go model would choose (booleans as strings, sizes as strings, human-formatted
counts). The stores ([0004](0004-storage-sql-metadata-and-blobs.md)), the authorizer
([0006](0006-authorization-native-permission-model.md)) and the package index
([0018](0018-package-index.md)) all need one agreed model before any of them is built.

## Decision

The domain model is defined by the wire entities in [research 15](../15-data-model.md),
section 2, and nothing else: an entity exists because some in-scope operation reads or
writes it, and its fields are the union of the fields those operations carry.

- **One internal representation per entity.** Each entity has one Go type in `pkg/types`
  (domain) that carries every field any wire form needs, in the type the server reasons
  with (booleans as `bool`, sizes as `int64`, times as `time.Time`). Wire forms live only
  in `internal/wire/{artifactory,access,legacy}` and are produced and consumed by
  converters; no handler or store touches a wire struct directly. `pkg/repo` is the
  existing instance of this rule for repositories and stays the model for that entity.
- **Wire quirks are preserved by the converters, never by the domain.** Booleans
  accepted as strings, `size` as a decimal string, `lastDownloaded` as epoch
  milliseconds in one projection and ISO time in another, kebab-case keys in the legacy
  v2 permission target, the `<key>-cache` repository name on cached items, and
  human-formatted storage summaries are all rendered at the edge from one domain value.
- **Identity.** Every entity's identity is its wire identity: repository `key`, item
  `(repo, cached, path, name)`, user `username`, group `name`, permission `name`, token
  `token_id`, project `project_key`, webhook `key`, cleanup policy `key`, replication
  `(repoKey, url)` with a server-owned `replicationKey`, build `(project, name, number)`,
  peer `instance_id`, release bundle `(name, version)`, distribution tracker `id`, archive
  policy `key`, archived item `(namespace, original repo, path, name, archived_at)`, Access
  Federation target `name`, platform federation `id`, topology `key`, worker `key`.
  Identities are create-only; renaming is not supported where Artifactory does not
  support it (environments are the exception and have a rename operation).
- **Mutability and server-owned fields.** Fields that appear only in responses
  (`created`, `createdBy`, checksums, `effective_admin`, `last_logged_in`, `realm`,
  `status`, `soft_limit`, `replicationKey`, run records, delivery records, sync state and
  lag, JPD status, execution records) are set by the
  server and ignored on input. Write-only fields (passwords, webhook secret values,
  private keys, passphrases) are accepted on input, sealed at rest
  ([0017](0017-sealed-columns-and-master-key.md)) and rendered masked or omitted on output
  exactly as Artifactory does for each endpoint. Partial updates (`PATCH`, `POST` on
  repositories) merge into the stored entity; replacements (`PUT`) replace the whole
  document except server-owned fields.
- **Derived records are not entities.** Search results, the storage summary, policy
  views, replication status, federation status and lag, monitoring entities and
  effective permissions are computed from entities; they
  have no table and no domain type beyond a result struct.
- **Storage mapping.** The tables in [research 15](../15-data-model.md), section 4, are
  the schema the stores implement: queried fields as columns, the rest of a record as a
  JSON document, sealed columns in their own tables, and the package index maintained by
  the format handlers. The YAML and XML descriptors are views over those tables.
- **Permissions have one shape.** The Access v2 permission (resources with targets and
  grants) is the internal shape; the legacy v1 and v2 permission targets are converters
  over it, as [0006](0006-authorization-native-permission-model.md) already states.

## Rationale

Deriving the model from the crawl rather than from a database design keeps the project
honest about what it has to store: every column traces to an operation that reads or
writes it, and every operation's shape traces to a cached definition. One domain type per
entity with edge converters is the only arrangement that can serve the legacy, Access and
descriptor forms of the same record without drift between them. Putting the derived
records outside the entity set stops the schema growing tables for things that are really
queries.

Alternatives considered:

- *Store wire JSON documents as-is and query them.* Simplest to write, but AQL, property
  search, permission evaluation and retention all filter on fields that must be indexed
  columns, and the three wire forms of one entity would end up stored three ways.
- *A clean-room model with mapping layers added later.* Attractive in the abstract, but
  the wire forms carry the real constraints (identities, required fields, string typing),
  and a model designed without them would need reshaping as soon as the first client ran.

## Constraints

- The domain must never lose information a wire form carries, including fields the
  oracle does not type but the Terraform provider round-trips (listed in
  [research 15](../15-data-model.md), section 5); those are covered by the Terraform
  fixture rather than the parity test.
- Converters must reproduce the per-endpoint quirks exactly; a boolean that Artifactory
  returns as `"true"` must be returned as `"true"`.
- The oracle leaves several shapes untyped (free-form objects); those entities are
  modelled from the research and must be captured against a live Artifactory before their
  phase closes. Build-info is no longer among them: the oracle has fetched the Builds API
  family, whose `uploadbuild` block types the `BuildInfo` document, since
  [#194](https://github.com/weaveplatform/artifactweave/pull/194)
  ([research 22](../22-aql-searches-build-info.md#32-the-document)).
- Adding a wire field is a change to the domain type, the converter and the spec together.

## Verification

- The parity oracle (`make parity`) compares our specs with the cached definitions per
  operation; a shrinking baseline shows the wire forms converging.
- `TestSpecMatchesGoTypes`-style tests, once `pkg/types` exists: a fully populated domain
  value round-trips through every wire converter and validates against the matching
  schema in `api/*.openapi.yaml`, so a field added in one place and not the others fails
  CI.
- The Terraform fixture and the JFrog CLI acceptance features exercise the fields the
  oracle cannot see.
- Store contract suites (`storetest`) assert that every column in section 4 of the
  research is read and written by the store for its entity.

## References

- [Research 15: Data model derived from the JFrog API definitions](../15-data-model.md)
- [Research 02](../02-artifactory-core-api-inventory.md), [03](../03-repository-configuration-model.md),
  [05](../05-platform-identity-and-security-apis.md), [06](../06-system-configuration-and-descriptor.md),
  [07](../07-webhooks-retention-projects.md), [08](../08-terraform-providers.md),
  [09](../09-jfrog-cli-compatibility.md)
- Decisions [0003](0003-repository-model.md), [0004](0004-storage-sql-metadata-and-blobs.md),
  [0006](0006-authorization-native-permission-model.md), [0017](0017-sealed-columns-and-master-key.md),
  [0018](0018-package-index.md)
