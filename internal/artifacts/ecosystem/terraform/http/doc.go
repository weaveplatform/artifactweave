// Package terraformhttp serves Terraform's discovery and module registry
// through the package router kit (decision 0040): under
// /artifactory/api/terraform/, the module registry, the provider registry,
// the network mirror and the signed URLs that Terraform downloads files
// from, and, at the host root, the discovery document
// /.well-known/terraform.json.
//
// # Role
//
// artifacts module, ecosystem layer: the native Terraform discovery,
// module and provider registry protocol of the Terraform slice. It is named
// terraformhttp so that it does not shadow net/http. Outside tests, only
// its own http subtree, the module's http adapters and app may import it
// (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type terraform, with the
//     encoded-key resolver of the global v1/modules/ and v1/providers/
//     paths and the host-root discovery route. The reindex operation under
//     the same prefix is the server's, which Protocol reserves.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads items and writes errors;
//     the format package (artifacts/ecosystem/terraform), whose paths,
//     index, hashes and release checks the answers are built from; the
//     access contracts in Options (access/api), which recheck the subject a
//     signed URL names and read a provider repository's signing keys; the
//     keyring (Options.Keys), from whose master key the URL tokens are
//     signed; and the remote source service (Options.RemoteSources), which
//     fetches upstream modules and provider packages into the remote cache.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Terraform registry error profile.
//   - IsTerraformReindex: the JFrog reindex operation beside the registry,
//     which the server's error profile rules give Artifactory's envelope.
//   - Answers: the discovery document; a module's versions and its
//     download location; a provider's versions and its download metadata
//     with the signing keys; the network mirror's index and version
//     documents; and a signed, time-limited URL for every file. For a
//     remote repository, upstream modules and provider packages are served
//     once their checksums, signatures and archives pass.
//
// # Invariants
//
//   - Every file URL handed to Terraform is signed, since Terraform sends
//     no credentials when it downloads; a URL is refused once it expires,
//     its subject loses Read or its account, or the file it signed is
//     overwritten.
//   - A list or document shows only what the caller may read.
//   - Remote answers keep the upstream's signed bytes and public-key
//     identity, and a provider package is advertised only after its whole
//     bundle has passed.
//   - The download policy (decision 0032) is checked after Read
//     permission, a signed URL's principal included, and answers its own
//     404 and message. A blacked-out local or federated repository refuses
//     every route: a version list, package metadata or mirror document at
//     the request's path, before the index is read, and a file or a
//     module's /download at the archive's path. Patterns refuse only a
//     file, and a module's /download whose archive they reject; a release
//     whose file they reject stays listed. A remote checks its policy at
//     the 7.x cache path of the registry document a client asks for, under
//     .terraform/ (RTFACT-30802, RTFACT-31026), and each file at its cache
//     path; the lookups on the way are refused by the blackout alone, and a
//     listing keeps a release whose archive the patterns reject.
//   - Errors are {"errors":[...]} (the Terraform registry profile), and
//     statuses and messages stay byte-identical.
//
// # References
//
//   - Decision 0040: docs/research/decisions/0040-terraform.md ("Packages
//     and routes", "The discovery document", "Repository keys in the path",
//     "Signed download URLs", "Modules", "Providers", "The network mirror",
//     "Errors", "Permissions", "P9 remote module implementation (#631)",
//     "P9 remote provider implementation (#632)").
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit); decision 0017:
//     docs/research/decisions/0017-sealed-columns-and-master-key.md (the
//     master key the URL tokens' key derives from).
//   - docs/operations/terraform-remote-modules.md and
//     docs/operations/terraform-remote-providers.md (remote repositories).
//   - JFrog Terraform repositories, as decision 0040 cites it:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md,
//     and its remote repositories:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories#remote-terraform-repository.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/terraform) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/631 (remote
//     modules), https://github.com/weaveplatform/artifactweave/issues/632
//     (remote providers) and
//     https://github.com/weaveplatform/artifactweave/issues/1034
//
// # Status
//
// Implemented P8, with remote modules and providers in P9. Moved here in P9d.
//
// # Testing
//
//   - terraform_test.go and providers_test.go: the module and provider
//     routes through a server, signed downloads, key rotation and
//     permission revocation.
//   - fault_test.go: store faults, missing modules and signing keys, and
//     token validation.
//   - remote_test.go, remote_links_test.go, remote_source_test.go and
//     remote_archive_test.go: remote modules, their sources, links and
//     archives.
//   - remote_provider_test.go, remote_provider_policy_test.go,
//     remote_provider_rotation_test.go and remote_provider_source_test.go:
//     remote providers, the mirror, redirects and credentials.
//   - policy_test.go: the download policy on local and remote module and
//     provider routes: blackout, patterns on one archive, patterns on the
//     .terraform/ metadata paths, and Read permission checked first.
package terraformhttp
