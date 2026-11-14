// Package idempotency runs a keyed request's local transactional effect at
// most once under its Idempotency-Key: while the key is unexpired, a retry of
// a committed effect gets the stored receipt, confirmed against the current
// state, instead of running the effect again. It holds the replay every keyed
// operation shares, the gate that refuses a key where none is accepted, and
// the port of the receipt store.
//
// # Role
//
// platform layer: replay of local transactional effects under an idempotency
// key. Retry safety belongs to no module. The router runs Gate ahead of
// authentication and admission, and installs Strict on every generated
// server; an operation whose API description references the shared
// IdempotencyKey parameter is wrapped, and its module supplies a Binding
// (decision 0043, "Replay for keyed operations"). The replay turns the key,
// the credential, the operation and the resource into a ReplayRequest and
// runs the generated handler as the effect through ReplayStore. The port and
// its values are declared here, apart from any store, so that the platform
// imports no module; the platform's store adapters (platform/store/sql and
// platform/store/inmem) implement ReplayStore over the api_replays table, and
// the metadata store's Replays returns theirs. The package imports the
// kernel, platform/tx, platform/httpkit and the generated operation table
// (platform/gen/operations).
//
// # Inputs
//
//   - The generated operation table: which operations declare the
//     IdempotencyKey parameter, for Gate and Strict.
//   - From the router: the request's Idempotency-Key values, its principal
//     and its exact body (httpkit.RawBody, kept for requests Keyed
//     reports), and the generated strict server's request object.
//   - From the module that serves a keyed operation: its Binding (the
//     canonical resource, the effect parameters the fingerprint adds, the
//     reload that confirms a committed effect and builds its answer, and the
//     error mapping), and from its handler the resource ID, through Report.
//   - ReplayStore and the metadata store's tx.Runner, which Replay holds.
//
// # Outputs
//
//   - Gate: the middleware that answers 400 to a key on any operation that
//     does not declare one, or to more than one key.
//   - Strict: the generated servers' strict middleware, which runs a keyed
//     request under Replay and passes every other request through.
//   - Replay and Effect: Do runs an effect and its reload once under a
//     request, within a budget, after pruning a bounded batch of expired
//     receipts. Retention, Budget and PruneLimit are its constants.
//   - Binding, Report, Keyed, ParseKey and Header.
//   - ReplayStore: Do, which runs the effect or replays its receipt, and
//     Prune, which removes expired receipts in bounded batches.
//   - ReplayRequest, with Validate, which checks the digests and the
//     retention interval.
//   - ReplayReceipt: the resource ID, the expiry and whether the call
//     replayed a committed effect.
//   - ErrReplayMismatch, which Strict answers with 422.
//
// # Invariants
//
//   - Which operations accept a key is the operation table's to say: Gate
//     and Strict read it, and the router's binding test fails while an
//     operation that declares the parameter has no binding, or a binding
//     names one that does not. A keyed operation without a binding answers
//     500 rather than run unprotected.
//   - A request without a key runs exactly as it would unwrapped.
//   - Scope, key and fingerprint are SHA-256 digests, never credentials, raw
//     keys or request bodies; Validate refuses anything else with an error
//     that wraps kernel.ErrInvalid.
//   - The claim, the handler's mutations, the reload and the receipt commit
//     together, in the caller's metadata transaction when there is one, and
//     any error rolls them all back, so a crash leaves no pending state to
//     recover. A handler that reports no resource commits nothing either.
//   - A receipt carries no response body, so no secret enters the replay
//     store; a replay's answer is the reload's, built from current state.
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
//     and its expiry, and what a replay rechecks; "Replay for keyed
//     operations": the shared replay and its bindings; "A key where none is
//     accepted": the gate; "Retained validation": the tests that keep it).
//   - How to add a keyed operation: docs/research/api-reliability/retrying.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": platform/idempotency is one of the new
//     packages the target layout does not name).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 40 and 41 (the platform imports no module and no gateway) and
//     line 727 (platform/events' idempotency.go, consumer-side event dedupe,
//     a different concern from this package).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/890
//   - https://github.com/weaveplatform/artifactweave/issues/989
//   - https://github.com/weaveplatform/artifactweave/issues/1406
//
// # Status
//
// Implemented with decision 0043 (issue 890, pull request 905) and released
// in v0.2.0, the P9c release: the replay port in the metadata store, with its
// SQL and in-memory stores, and protected build appends. Moved here in P9d:
// the port and its values are declared here. IDEM-01 (issue 1406) added the
// shared replay (Replay, Strict and Binding) and the table-driven Gate, and
// moved build append onto them.
//
// # Testing
//
//   - run_test.go: Do refuses invalid requests and missing functions, stops
//     on a prune failure, answers a deadline within its budget, and keeps
//     the deadline behind an error a store maps.
//   - keyed_test.go: ParseKey's accepted and refused keys; Strict over an
//     in-memory store runs a handler once and replays it, passes unkeyed and
//     unwrapped operations through, checks the resource before the key,
//     refuses a missing, anonymous, registry or otherwise unstable
//     credential, answers an unbound operation 500 and a store-less replay
//     501, fingerprints effect parameters, rolls back a failed handler,
//     reload or unreported resource, and answers 503 at its budget.
//   - gate_test.go: over the generated table the gate answers as main's
//     hand-written gate did for every method, path and key combination
//     tried; its wording names build append only while that is the only
//     keyed operation; the refusal uses the path's error envelope; a bad
//     table fails.
//   - The platform's replay contract (the Replays, ReplayRollback,
//     ReplayInProgressCancellation and Keyed* subtests of
//     platform/store/storetest: concurrent identical requests run the effect
//     once and replay it after, a changed fingerprint is ErrReplayMismatch,
//     another scope and an expired key run new effects, bounded pruning, the
//     requests Validate refuses, failed and nested transactions, a refused
//     reload, and a duplicate that stops waiting when its context or the
//     budget ends) runs on the platform's in-memory and SQLite stores and on
//     the metadata store's in-memory, SQLite and PostgreSQL stores.
//   - The metadata store's SQLite and PostgreSQL stores' TestAccReplayProcessCrash
//     and TestAccKeyedReplayProcessCrash kill a process inside the receipt
//     transaction, and the PostgreSQL store's
//     TestAccReplayPruneDoesNotDeleteRenewal keeps a renewed receipt that a
//     concurrent prune had selected.
//   - The router's keyed_test.go checks the bindings against the table and
//     the gate through the router; build append's binding is tested in the
//     artifacts module's HTTP adapter and in test/server/artifacts.
package idempotency
