// Package repopath finds the repository root and reads files by their
// root-relative paths, so a test names a fixture such as
// "api/access.openapi.yaml" the same way from any package directory. It
// also holds the walk rule that every repository walk of the migration
// shares.
//
// # Role
//
// platform module, test support layer (platform/testkit). It imports only
// the standard library, so it moves unchanged when migration/ is promoted
// to internal/.
//
// # Inputs
//
//   - The working directory of the test, which go test sets to the package
//     directory; the nearest go.mod at or above it marks the root.
//   - Slash-separated paths relative to that root, named by tests.
//
// # Outputs
//
//   - Absolute paths and file contents. A failed lookup fails the test
//     through testing.TB instead of returning an error to every caller.
//   - The sorted root-relative directories that hold Go files, for the
//     layout and ledger tests.
//
// # Invariants
//
//   - Every walk skips directories whose name starts with ".", testdata,
//     vendor, node_modules and any directory below the root with its own
//     go.mod. Untracked worktree copies under dot-directories such as
//     .worktrees/ each hold a go.mod, so a walk without this rule would see
//     every package many times over.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - migration/README.md, "Walk rule".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration.
//   - https://github.com/weaveplatform/artifactweave/issues/950
//
// # Status
//
// Implemented P9d.
package repopath
