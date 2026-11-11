// Package settings holds the keys of the singleton settings and the port
// that stores their values as JSON. A key without a stored value has its
// default, so a setting is stored only once something changes it, and
// removing the value restores the default.
//
// # Role
//
// platform layer: the singleton setting keys and the port that stores their
// JSON values. Settings belong to no one module: access keeps its security
// policies, the anonymous access switch, the default token expiry and the
// mark of its seeded principals in them; artifacts reads the trash can and
// folder download settings; the configuration descriptor sets the anonymous
// access switch; and the composition root marks the bootstrap descriptor
// applied. A port in one module would make the others depend on it, so the
// keys and the port sit in the platform (decision 0044, "Shared stores in
// the platform"). The platform's store adapters (platform/store/sql and
// platform/store/inmem) implement SettingsStore over the settings table. The
// package imports only the standard library.
//
// # Inputs
//
//   - A key: one of the Setting* constants, or a key that a package keeps
//     for its own state, such as the token service's record of retired
//     signing keys.
//   - For Put, a value that encoding/json can encode; for Get, a pointer to
//     decode the stored value into.
//
// # Outputs
//
//   - The twelve Setting* keys: the user lock, password expiration and
//     password policies, the basic authentication switch, the default token
//     expiry, the anonymous access switch, the trash can, folder download
//     and bundle retention settings, the marks of the seeded principals and
//     of an applied bootstrap descriptor, and the fields of the Access user
//     lock policy that the other two policies have no place for.
//   - SettingsStore, with Get, Put, Delete and Keys.
//
// # Invariants
//
//   - kernel.ErrNotFound from Get means use the default, and Delete
//     restores it; Delete of a key with no stored value is
//     kernel.ErrNotFound as well.
//   - Keys are part of the stored data, so their strings never change: a
//     renamed key would leave every stored value under the old one and bring
//     back the default.
//   - A value is stored as JSON and read back by decoding into the caller's
//     type, so a setting's type changes only in ways that its stored JSON
//     still decodes into. Put of a value that cannot be encoded fails and
//     stores nothing.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": store contracts with a narrow interface per concern).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Ownership of shared models" and "Shared stores in the platform": why
//     the port is here and not in the configuration module).
//   - Research 15: docs/research/15-data-model.md, line 696 (the settings
//     table).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 137 (the settings table among the platform's own) and line 312
//     (the configuration module's core/port, where the target document puts
//     SettingsStore).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p3-repositories-generic-artifacts-cli-aql-shape and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/43
//   - https://github.com/weaveplatform/artifactweave/issues/47
//   - https://github.com/weaveplatform/artifactweave/issues/50
//   - https://github.com/weaveplatform/artifactweave/issues/988
//
// # Status
//
// Implemented P2: SettingsStore and eight of the keys (pull request 3). P3:
// SettingSeeded with the seeded principals (issue 43, pull request 56),
// SettingFolderDownload with folder downloads as archives (issue 47, pull
// request 62) and SettingBootstrapDescriptor with the descriptor apply
// (issue 50, pull request 59). SettingAccessLockSettings came with the
// Access user lock policy endpoints (pull request 242), released in v0.1.0.
// Moved here in P9d.
//
// # Testing
//
// The package declares only keys and a port, so it has no tests of its own.
// The platform's settings contract (the Settings subtest of
// platform/store/storetest: Get and Delete of a key with no stored value are
// kernel.ErrNotFound, Put replaces a value, a value that cannot be encoded
// and a decode into the wrong type fail, and Keys lists the stored keys in
// order) runs on the platform's in-memory and SQLite stores and on the
// metadata store's in-memory, SQLite and PostgreSQL stores.
package settings
