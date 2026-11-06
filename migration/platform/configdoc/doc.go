// Package configdoc holds configuration documents: identity-provider
// settings, the descriptor sections that are not repositories or security,
// and the external secret backends. Each is stored and returned whole under
// a kind and a key and never queried by field, with its secrets kept apart
// from the document.
//
// # Role
//
// platform layer: configuration documents stored and returned whole, and
// the port they are stored through. Several modules use them: access keeps
// its OIDC providers and identity mappings in them, the configuration
// descriptor writes the sections it applies, and the remote repositories
// read their proxies and the item service its repository layouts from them.
// A port in the configuration module would make the readers depend on it,
// so the documents and the port sit in the platform (decision 0044, "Shared
// stores in the platform"). The metadata store's SQL and in-memory stores
// implement ConfigStore, the SQL store over a table of documents and one of
// sealed secrets. The package imports only the standard library.
//
// # Inputs
//
//   - Put: a ConfigDocument with its Kind and Key set, its Document as the
//     JSON that the owning service defines (usually its wire form with the
//     secrets removed) and its Secrets by name.
//   - Get and Delete: a kind and a key. List: a kind.
//
// # Outputs
//
//   - ConfigKind and its sixteen kinds: OIDC providers and identity
//     mappings, LDAP settings and group settings, SAML, Crowd, HTTP SSO,
//     Vault, proxies, property sets, layouts, backups, the mail server, the
//     reverse proxy, the GPG signing key and the curation settings.
//   - ConfigDocument: the kind and key, whether it is enabled, the document,
//     the secrets and when it was updated.
//   - The port: ConfigStore, with Put, Get, List and Delete.
//
// # Invariants
//
//   - Secrets are sealed at rest whenever a master key is configured
//     (decision 0017) and are never part of Document, so a read of the
//     document cannot leak one; ConfigDocument's JSON form leaves them out.
//   - A secret with an empty value is not stored, and Put replaces a
//     document's secrets with the ones it carries, so a key that is created
//     again starts without the deleted document's secrets.
//   - Key is unique within Kind: Put of a stored kind and key replaces the
//     document. OIDC identity mappings use "<provider>/<name>".
//   - The kind strings are stored with every document, so they never
//     change.
//   - Writing a layout document that is new or changed, or deleting one,
//     fences the remote repositories that use the layout in the same
//     transaction, so a cache fill that started under the old definition
//     cannot publish. The metadata store's stores do this until STO-12
//     makes it a Watch that the composition registers here (decision 0044,
//     "Layout fencing").
//
// # References
//
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the sealed secrets).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Ownership of shared models" and "Shared stores in the platform": why
//     the port is here and not in the configuration module; "Layout
//     fencing"; "Type-alias bridges": the shared types and the metadata
//     store keep aliases of the kinds, the document and the port while their
//     users move).
//   - Research 15: docs/research/15-data-model.md, line 759 (one table of
//     documents and one of secrets, in place of a table per family).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 312 (the configuration module's core/port, where the target
//     document puts ConfigStore).
//   - Plan: docs/implementation/plan.md#p2-stores, #p9-remote-repositories
//     and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/988
//   - https://github.com/weaveplatform/artifactweave/issues/1056
//
// # Status
//
// Implemented P2: ConfigKind with its sixteen kinds, ConfigDocument and
// ConfigStore, with the secrets sealed apart from the documents (pull
// request 3). P9: layout writes fence the remote repositories that use the
// layout (pull request 680). Staged in P9d.
//
// # Testing
//
// The package declares only a document, its kinds and a port, so it has no
// tests of its own. The metadata store's configuration document contract
// runs on its in-memory, SQLite and PostgreSQL stores: the Configs subtest
// (an empty kind or key is kernel.ErrInvalid, an empty secret is not
// stored, Get and Delete of a missing document are kernel.ErrNotFound, List
// orders by key, Get returns a copy, Put replaces a document and its
// secrets, and a key created again has no old secrets) and the
// RemoteLayoutFences subtest (which layout writes fence which remote
// repositories, and a delete that is rolled back fences nothing). The tests
// beside the shared types' and the metadata store's bridges fail if a
// kind's alias changes its type or string, or if an alias of the document,
// its kind type or the port becomes a declared copy.
package configdoc
