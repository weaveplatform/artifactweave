// Package api is the access module's published surface: the permission
// vocabulary, signing key pairs and certificates, the subject and repository
// that a permission question names, and the helpers that recognise a token
// by its shape.
//
// # Role
//
// access module, api layer. Other modules reach access only through this
// package, and it imports only kernel and the standard library (decision
// 0044, dependency rules 5 and 6), so it declares its own types. Artifacts
// code uses the vocabulary, Keypair, Certificate, Repo and Subject heavily,
// and an api package cannot import access core, so they are declared here
// rather than in the core packages that evaluate them.
//
// # Inputs
//
// None: the package holds values and pure functions.
//
// # Outputs
//
//   - The permission vocabulary: ResourceType and its constants, the
//     special targets (TargetAny, TargetAnyLocal, TargetAnyRemote and
//     TargetAnyDistribution) with BuildInfoRepository and
//     ReleaseBundlesRepository, and Actions with its constants. AccessNames,
//     LegacyNames and Letters spell a set of actions in the three wire
//     forms; ParseAccessActions, ParseLegacyActions and ParseLetters read
//     them back and refuse an unknown name with ErrUnknownAction.
//   - Keypair, of a KeypairType, and Certificate: the signing key pairs and
//     the client certificates of remote repositories.
//   - Subject, who a request acts as, and Repo, the repository a permission
//     target names.
//   - Token shapes: IsReferenceToken and LooksLikeToken tell a token from a
//     password; ErrNotRevocable refuses to revoke a short-lived token.
//
// # Invariants
//
//   - Error texts are observable in API responses and logs, so they stay
//     byte-identical: "unknown permission action" and "token: token is not
//     revocable". Until the migration's bridges are deleted
//     (https://github.com/weaveplatform/artifactweave/issues/1101), the
//     legacy names of ErrUnknownAction and ErrNotRevocable are these same
//     values, so errors.Is matches under either name.
//   - Actions marshals to the list of its Access names, such as
//     ["READ","WRITE"], which stored permission documents hold; the JSON
//     stays byte-identical.
//   - Keypair and Certificate never serialise a private key or passphrase
//     (json:"-"); the stores seal them at rest (decision 0017).
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" and "Type-alias bridges".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 258 to 272, and "Access and identity",
//     lines 529 to 574.
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (reference tokens, tokens sent as a Basic password, revocable tokens).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission model and its three wire forms).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Permissions v2", "Access tokens v1" and "Reference tokens".
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ACC (https://github.com/weaveplatform/artifactweave/issues/940), and
//     #p4-identity-and-authorization.
//   - https://github.com/weaveplatform/artifactweave/issues/963
//
// # Status
//
// Implemented P4 to P9c, staged in P9d. The contracts other modules call
// follow with https://github.com/weaveplatform/artifactweave/issues/963 and
// https://github.com/weaveplatform/artifactweave/issues/966.
//
// # Testing
//
//   - TestUnitActionsSpellings, TestUnitActionsJSON: the three spellings of
//     every action, case-insensitive Access names, ErrUnknownAction, and the
//     JSON form.
//   - TestUnitIsReferenceToken, TestUnitLooksLikeToken: the reference-token
//     form and how a Basic password is told apart from a token.
package api
