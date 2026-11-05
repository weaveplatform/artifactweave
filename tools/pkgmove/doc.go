// Command pkgmove moves the legacy packages and files of the modular-monolith
// migration to their ledger targets, repairs the references a split leaves,
// and proves that no stale path to them remains.
//
//	pkgmove status  [-ticket ID] [-json] [legacy-path]
//	pkgmove move    -ticket ID [-dry-run] [-no-regenerate] [-skip GLOB]...
//	pkgmove move    -from DIR -to DIR [-name NAME] [-dry-run] [-no-regenerate] [-skip GLOB]...
//	pkgmove check   [-ticket ID | -from DIR]
//	pkgmove fmt     [-check]
//	pkgmove promote [-dry-run] [-no-regenerate]
//	pkgmove fix     -ticket ID [-report-only] [-no-regenerate] [-external FILE...]
//	pkgmove fix     -bridges [-report-only] [-no-regenerate]
//
// The exit status is 0 on success, 1 when it finds problems (printed as
// file:line: message) or fails, and 2 for a command line it cannot run.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary. A thin main over migration/devtools/pkgmove, which holds the logic
// and its tests. The impact job of the quality gate runs check.
//
// # Inputs
//
//   - The command line and the working directory: move and promote run from
//     the module root.
//
// # Outputs
//
//   - What migration/devtools/pkgmove writes, and its exit status.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - migration/README.md, "Moving packages with pkgmove".
//   - https://github.com/weaveplatform/artifactweave/issues/955
//   - https://github.com/weaveplatform/artifactweave/issues/956
//
// # Status
//
// Implemented P9d (FND-07, FND-08). It is deleted with the ledger when
// migration/ is promoted to internal/.
package main
