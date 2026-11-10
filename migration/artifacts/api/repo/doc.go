// Package repo is the repository configuration model: the JSON document that
// PUT, POST and GET /artifactory/api/repositories/{key} exchange, with JFrog's
// field names, together with its validation, the defaults Artifactory applies
// on create, the views each reader gets, the package-type names and a
// repository's include and exclude patterns.
//
// # Role
//
// artifacts module, api layer. Every module that touches repositories reads
// this model, and only artifacts writes repositories. It is the one artifacts
// package that other modules, the generated public clients (pkg/client) and
// the generated servers import: the OpenAPI sources name it in their
// x-go-type-import blocks, and the layout test lets pkg/client import only it
// and the wire package among internal packages. As an api package it imports
// only the kernel (kernel/antpath) and the standard library.
//
// # Inputs
//
//   - Repository configuration JSON from the REST API: the create, replace,
//     update and batch requests on /artifactory/api/repositories and
//     /artifactory/api/v2/repositories, decoded into Config.
//   - Configuration as code: the YAML descriptor, which
//     migration/configuration/core/descriptor reads into Config (decision
//     0009), and the JFrog Terraform provider, which sends the REST
//     document.
//   - The stored Config of a repository, for ValidateStored, ValidateUpdate
//     and RestoreSecrets.
//
// # Outputs
//
//   - Config, its enumerations (Class, PackageType, ChecksumPolicy and the
//     rest) and its nested shapes (Member, CustomHTTPHeader,
//     ContentSynchronisation and the rest). FlexBool reads the booleans
//     JFrog documents as strings, and Flex builds one.
//   - Validation errors: Validate, ValidateStored and ValidateKeyPair return
//     a ValidationError, a list of FieldError; ValidateUpdate returns a
//     CreationOnlyError. Both wrap ErrInvalid. A malformed flexible boolean
//     fails with ErrNotBoolean.
//   - Defaults: Defaults fills what Artifactory fills on create, and
//     DefaultLayout gives the layout the JFrog Terraform provider assigns.
//   - Views: Public, the subset a reader who is not an administrator gets;
//     Masked, the configuration without its secrets, as every reader gets
//     it, with RestoreSecrets keeping a sensitive header's stored value on
//     write-back; and Relevant, the shape of the v2 read and of the
//     configurations list.
//   - Package-type names: AllowedPackageTypes per class, AcceptsMember for
//     virtual members, PackageTypeName as listings spell a type and
//     PackageTypeDisplayName as the storage summary does.
//   - RepoPatterns, which PatternsOf compiles from a Config: a repository's
//     include and exclude patterns, matched with kernel/antpath. AQL,
//     listings, searches and the generic and Maven formats check paths with
//     them.
//   - ResolveKey: the stored key that a key as a client sent it names, and
//     whether it names that remote repository's cache ("maven-remote-cache"
//     is the cache of "maven-remote"); the access module evaluates
//     permission targets on the stored key.
//
// # Invariants
//
//   - JSON names are JFrog's: camelCase with kebab-case and snake_case mixed
//     into the same structs. Clients and Terraform state compare them, so
//     .golangci.yml exempts this package from tagliatelle (decision 0002).
//   - Validation and update error texts reach API clients and Terraform
//     users, so they stay byte-identical. Validate reports every invalid
//     field, not only the first, and applies no defaults: run Defaults
//     first.
//   - Booleans are written as JSON booleans on every class, and the strings
//     "true" and "false" are accepted on input; anything else is an error
//     rather than a silent false.
//   - Defaults leaves the fields of other classes unset, so a local
//     repository grows no remote-only keys.
//   - Masked never returns a secret. RestoreSecrets puts a stored sensitive
//     header value back only where the client sent that header back empty,
//     as Masked showed it, so reading a configuration and writing it back
//     keeps the secret.
//   - An unset or empty includesPattern includes everything, as
//     Artifactory's "**/*" does, and an exclude wins over an include.
//
// # References
//
//   - Research 03: docs/research/03-repository-configuration-model.md, the
//     field semantics, defaults, per-class rules and typing traps.
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md,
//     "Wire models and the linter".
//   - Decision 0003: docs/research/decisions/0003-repository-model.md, "One
//     configuration struct".
//   - Decision 0020: docs/research/decisions/0020-domain-model.md (this
//     package stays the model for repositories).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" and "Names the migration
//     introduces" (RepoPatterns and PatternsOf).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 175 (artifacts/api/repo) and lines 1094 to 1095 (repository
//     configuration is published as artifacts/api/repo).
//   - https://docs.jfrog.com/integrations/docs/configuration-json-files.md
//   - https://docs.jfrog.com/artifactory/reference/createrepository.md
//   - https://docs.jfrog.com/artifactory/reference/getrepositoryconfiguration.md
//   - Plan: docs/implementation/plan.md#p0-foundations (task 2),
//     #p1-data-model-and-api-schemas and #p9d-modular-monolith-migration,
//     epic ART (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/964
//   - https://github.com/weaveplatform/artifactweave/issues/967
//
// # Status
//
// Implemented P0 to P9c: the model, its JSON round trip and validation in P0
// and P1, then the fields, defaults and package types of each ecosystem from
// P3 to P9c. RepoPatterns, PatternsOf and ResolveKey added in P9d. Staged in
// P9d.
//
// # Testing
//
//   - TestUnitFixturesRoundTrip: the documents in testdata decode and encode
//     back unchanged.
//   - The TestUnitValidate* family: the rules and their messages, every
//     error of a document reported at once
//     (TestUnitValidateCollectsEveryError), the key rules that
//     ValidateStored skips, and the fields only a create may set
//     (TestUnitValidateUpdate).
//   - TestUnitDefaults and the per-ecosystem default tests.
//   - TestUnitPublic, TestUnitMaskedAndRestoreSecrets and the
//     TestUnitRelevant family: the views. The two
//     *MatchesTheDocumentedSample tests compare them with JFrog's documented
//     samples.
//   - TestUnitAllowedPackageTypes, TestUnitPackageTypeName,
//     TestUnitPackageTypeDisplayName and
//     TestUnitEveryAcceptedTypeHasAListingName: the package-type names.
//   - TestUnitRepoPatterns: includes, excludes and folders that lead to
//     included content.
package repo
