// Package oidc is the access module's workload OpenID Connect integration
// (research 21): the providers whose ID tokens artifactweave trusts, the
// identity mappings that turn a token's claims into an access token, and the
// token exchange that CI systems call (POST /access/api/v1/oidc/token).
// Providers and mappings are configuration documents (configdoc.ConfigOIDC,
// configdoc.ConfigOIDCMapping) stored in their wire form, so they read back
// exactly as written.
//
// # Role
//
// access module, core layer: workload OIDC. The access module's HTTP
// adapter serves the provider, mapping and exchange operations through
// Service; no other module calls it. A subject token is verified through
// core/idtoken and the access token issued through core/token. The package
// imports the standard library, the kernel, platform/configdoc and the
// module's core/idtoken and core/token.
//
// # Inputs
//
//   - Service: a Store whose Configs hold the providers and mappings, the
//     token service, the ID token verifier and the clock.
//   - Providers, Provider, CreateProvider, UpdateProvider and
//     DeleteProvider: provider names and Provider documents as the Access
//     API sends them.
//   - Mappings, Mapping, CreateMapping, UpdateMapping and DeleteMapping: a
//     provider's name, a project to list, and Mapping documents.
//   - Exchange: an ExchangeRequest, the exchange's form fields.
//   - CanonicalType: a provider type as a client spells it.
//
// # Outputs
//
//   - Provider and Mapping documents as they were written; a mapping's
//     project is derived from the project roles its scope names.
//   - Exchanged: the access token that the first matching mapping
//     describes, issued by the token service, and its user name.
//   - ErrNotFound, ErrInvalid, ErrNoMatch and ErrUnverified, the last
//     wrapping idtoken's errors; the HTTP adapter answers them 404, 400,
//     400 and 401.
//
// # Invariants
//
//   - A mapping's project cannot change once it is stored, and a mapping
//     names at most one project.
//   - An exchange tries the request's project's mappings first, then the
//     global ones, never another project's; within each, lower priorities
//     first and the first created among equals. The first mapping whose
//     claims the token satisfies decides the token issued.
//
// # References
//
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md,
//     lines 22 to 25 (workload OIDC exchange is implemented, apart from
//     human login).
//   - Research 21: docs/research/21-oidc-token-exchange.md.
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (the tokens an exchange issues).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": idtoken stays a separate package).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 283 (core/oidc) and line 1031 (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/83
//   - https://github.com/weaveplatform/artifactweave/issues/1073
//
// # Status
//
// Implemented P4: providers, identity mappings and the token exchange
// (pull request 145), with mappings aligned with 7.x after (pull requests
// 468, 486 and 499). Staged in P9d, where Service.Store needs only the
// configuration documents.
//
// # Testing
//
//   - oidc_test.go: TestUnitCanonicalType, TestUnitProviders,
//     TestUnitMappings, TestUnitCombinations, TestUnitPatternSyntax,
//     TestUnitProjectRolesWithoutUser, TestUnitMappingNamesOneProject,
//     TestUnitExchangeOrder, TestUnitClaimMatching, TestUnitExchangeTokens,
//     TestUnitExchangeRefuses and TestUnitProviderTypes; TestUnitStoreFailures
//     and TestUnitCorruptDocuments fail the configuration store and corrupt
//     its documents.
//   - pattern_test.go: TestUnitPatterns, TestUnitGroupPatterns,
//     TestUnitUserScope and TestUnitResourcePatterns.
//
// The tests run on the metadata store's in-memory store, with idtokentest
// as the provider.
package oidc
