// Package kernel holds the values every module shares: the store sentinel
// errors, keyset paging, deep copies of stored values, content checksums, the
// item path rules, token scopes, the token values every module reads, and the
// request principal with its context accessors, auth errors and credential
// references. It has no behaviour beyond parsing, paging and comparing scopes
// and carrying the principal, so a module, a platform package or the gateway
// can use it without depending on another module.
//
// # Role
//
// kernel layer: the shared values below every module. It imports only the
// standard library and platform/httpkit/apierror, whose errors give the auth
// sentinels their HTTP status and message. It never imports a module, the
// gateway or app.
//
// # Inputs
//
// None: the package holds values and functions that do no I/O.
//
// # Outputs
//
//   - Sentinel errors that store adapters wrap and callers test with
//     errors.Is: ErrNotFound, ErrConflict, ErrInvalidCursor and ErrInvalid;
//     and ErrStore, which marks the store's own failure beside the driver's
//     error rather than a verdict on the record.
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
//   - The request principal: Principal, who a request acts as, with
//     AnonymousUser and Anonymous, the principal of a request without
//     credentials. WithPrincipal stores a principal in a context and
//     FromContext reads it back; PrincipalBinding identifies the call that
//     stored it, which access binds the request's permission decider to.
//     RequireAuthenticated and RequireAdmin refuse a request without a user
//     or without an administrator.
//   - The auth sentinels, API errors with an HTTP status: ErrBadCredentials
//     and ErrUnauthenticated (401), and ErrForbidden, ErrAPIRestricted,
//     ErrPasswordExpired and ErrAccountLocked (403).
//   - Credential references: CredentialReference, with RegistryReference
//     for a registry token's caller, is what queued work keeps of the
//     principal that scheduled it, and DownloadCredential is what a signed
//     download envelope keeps. Principal.CredentialReference,
//     CredentialReference.Principal and Principal.DownloadReference convert
//     between them.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "store: not found", "store: conflict", "store: invalid
//     cursor", "store: invalid record", "invalid item path" and "invalid
//     token scope".
//   - Sentinels are compared by identity through errors.Is.
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
//     they are, and TestUnitCredentialReferenceGolden pins the JSON.
//   - Credential-reference JSON is persisted in work items (decision 0033)
//     and hashed into replay scopes (decision 0043), and signed download
//     envelopes carry a DownloadCredential, so the fields and JSON tags of
//     CredentialReference, RegistryReference and DownloadCredential never
//     change; TestUnitCredentialReferenceGolden pins the bytes.
//   - The auth sentinels' messages are what JFrog clients print, and their
//     401 and 403 split is Artifactory's (decision 0005), so statuses and
//     texts stay byte-identical: 401 "Bad credentials" and "Authentication
//     is required"; 403 "Forbidden", "API access is temporarily
//     restricted", "Password has expired" and "User is Locked.\nContact
//     System Administrator to Unlock The Account.".
//   - The principal carries no permission evaluator (decision 0006): access
//     evaluates a request's permissions once and binds the evaluator to the
//     WithPrincipal call, through PrincipalBinding, so an evaluator never
//     answers for a principal that another call stored.
//   - Only access and the kernel read or set PasswordRevision,
//     IsTokenAuthenticated and TokenSubject, which access trusts when it
//     rechecks a credential; TestUnitPrincipalInternals in internal/layout
//     enforces it.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Persistence" (what the kernel holds and imports), "Composition and
//     HTTP" (the per-request decider bound through PrincipalBinding) and
//     "Names the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 118 to 125, and "Summary", dependency rule
//     4 (lines 40 to 41).
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md:
//     scopes only narrow, the transient and synthetic token identities, the
//     registry token defaults, and the 401 and 403 split with the messages
//     JFrog clients print.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md:
//     permissions are evaluated once per request, in access, so the
//     principal carries no evaluator.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (fuzz seed corpora in the three-OS Go test matrix).
//   - Decision 0020: docs/research/decisions/0020-domain-model.md.
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md,
//     "Authentication on the registry": on the registry, the effective
//     actions are those the token grants, intersected with the caller's
//     current permissions.
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md:
//     work items hold an internal credential reference, never a password or
//     bearer token.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md: the
//     replay scope is a SHA-256 digest of the credential reference, and
//     "Temporary restrictions and administrative recovery".
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Scope grammar".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/961
//   - https://github.com/weaveplatform/artifactweave/issues/962
//   - https://github.com/weaveplatform/artifactweave/issues/965
//
// # Status
//
// Implemented P2 to P9: the store values, paging and item paths in P2, with
// the SHA-512 checksum added in P7; token scopes with the P0 to P2 baseline,
// with build scopes added in P5; the request principal and its 401 and 403
// errors in P3; token identities and the password expiry and account lock
// errors in P4; registry grants in P6; the credential references in P9. The
// API restriction error came with decision 0043. Moved here in P9d.
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
//   - TestUnitPrincipalContext: WithPrincipal and FromContext, a binding
//     per WithPrincipal call that derived contexts keep, and the 401 and 403
//     split of RequireAuthenticated and RequireAdmin.
//   - TestUnitCredentialReferenceGolden, with
//     testdata/credential_reference.golden.json: the JSON of every kind of
//     credential reference and download credential, byte for byte, and a
//     reference that goes through Principal and back unchanged. Rewrite the
//     golden file with -update only for a deliberate format change.
package kernel
