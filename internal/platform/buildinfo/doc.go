// Package buildinfo carries the version metadata that the linker stamps into
// the binary with -ldflags -X. A plain `go build` stamps nothing, so it
// reports "devel" and empty values.
//
// # Role
//
// platform layer: build metadata that no module owns. It imports no package,
// so the version command and the composition root read it without
// depending on any module.
//
// # Inputs
//
//   - make build: LDFLAGS in the Makefile stamps the version (git describe),
//     the commit (git rev-parse HEAD) and the build time in UTC.
//   - The image build: the Dockerfile stamps the version and the commit from
//     its VERSION and COMMIT build arguments, which make docker sets. It
//     stamps no build date.
//   - The release build: .goreleaser.yaml stamps the version of the tag, the
//     full commit and, as the build date, the commit date rather than the
//     time of the build, so that the build is reproducible.
//
// # Outputs
//
//   - Version: the stamped version, or "devel".
//   - Commit and BuildDate: the stamped commit and date, or empty.
//   - The version command (cmd/artifactweave) prints them. The composition
//     root logs the version at startup and serves it as the revision of
//     /artifactory/api/system/version.
//
// # Invariants
//
//   - The import path and the names version, commit and buildDate are part
//     of the build contract. The linker ignores -X for a symbol that does
//     not exist, so renaming a variable, or moving the package without its
//     flags, ships "devel" with no build error;
//     TestUnitLdflagsStampThisPackage fails first.
//   - Each variable stays uninitialized or initialized to a constant string.
//     -X does not work on a variable whose initializer calls a function or
//     refers to another variable, and nothing reports that either.
//   - "devel" is also the version the Makefile and the Dockerfile fall back
//     to when they have none; TestUnitDefaults pins it.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md,
//     "Compatibility version strings": the real artifactweave version is the
//     revision of the Artifactory version response.
//   - docs/testing/ci.md, "Releases": the release build stamps the package
//     as make build does.
//   - migration/README.md, "Per-ticket checklist", item 3: the -X flags are
//     a path coupling that pkgmove rewrites when a package moves.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 128 (platform/buildinfo) and line 991 (the mapping row).
//   - The linker's -X flag: https://pkg.go.dev/cmd/link.
//   - Plan: docs/implementation/plan.md#p0-foundations and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/947 (the -X
//     guard) and https://github.com/weaveplatform/artifactweave/issues/977.
//
// # Status
//
// Implemented P0, from the repository template: make build stamps the
// package, and the version command and the revision of the system version
// read it. The image build has stamped it since #73, and the release build
// since #507, which brought the release binaries of P18 forward. Staged in
// P9d.
//
// # Testing
//
//   - TestUnitDefaults: an unstamped build reports "devel" and empty values.
//   - TestUnitLdflagsStampThisPackage: every -X flag in the Makefile,
//     .goreleaser.yaml and the Dockerfile names this package's import path;
//     the Makefile and .goreleaser.yaml stamp all three variables and the
//     Dockerfile the version and the commit. The test takes the address of
//     each variable, so renaming one fails to compile.
//
// No test runs the linker, so the stamped values themselves show only when a
// built binary runs version: one from make build, make docker or a snapshot
// of the release config.
package buildinfo
