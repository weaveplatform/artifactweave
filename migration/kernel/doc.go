// Package kernel holds the values every module shares: the store sentinel
// errors, keyset paging, deep copies of stored values, content checksums, the
// item path rules, token scopes and the token values every module reads. It
// has no behaviour beyond parsing, paging and comparing scopes, so a module, a
// platform package or the gateway can use it without depending on anything
// else.
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
//   - Token scopes: Scope, a list of ScopeEntry values, with ScopeKind,
//     ScopeActions and their constants. ParseScope reads a scope string of
//     at most MaxScopeLength characters and refuses any other with
//     ErrInvalidScope; UserScope is an identity token's default scope.
//     String renders the canonical form, IsAdmin and Narrows compare scopes,
//     and ScopeActions.Covers compares actions.
//   - Token values: TokenIdentity, with IdentityStored, IdentityTransient
//     and IdentitySynthetic, says how a token's subject resolves to a user;
//     RegistryAccess, a list of RegistryGrant values, is what a registry
//     token grants.
//
// The request principal follows with
// https://github.com/weaveplatform/artifactweave/issues/965.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "store: not found", "store: conflict", "store: invalid
//     cursor", "store: invalid record", "invalid item path" and "invalid
//     token scope".
//   - Sentinels are compared by identity through errors.Is. Until the
//     migration's bridges are deleted
//     (https://github.com/weaveplatform/artifactweave/issues/1101), the
//     legacy names are these same values, not copies, so a check under either
//     name matches an error wrapped under the other.
//   - RootPath is "." because AQL reports root items that way and the item
//     table stores them the same way, so a root listing and a nested listing
//     are one query (decision 0004).
//   - Scopes only narrow (decision 0005): a token issued from another token,
//     or by a user who is not an administrator, must narrow the scope it
//     comes from, and Narrows is that check.
//   - String renders any scope that ParseScope accepted to a string that
//     ParseScope reads back as the same scope. Token records store a scope as
//     that string and a token's scp claim carries it, so a scope that did not
//     survive the trip would change what a stored or issued token grants.
//   - Token records store TokenIdentity values and a registry token's idn
//     claim carries them; the registry claim carries RegistryAccess as JSON;
//     and the credential reference that queued work keeps for a registry
//     token's caller holds both. The values and JSON names therefore stay as
//     they are, and TestUnitCredentialReferenceGolden in the access module
//     pins the JSON.
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
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md:
//     scopes only narrow, the transient and synthetic token identities, and
//     the registry token defaults.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (fuzz seed corpora in the three-OS Go test matrix).
//   - Decision 0020: docs/research/decisions/0020-domain-model.md.
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md,
//     "Authentication on the registry": on the registry, the effective
//     actions are those the token grants, intersected with the caller's
//     current permissions.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Scope grammar".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/961
//   - https://github.com/weaveplatform/artifactweave/issues/962
//
// # Status
//
// Implemented P2 to P7: the store values, paging and item paths in P2, with
// the SHA-512 checksum added in P7; token scopes with the P0 to P2 baseline,
// with build scopes added in P5; token identities in P4; registry grants in
// P6. Staged in P9d.
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
//   - TestUnitParseScope, TestUnitParseScopeErrors: the scope grammar, its
//     canonical form and the strings it refuses.
//   - TestUnitScopeHelpers, TestUnitScopeNarrows: UserScope, IsAdmin,
//     ScopeActions.Covers and the narrowing rule.
//   - FuzzParseScope, with its seed corpus in testdata/fuzz/FuzzParseScope:
//     whatever ParseScope accepts renders, within MaxScopeLength, to a
//     canonical string that parses back to the same scope and narrows
//     itself. make test runs the seeds (FUZZ_PKGS) and CI runs them in the
//     three-OS Go test matrix; go test -fuzz=FuzzParseScope explores
//     further.
package kernel
