// Command formatinventory validates the versioned package-format catalog and
// generates the reviewable class/client matrix from it.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// impact job of the quality gate runs it with -check.
//
// # Inputs
//
//   - The catalog, docs/implementation/package-format-inventory.json by
//     default (-inventory).
//
// # Outputs
//
//   - The matrix, docs/implementation/package-format-matrix.md by default
//     (-output).
//   - With -check, a failure instead of a write when the catalog is invalid
//     or the committed matrix differs.
//
// # References
//
//   - Decision 0042:
//     docs/research/decisions/0042-capability-scope-and-supersession.md.
//   - docs/implementation/package-format-inventory-notes.md.
//   - https://github.com/weaveplatform/artifactweave/issues/755
//
// # Status
//
// Implemented P9c (issue 755).
package main
