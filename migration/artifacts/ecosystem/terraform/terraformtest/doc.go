// Package terraformtest builds Terraform registry content for tests (role 2
// of epic #526: Terraform modules and Terraform provider packages that
// artifactweave stores): GPG key pairs, the files of a Terraform provider
// package's release laid out and signed as HashiCorp publishes them, and
// module zips. The registry's and the server's tests share it, so they
// deploy the bytes the terraform CLI installs.
//
// # Role
//
// artifacts module, ecosystem layer: test support of the Terraform slice,
// which builds Terraform registry content for other packages' tests. Only
// tests import it; the layout rules admit it to any test, as a package
// whose name ends in test.
//
// # Inputs
//
//   - Driving: the tests of the Terraform slice's format and protocol
//     packages, of the server root, of the guard suite
//     test/server/artifacts/terraform and of the acceptance steps; and the
//     Debian, opkg, RPM and PyPI tests that need a GPG key pair (NewKey).
//   - Driven: OpenPGP (github.com/ProtonMail/go-crypto) and the standard
//     library; nothing of the module.
//
// # Outputs
//
//   - Key and NewKey: an RSA key pair with its ASCII-armored halves, its
//     long key ID (KeyID) and binary detached signatures (Sign).
//   - Release: a version of a Terraform provider package, whose Files are
//     its archives, their SHA256SUMS, its signature and the manifest, under
//     the names Name gives.
//   - ProviderZip and ModuleZip: the archives, stable to the byte, so their
//     h1: hashes are too.
//
// # References
//
//   - Decision 0040: docs/research/decisions/0040-terraform.md ("Modules",
//     "Providers", "Verification").
//   - JFrog Terraform repositories, as decision 0040 cites it:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 1003 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/526 (Terraform
//     modules and providers) and
//     https://github.com/weaveplatform/artifactweave/issues/1034
//
// # Status
//
// Implemented P8 (Terraform module and provider local repositories), with
// remote modules and providers in P9. Staged in P9d.
//
// # Testing
//
//   - terraformtest_test.go: a release's files pass the format's registry
//     check, and the zips are stable and the provider binary executable.
package terraformtest
