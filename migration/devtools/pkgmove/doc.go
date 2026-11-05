// Package pkgmove moves legacy packages and files to their ledger targets
// and proves that no stale path to them remains. It is the logic of
// tools/pkgmove: status reports where any legacy path lives now, move applies
// a ticket's ledger entries in one command, fix repairs the references that
// a ticket's file moves or the bridges leave, check is the stale-path step of
// CI, fmt keeps the ledger canonical and promote renames the staging root to
// the final root at the end of the migration.
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
//   - A ticket ID (move -ticket, fix -ticket, check -ticket, status
//     -ticket), a staged or test/ directory to re-home (move -from -to), a
//     legacy path (status), or the test files to convert to external tests
//     (fix -ticket -external).
//   - Type information of the packages that import a package whose name
//     changes, and for fix of the packages a split touches, their importers
//     and the importers of the bridged packages, loaded with their tests and
//     the e2e tag for linux, darwin and windows. fix loads a package that
//     does not compile too, and reads its type errors.
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
//   - move of file entries, after the package entries: git mv of each listed
//     file, or each tracked file of an asset directory, to the entry's
//     target; a moved Go file takes the target's package name, a test keeps
//     its style, and an in-package test that joins a directory of external
//     tests becomes one, with a notice. A moved fuzz target's FUZZ_PKGS entry
//     follows it, and a moved test's ownership inventory keys follow the
//     rules of docs/testing/server-test-ownership.md, "Moving a root test".
//   - fix -ticket: each identifier that the ticket's file moves left
//     unresolved is pointed at the package that now declares it, qualified
//     with an import (named when its name is taken) or bare in that package,
//     and an import whose every use moved goes; -external FILE... converts
//     test files to the external test package. What fix cannot rewrite is
//     printed as file:line: message, with exit status 1: an unexported or
//     test-only declaration across the new boundary, a method set split from
//     its type, an import cycle, a test that reads its package's source, a
//     //go:embed pattern that matches nothing, a generated file and a file no
//     load compiles. -report-only prints the plan only.
//   - fix -bridges: each use of a declaration in a bridge file of a legacy
//     package is pointed at the declaration it aliases; uses that would
//     change meaning, such as an embedded field's name or an assignment to a
//     var bridge, are reported instead.
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
//     them (decision 0002). fix edits no generated file and no bridge file.
//   - move and promote refuse a working tree with uncommitted changes to
//     tracked files, a ledger that does not validate, an entry that is not
//     pending, untracked files in a source directory, a target directory
//     that already holds Go files, a package rename that a wrapcheck
//     exception matches by name and importers that do not type-check. move
//     also refuses a file entry whose old path the tree does not hold in the
//     entry's shape, two moves to one path, a move onto a tracked file and a
//     moved fuzz target whose seed corpus has no file entry to follow it.
//   - fix repairs only what move did: it refuses a ticket whose file move
//     entries are not done or that has none, and a moved Go file that does
//     not parse.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Decision 0002: docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - migration/README.md, "Moving packages with pkgmove" and "Splitting a
//     package".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md.
//   - https://github.com/weaveplatform/artifactweave/issues/955
//   - https://github.com/weaveplatform/artifactweave/issues/956
//
// # Status
//
// Implemented P9d (FND-07, FND-08). The ledger, its library and this package
// are deleted when migration/ is promoted;
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
//   - split_test.go runs each testdata/split/*.txtar archive the same way,
//     one named test per archive for the file moves, fix, fix -external, fix
//     -bridges and the inventory of moved tests; it also holds the unit
//     tests of fix's helpers and of the files they cannot read.
//   - match_test.go: the matcher's boundaries, one case per rule, and how
//     file entries share paths with their package's entry.
//   - unit_test.go: the failures no script reaches, such as an unreadable
//     tree, git outside a repository and a failed write, and the import
//     de-duplication and glob rules.
//   - ownership_test.go: the inventory of this repository round-trips
//     byte for byte, and each rule for a moved root test.
package pkgmove
