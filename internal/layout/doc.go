// Package layout holds the repository-wide tests of the modular-monolith
// layout: every package documents itself to the standard, packages keep the
// dependency rules, tests read fixtures by root-relative paths, only access
// and the kernel touch the credential fields of the request principal, tests
// outside access stay off its internals, and production code stores a
// principal without its permission decider only where that is the point. It
// has no production code. The tests are the enforcement, so a violation
// fails the gate instead of waiting for review.
//
// # Role
//
// layout test support: repository checks that run with the unit tests and
// are not part of the server binary.
//
// # Inputs
//
//   - The repository tree, found from the test's working directory with
//     platform/testkit/repopath and walked with its SkipDir rule.
//   - The Go source files of every package (package clauses, doc comments,
//     imports, fixture reads, the selectors and composite literal keys that
//     name a field of the principal, and the identifiers that name
//     WithPrincipal) and .golangci.yml.
//
// # Outputs
//
//   - Test failures that name each violation.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - docs/implementation/repo-layout.md: the layout these tests enforce.
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
//   - TestUnitPackageDocs, TestUnitPackageDocsRejectsNonConformingPackages,
//     TestUnitLintScopeListsEveryLinter: the package doc standard and the
//     lint scope.
//   - TestUnitImportTiers, TestUnitTopLevelDirectories,
//     TestUnitImportRulesDetectViolations: the dependency rules between
//     packages and the fixed set of top-level directories.
//   - TestUnitFixtureReadsAreRootRelative,
//     TestUnitFixtureReadRuleDetectsViolations: tests read repository
//     fixtures through repopath.
//   - TestUnitPrincipalInternals: outside the kernel and access
//     trees, no Go file selects IsTokenAuthenticated, TokenSubject or
//     PasswordRevision, the Principal fields that access trusts when it
//     rechecks a credential, or sets one in a composite literal
//     (PasswordRevision only in a Principal literal, since the credential
//     references declare it too); its cases prove each rule can fail.
//   - TestUnitWithPrincipalCallers: no production file names WithPrincipal,
//     which stores a principal without its decider, except access/api's
//     WithCaller, which binds the decider next, and the remote reindex,
//     which stores the caller it has just rechecked so that its permissions
//     are evaluated afresh; its cases prove each way of naming it is caught.
//   - TestUnitTestsAvoidAccessInternals,
//     TestUnitAccessInternalsRuleDetectsViolations: outside access, no test
//     imports a package of access core, such as the evaluator, other than
//     test support. Only internal/app's tests and the test/server suites,
//     which wire the router's authenticator and token service themselves,
//     may import the authenticator, the token service and the roles and OIDC
//     services. An allowlist entry that no file uses fails; the second
//     proves each rule can fail.
package layout
