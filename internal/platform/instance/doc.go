// Package instance holds this instance's identity: the jfrt@<uuid> id that
// Artifactory reports as its service id, the Ed25519 key pair that peers
// know the instance by, and the peer protocol version it speaks (decision
// 0021). EnsureInstance creates the identity on first start and keeps it
// from then on.
//
// # Role
//
// platform layer: this instance's identity and the port it is stored
// through. The identity belongs to no module: access derives its token
// issuer from it, the system endpoints serve its id, the health probes read
// it, and peers will know the instance by it, so it sits in the platform.
// The composition root calls EnsureInstance before it builds the token
// service. The identity and its port are declared here, and EnsureInstance
// takes Store, the one method it calls, so that the package imports no store
// (decision 0044, "Narrowing"); the metadata store satisfies it, and the
// platform's store adapters (platform/store/sql and platform/store/inmem)
// implement InstanceStore. The package imports the kernel and the standard
// library.
//
// # Inputs
//
//   - EnsureInstance: the context and a Store, whose Instance is the
//     identity store. A new identity's id and key pair come from
//     crypto/rand.
//   - ValidateInstanceID: an id to check against the form that
//     /api/system/service_id serves and peers identify themselves by.
//
// # Outputs
//
//   - InstanceIdentity: the id, the Ed25519 public and private keys, the
//     cold-storage namespace (decision 0024) and the protocol version.
//   - ValidateInstanceID, with its sentinel ErrInvalidInstanceID, and
//     ProtocolVersion, the peer protocol version this build speaks.
//   - The port: InstanceStore, with Init, Get and Update; and Store, what
//     EnsureInstance takes.
//   - EnsureInstance, which returns nil once an identity is stored, or an
//     error whose text starts "instance identity:".
//
// # Invariants
//
//   - The id is jfrt@<uuid>, with a random (version 4) UUID, and the Access
//     token issuer jfac@<uuid> derives from it, so EnsureInstance never
//     replaces a stored identity: a token issued before a restart must
//     still verify after it.
//   - Two instances starting together on one database agree on whichever
//     identity was stored first: Init refuses a second identity with
//     kernel.ErrConflict, which EnsureInstance takes as success.
//   - The private key is sealed at rest whenever a master key is configured
//     (decision 0017) and never serialised: InstanceIdentity's JSON form
//     leaves it out.
//   - Get returns copies of the keys, so a caller that changes a returned
//     key changes nothing that is stored.
//
// # References
//
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     ("Identity": the stable id and the Ed25519 key pair generated at first
//     start; "Versioning": the protocol version).
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     ("Issuer and subject": the token issuer, taken from the id).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the sealed private key). Decision 0024:
//     docs/research/decisions/0024-cold-storage-tier.md ("Namespace").
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Narrowing").
//   - Research 15: docs/research/15-data-model.md, line 700 (the instance
//     table).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 157 (platform/instance) and lines 341 and 342 (the federation
//     module's core/port, where the target document keeps InstanceStore and
//     the peer types).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p4-identity-and-authorization and #p9d-modular-monolith-migration,
//     epic PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/74
//   - https://github.com/weaveplatform/artifactweave/issues/988
//
// # Status
//
// Implemented P2: InstanceIdentity, ValidateInstanceID, ProtocolVersion and
// InstanceStore, with the metadata store's SQL and in-memory stores (pull
// request 3). P4: the identity is created on first start, so that the token
// service can derive its issuer (issue 74, pull request 95). Staged in P9d:
// EnsureInstance and Store are declared here.
//
// # Testing
//
//   - instance_test.go: TestUnitValidateInstanceID (a jfrt@ UUID passes; a
//     short id, a jfac@ id and an empty one wrap ErrInvalidInstanceID).
//   - ensure_test.go: TestAccEnsureInstance (a failed read and a failed
//     Init return their errors; kernel.ErrConflict from Init, another
//     instance having stored its identity first, is success).
//
// The platform's instance contract (the Instance subtest of
// platform/store/storetest: Get and Update before Init are
// kernel.ErrNotFound, a second Init is kernel.ErrConflict, keys come back as
// copies, key rotation, an identity without keys, and cancelled contexts)
// runs on the platform's in-memory and SQLite stores and on the metadata
// store's in-memory, SQLite and PostgreSQL stores. The composition root's
// TestAccRuntimeTokens creates the identity on a first start and keeps it
// across a restart, and its TestAccOpenTokensWithoutIdentity checks that the
// token service does not start when the identity cannot be read back.
package instance
