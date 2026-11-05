// Package ownership holds the rule that decides which test directories need a
// reviewed owner in tools/citestselection/test_ownership.json, so the selector
// and the migration's move tool apply the same rule.
//
// # Role
//
// Library of the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration.
// tools/citestselection validates the inventory with it, and
// migration/devtools/pkgmove drops an inventory key whose package moves to a
// directory the rule does not own.
//
// # Inputs
//
//   - Slash-separated directories relative to the repository root.
//
// # Outputs
//
//   - Whether the tests in a directory need a reviewed owner.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - docs/testing/server-test-ownership.md.
//   - https://github.com/weaveplatform/artifactweave/issues/951
//   - https://github.com/weaveplatform/artifactweave/issues/955
//
// # Status
//
// Implemented P9d (issue 951); issue 955 made it importable for pkgmove.
package ownership
