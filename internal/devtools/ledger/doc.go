// Package ledger reads, checks, formats and evaluates migration/ledger.json,
// the map from every legacy package directory to its place in the
// modular-monolith layout. The ledger records intent only: which ticket
// moves, merges, splits or deletes each directory, and where it goes.
// Progress is derived from the tree, so no ticket edits the ledger to record
// that its move happened.
//
// # Role
//
// devtools layer: a library behind repository tooling (tools/pkgmove) and
// the layout tests, not part of the server binary. It imports only the
// standard library.
//
// # Inputs
//
//   - migration/ledger.json, through Load. Its fields are described for
//     editors by migration/ledger.schema.json; Validate is authoritative.
//   - The repository tree under a root directory, through Status and
//     ReadDir.
//   - Import paths of legacy packages, through Translate.
//
// # Outputs
//
//   - Validation errors that wrap ErrInvalid and name the entry.
//   - The canonical bytes of a ledger (Format): one entry per line, so
//     parallel pull requests that add entries conflict only on their own
//     lines.
//   - A Report with each entry's status derived from the tree.
//   - The target import path and action of a legacy import path, and the
//     root-relative directory of a target (Dir) or file move (FileTarget).
//
// # Invariants
//
//   - LegacyRoots, LegacyRegexp and the schema's legacy patterns describe
//     the same directories.
//   - Format sorts packages and files by from and placeholders by to, and
//     Format of a loaded canonical file returns the same bytes.
//   - Status and ReadDir refuse a regular file in a directory's place with
//     the same error on every operating system.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - migration/README.md, "The ledger".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration.
//   - https://github.com/weaveplatform/artifactweave/issues/950
//
// # Status
//
// Implemented P9d. The ledger, this package and the tool built on it are
// deleted when migration/ is promoted to internal/;
// docs/implementation/p9d-path-map.json keeps the final map.
//
// # Testing
//
//   - TestUnitValidateRejectsInvalidEntries: one rejecting case per rule.
//     TestUnitValidateReportsEveryErrorInOrder: Validate reports them all.
//   - TestUnitFormatIsCanonicalAndIdempotent, TestUnitLoadRejectsUnknownFieldsAndTrailingData.
//   - TestUnitStatusDerivesFromTree: one temporary tree per status.
//     TestUnitStatusReportsEntriesInLedgerOrder,
//     TestUnitStatusFailsOnAnUnreadableDirectory.
//   - TestUnitTranslate, TestUnitDirResolvesScopes,
//     TestUnitFileTargetKeepsNamesAndAssetDirectories,
//     TestUnitLegacyRootsAgreeWithPatterns.
//   - TestUnitSchemaAgreesWithValidator: the schema's constants, enums,
//     patterns and field names equal the Go ones.
package ledger
