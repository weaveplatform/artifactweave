// Package terraform is the format of Terraform registry repositories
// (decision 0040): the modules and Terraform provider packages that users
// store in terraform repositories, which the module registry, the provider
// registry and the network mirror serve. It reads the paths of what is
// stored, keeps the package index, computes the h1: hashes of a Terraform
// provider package's archives, and makes the checks a registry download
// runs. The Terraform backend's state is decision 0041's, under a package
// type of its own.
//
// A repository's terraformType chooses what it indexes. A MODULE
// repository indexes each module zip at <ns>/<name>/<system>/<version>.zip
// as version <version> of package <ns>/<name>/<system>. A PROVIDER
// repository indexes the files of a Terraform provider package's release
// at [<folders>/]<ns>/<type>/<version>/ as version <version> of package
// <ns>/<type>: its archives, its SHA256SUMS, the SHA256SUMS's signature and
// its manifest. Any other file, a module zip in a PROVIDER repository and
// the reverse included, is stored and not indexed, and so is everything in
// a repository without terraformType. The format sets no property.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the Terraform
// registry slice (package type terraform: modules and Terraform provider
// packages). Its Handler plugs into the item service through the format
// hooks (decision 0032); the registry protocol that serves what it keeps is
// the slice's http package (artifacts/ecosystem/terraform/http), and the
// content the slice's tests deploy comes from terraformtest.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for terraform repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and the
//     registry protocol, which parses module and provider paths
//     (ParseModule, ParseProviderPackageFile, ProviderPackageName), reads a
//     version's archives (ReadMeta), hashes an archive whose hash the index
//     lacks and each remote one (H1), and checks a release before it is
//     served (Check, Shasum, KeyID).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored archives and keeps the package index
//     (artifacts/core/pkgindex), with AQL (artifacts/core/aql) for a
//     reindex; and the generic deploy policy (artifacts/ecosystem/generic),
//     which Handler embeds.
//   - Settings: none. A repository's terraformType (repo.Config) chooses
//     what is indexed.
//
// # Outputs
//
//   - Handler: the hooks that hash a provider archive before the write's
//     transaction (Inspect), settle the versions a write or a delete
//     changes (Deployed, Deleting, Deleted) and rebuild the index
//     (Reindex).
//   - The package index: a module version per module zip, and a provider
//     version per release, with its archives by platform as its Meta.
//   - The rules the protocol shares: Module and ParseModule;
//     ProviderPackageFile, Kind, ParseProviderPackageFile and
//     ProviderPackageName; Meta, Archive and ReadMeta; H1; Release,
//     Verified, Check, Shasum and KeyID.
//
// # Invariants
//
//   - A version's index entry is what the tree says, read the same way by
//     a write, a delete and a reindex, and in the same order on every
//     store.
//   - No decompression holds the store's write lock: archives are hashed
//     before the write's or the reindex's transaction, and an archive keeps
//     its hash while its content is unchanged.
//   - A failed release check is a 409 that names what is missing or wrong,
//     and every status and message a client sees stays byte-identical.
//
// # References
//
//   - Decision 0040: docs/research/decisions/0040-terraform.md ("Modules",
//     "Providers", "The network mirror", "terraformType", "Package index",
//     "Reindex", "Properties").
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0018: docs/research/decisions/0018-package-index.md.
//   - docs/operations/terraform-remote-modules.md and
//     docs/operations/terraform-remote-providers.md (remote repositories).
//   - JFrog Terraform repositories, as decision 0040 cites it:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/terraform) and line 1003 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1034
//
// # Status
//
// Implemented P8 (Terraform module and provider local repositories), with
// remote modules and providers in P9. Moved here in P9d.
//
// # Testing
//
//   - handler_test.go and fault_test.go: indexing modules and releases,
//     hashing each archive once, which path holds each place of a version,
//     and store faults.
//   - path_test.go, hash_test.go and release_test.go: path parsing, h1:
//     hashes and release checks.
//   - contract_test.go: the format contract suite (artifacts/core/formattest)
//     for MODULE and PROVIDER repositories.
package terraform
