// Package idempotency holds the port through which a retried request runs
// its local transactional effect at most once under an idempotency key: while
// the key is unexpired, a retry of a committed effect gets the stored receipt
// instead of running the effect again.
//
// # Role
//
// platform layer: replay of local transactional effects under an idempotency
// key. Retry safety belongs to no module: the HTTP boundary turns an
// Idempotency-Key header, the credential, the operation and the resource into
// a ReplayRequest, and the build service runs a protected build append
// through ReplayStore. The port and its values are declared here, apart from
// any store, so that the platform imports no module; the platform's store
// adapters (platform/store/sql and platform/store/inmem) implement
// ReplayStore over the api_replays table, and the metadata store's Replays
// returns theirs. The package imports the kernel and the standard library.
//
// # Inputs
//
//   - Request digests from the HTTP boundary: the scope (the authenticated
//     credential reference, the operation and the canonical resource), the
//     key and the exact request body (the fingerprint), each a lowercase
//     SHA-256 hex digest, with the caller's clock reading and the receipt's
//     expiry.
//   - An effect from the service layer: a function that makes its mutations
//     through the context it is given and returns a positive, stable
//     resource ID.
//
// # Outputs
//
//   - ReplayStore: Do, which runs the effect or replays its receipt, and
//     Prune, which removes expired receipts in bounded batches.
//   - ReplayRequest, with Validate, which checks the digests and the
//     retention interval.
//   - ReplayReceipt: the resource ID, the expiry and whether the call
//     replayed a committed effect.
//   - ErrReplayMismatch, which the HTTP boundary answers with 422.
//
// # Invariants
//
//   - Scope, key and fingerprint are SHA-256 digests, never credentials, raw
//     keys or request bodies; Validate refuses anything else with an error
//     that wraps kernel.ErrInvalid.
//   - The receipt and the effect's mutations commit together, in the
//     caller's metadata transaction when there is one, and any error rolls
//     both back, so a crash leaves no pending state to recover.
//   - A receipt carries no response body, so no secret enters the replay
//     store.
//   - An unexpired key reused with another fingerprint is ErrReplayMismatch;
//     an expired key may run a new effect.
//   - Now and Expires come from the caller's clock, so replicas need
//     synchronized clocks.
//   - It is not an outbox for external calls: an effect outside the metadata
//     transaction needs a recovery design of its own.
//
// # References
//
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     ("Optional protected build append": the scope, the digests, the receipt
//     and its expiry, and what a replay rechecks; "Retained validation": the
//     tests that keep it).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": platform/idempotency is one of the new
//     packages the target layout does not name; "Type-alias bridges": the
//     metadata store keeps aliases of the port and its values while their
//     users move).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 40 and 41 (the platform imports no module and no gateway) and
//     line 727 (platform/events' idempotency.go, consumer-side event dedupe,
//     a different concern from this package).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/890
//   - https://github.com/weaveplatform/artifactweave/issues/989
//
// # Status
//
// Implemented with decision 0043 (issue 890, pull request 905) and released
// in v0.2.0, the P9c release: the replay port in the metadata store, with its
// SQL and in-memory stores, and protected build appends. Staged in P9d: the
// port and its values are declared here.
//
// # Testing
//
// The package declares only the port and its values, so it has no tests of
// its own. The platform's replay contract (the Replays, ReplayRollback and
// ReplayInProgressCancellation subtests of platform/store/storetest:
// concurrent identical requests run the effect once and replay it after, a
// changed fingerprint is ErrReplayMismatch, another scope and an expired key
// run new effects, bounded pruning, the requests Validate refuses, failed and
// nested transactions, and a duplicate that stops waiting when its context
// ends) runs on the platform's in-memory and SQLite stores and on the
// metadata store's in-memory, SQLite and PostgreSQL stores. The metadata
// store's SQLite and PostgreSQL stores' TestAccReplayProcessCrash kills a
// process inside the receipt transaction, and the PostgreSQL store's
// TestAccReplayPruneDoesNotDeleteRenewal keeps a renewed receipt that a
// concurrent prune had selected. The build service's AppendOnce tests and the
// HTTP boundary's idempotency tests run through the port, and the test beside
// the metadata store's bridge fails if an alias of the port, its values or
// its sentinel becomes a declared copy.
package idempotency
