// Package layout holds the repository-wide tests of the modular-monolith
// migration: the ledger is complete, every package documents itself to the
// standard, staged packages keep the dependency rules, tests read fixtures by
// root-relative paths, only access and the kernel touch the credential
// fields of the request principal, tests outside access stay off its
// internals, and production code stores a principal without its permission
// decider only where that is the point. It has no production code. The tests
// are the enforcement, so a violation fails the gate instead of waiting for
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
//     imports, fixture reads, the selectors and composite literal keys that
//     name a field of the principal, and the identifiers that name
//     WithPrincipal) and .golangci.yml.
//
// # Outputs
//
//   - Test failures that name each violation. With
//     ARTIFACTWEAVE_UPDATE_LEDGER=1, TestUnitLedgerComplete first rewrites
//     migration/ledger.json in canonical form.
//   - With -v, the number of ledger entries in each derived status.
//   - With -v, each import of a split legacy package that
//     TestUnitImportTiers allowed, and how many there were.
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
//   - TestUnitPrincipalInternals: outside internal/auth and the staged
//     kernel and access trees, no Go file selects IsTokenAuthenticated,
//     TokenSubject or PasswordRevision, the Principal fields that access
//     trusts when it rechecks a credential, or sets one in a composite
//     literal (PasswordRevision only in a Principal literal, since the
//     credential references declare it too); its cases prove each rule can
//     fail.
//   - TestUnitWithPrincipalCallers: no production file names WithPrincipal,
//     which stores a principal without its decider, except the bridge that
//     aliases it, access/api's WithCaller, which binds the decider next, and
//     the remote reindex, which stores the caller it has just rechecked so
//     that its permissions are evaluated afresh; its cases prove each way of
//     naming it is caught.
//   - TestUnitTestsAvoidAccessInternals,
//     TestUnitAccessInternalsRuleDetectsViolations: outside access, no test
//     imports migration/access/core/authz, internal/auth, internal/auth/token or a package
//     of access core other than test support. Only the server root,
//     internal/app and the test/server harnesses may import the
//     authenticator and the token service, until TST-04 to TST-07 and WEB-22
//     remove the reason, and an allowlist entry that no file uses fails; the
//     second proves each rule can fail.
package layout
