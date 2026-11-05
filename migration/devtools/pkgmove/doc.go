// Package pkgmove moves legacy packages to their ledger targets and proves
// that no stale path to them remains. It is the logic of tools/pkgmove:
// status reports where any legacy path lives now, move applies a ticket's
// ledger entries in one command, check is the stale-path step of CI, fmt
// keeps the ledger canonical and promote renames the staging root to the
// final root at the end of the migration.
//
// # Role
//
// devtools layer: repository tooling behind tools/pkgmove, not part of the
// server binary. It reads migration/ledger.json through
// migration/devtools/ledger and drives git, the go command (through
// go/packages) and the regeneration targets of the Makefile.
//
// # Inputs
//
//   - The ledger, its derived status and the tracked files of the
//     repository at the module root (git ls-files; untracked copies such as
//     .worktrees/ are never read).
//   - A ticket ID (move -ticket, check -ticket, status -ticket), a staged or
//     test/ directory to re-home (move -from -to), or a legacy path (status).
//   - Type information of the packages that import a package whose name
//     changes, loaded with their tests and the e2e tag for linux, darwin and
//     windows.
//
// # Outputs
//
//   - move: git mv of the files each entry carries (the files directly in
//     the directory, renamed by the entry, and asset subdirectories that hold
//     no package), the package clause, every importer's import path with its
//     qualifiers renamed or the old name kept as an import name, the paths in
//     Go string literals and comments, the fixed configuration list and the
//     living Markdown, the server test ownership inventory as JSON, then the
//     regeneration steps and the stale check. An import that a merge repeats
//     is removed. -dry-run prints the plan only.
//   - check: one file:line: message per stale path, and exit status 1.
//   - status: one line, or a JSON object under -json, per entry or for the
//     asked path.
//   - promote: the staged tree renamed to the final root, every staged path
//     rewritten, the ledger removed and docs/implementation/p9d-path-map.json
//     written.
//
// # Invariants
//
//   - One matcher decides every reference for move, check, promote and
//     status, so a move rewrites exactly the paths that check reports. It
//     matches <module>/P, $(MODULE)/P and ${MODULE}/P anywhere and a bare P
//     only at a path boundary, followed by a path boundary; a subpackage
//     with its own entry is never matched through its parent, and a
//     directory prefix or a P/... pattern is rewritten only when everything
//     below P moves alike. A directory above moved packages that has no entry
//     and no file of its own, such as internal/server/gen, maps too once its
//     packages all move alike, because the Makefile and the coverage
//     configuration name it; a legacy root never maps.
//   - The history set (CHANGELOG.md, research, evidence, decision 0044, the
//     repository layout document, the path map, migration/README.md and the
//     ledger files) is never rewritten and never checked.
//   - Generated Go files are moved but never edited; make generate rewrites
//     them (decision 0002).
//   - move and promote refuse a working tree with uncommitted changes to
//     tracked files, a ledger that does not validate, an entry that is not
//     pending, untracked files in a source directory, a target directory
//     that already holds Go files, a package rename that a wrapcheck
//     exception matches by name and importers that do not type-check.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Decision 0002: docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - migration/README.md, "Moving packages with pkgmove".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md.
//   - https://github.com/weaveplatform/artifactweave/issues/955
//
// # Status
//
// Implemented P9d (FND-07). FND-08 adds file entries and fix. The ledger,
// its library and this package are deleted when migration/ is promoted;
// docs/implementation/p9d-path-map.json keeps the final map.
//
// # Testing
//
//   - pkgmove_test.go runs each testdata/*.txtar archive as a repository in
//     a temporary directory, with real git under an empty configuration, the
//     go command (GOWORK=off GOPROXY=off) and a fake regeneration runner, and
//     compares the files, output and runs it lists. To fail one git command,
//     a script puts this test binary on PATH as git; TestMain then passes
//     every other command to the real git.
//   - match_test.go: the matcher's boundaries, one case per rule.
//   - unit_test.go: the failures no script reaches, such as an unreadable
//     tree, git outside a repository and a failed write, and the import
//     de-duplication and glob rules.
//   - ownership_test.go: the inventory of this repository round-trips
//     byte for byte.
package pkgmove
