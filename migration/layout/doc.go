// Package layout holds the repository-wide tests of the modular-monolith
// migration: the ledger is complete, every package documents itself to the
// standard, staged packages keep the dependency rules, and tests read
// fixtures by root-relative paths. It has no production code. The tests are
// the enforcement, so a violation fails the gate instead of waiting for
// review.
//
// # Role
//
// layout test support: repository checks that run with the unit tests and
// are not part of the server binary. When migration/ is promoted to
// internal/, this package replaces the legacy import-tier test at the same
// path.
//
// # Inputs
//
//   - The repository tree, found from the test's working directory with
//     platform/testkit/repopath and walked with its SkipDir rule.
//   - migration/ledger.json, read and checked through devtools/ledger.
//   - The Go source files of every package (package clauses, doc comments,
//     imports and fixture reads) and .golangci.yml.
//
// # Outputs
//
//   - Test failures that name each violation. With
//     ARTIFACTWEAVE_UPDATE_LEDGER=1, TestUnitLedgerComplete first rewrites
//     migration/ledger.json in canonical form.
//   - With -v, the number of ledger entries in each derived status.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - migration/README.md: the ledger, the walk rule and the per-ticket
//     checklist.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     dependency rules and repository root.
//   - https://github.com/weaveplatform/artifactweave/issues/935
//
// # Status
//
// Implemented P9d.
//
// # Testing
//
//   - TestUnitLedgerComplete, TestUnitLedgerCheckDetectsViolations: the
//     ledger validates, is canonical, has an entry for every legacy package
//     directory and no broken entry, and every placeholder holds doc.go
//     only; the second proves each of these checks can fail.
//   - TestUnitPackageDocs, TestUnitPackageDocsRejectsNonConformingPackages,
//     TestUnitLintScopeListsEveryLinter,
//     TestUnitLintLegacyRuleMatchesLedgerLegacyRoots: the package doc
//     standard and the lint scope.
//   - TestUnitImportTiers, TestUnitTopLevelDirectories,
//     TestUnitImportRulesDetectViolations: the dependency rules between
//     staged packages and the fixed set of top-level directories.
//   - TestUnitFixtureReadsAreRootRelative,
//     TestUnitFixtureReadRuleDetectsViolations: tests read repository
//     fixtures through repopath.
package layout
