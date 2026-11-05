// Package kernel holds the values every module shares: the store sentinel
// errors, keyset paging, deep copies of stored values, content checksums and
// the item path rules. It has no behaviour beyond parsing and paging, so a
// module, a platform package or the gateway can use it without depending on
// anything else.
//
// # Role
//
// kernel layer: the shared values below every module. It imports only the
// standard library and, once the request principal arrives
// (https://github.com/weaveplatform/artifactweave/issues/965),
// platform/httpkit/apierror. It never imports a module, the gateway or app.
//
// # Inputs
//
// None: the package holds values and pure functions.
//
// # Outputs
//
//   - Sentinel errors that store adapters wrap and callers test with
//     errors.Is: ErrNotFound, ErrConflict, ErrInvalidCursor and ErrInvalid.
//   - Keyset paging: Page with Normalize and Key, DefaultLimit, MaxLimit,
//     Cursor and Trim.
//   - Clone, the deep copy a store hands out so that no caller shares stored
//     state, and Normalise, the value every backend reads back.
//   - Checksums, the hex digests of an item's content.
//   - Item paths: SplitItemPath, RootPath and ErrInvalidItemPath.
//
// Scopes and token values follow with
// https://github.com/weaveplatform/artifactweave/issues/962, and the request
// principal with https://github.com/weaveplatform/artifactweave/issues/965.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "store: not found", "store: conflict", "store: invalid
//     cursor", "store: invalid record" and "invalid item path".
//   - Sentinels are compared by identity through errors.Is. Until the
//     migration's bridges are deleted
//     (https://github.com/weaveplatform/artifactweave/issues/1101), the
//     legacy names are these same values, not copies, so a check under either
//     name matches an error wrapped under the other.
//   - RootPath is "." because AQL reports root items that way and the item
//     table stores them the same way, so a root listing and a nested listing
//     are one query (decision 0004).
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Persistence" (what the kernel holds and imports) and "Type-alias
//     bridges".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 118 to 125, and "Summary", dependency rule
//     4 (lines 40 to 41).
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0020: docs/research/decisions/0020-domain-model.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/961
//
// # Status
//
// Implemented P2, with the SHA-512 checksum added in P7; staged in P9d.
//
// # Testing
//
//   - TestUnitPage, TestUnitTrim: limits, cursor decoding and the cursor of
//     the next page.
//   - TestUnitCloneAndNormalise: copies share no memory with their input,
//     and normalised times and durations have the precision of the SQL
//     columns.
//   - TestUnitSplitItemPath: the canonical parent, name and depth, and the
//     segments that are refused.
package kernel
