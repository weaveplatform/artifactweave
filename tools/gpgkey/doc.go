// Command gpgkey writes a fresh GPG key pair without a passphrase, as
// ASCII-armored private.asc and public.asc in the directory it is given, for
// the Terraform scenarios' artifactory_keypair (test/terraform/run.sh).
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. It
// replaces gpg there: gpg is not on every host, and its agent fails when the
// socket path under GNUPGHOME passes the platform's limit.
//
// # Inputs
//
// The directory to write to:
//
//	gpgkey <directory>
//
// # Outputs
//
//   - private.asc and public.asc in that directory, a new key pair on every
//     run.
//
// # References
//
//   - Research 08: docs/research/08-terraform-providers.md.
//   - https://github.com/weaveplatform/artifactweave/issues/535
//
// # Status
//
// Implemented P8 (issue 535).
package main
