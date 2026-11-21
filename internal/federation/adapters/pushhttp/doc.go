// Package pushhttp calls normal authenticated destination repository APIs for
// the federation module's single-push driver.
//
// # Role
//
// Federation adapter. Factory reads current sealed proxy configuration inside
// the shared metadata transaction, constructs immutable guarded connections
// without DNS or HTTP, and owns their bounded pool. Every network exchange
// refuses a metadata transaction. The core driver owns durable intent, source
// authority, configuration fences, checksum verification and retry decisions.
//
// # Inputs
//
// Current unsealed single-target configuration, trusted startup destination/TLS
// policy, and a stable purpose-specific private revision key.
//
// # Outputs
//
// A port.PushConnection using normal PUT, storage metadata, nonrecursive property
// PATCH and exact-file DELETE; no target response supplies a request URL.
//
// # Invariants
//
// Credentials stay on the configured origin. Response errors use closed safe
// classifications. File metadata requires computed SHA-256 and size; only the
// documented no-properties 404 denotes an empty property set, followed by a
// second item read. An observed folder never authorizes recursive deletion.
// Native DELETE has no documented conditional checksum operation, so the fresh
// pre-delete snapshot cannot guarantee atomicity against independent writers.
//
// AQL pages use path/name keyset seeks. Permission filtering can underfill an
// AQL limit; such a page is verified by a streamed deep storage listing, keeping
// only limit+1 next keys. [I] The fallback has a 64 MiB wire cap and may rescan
// under restricted permissions. Exceeding the cap, malformed or truncated
// responses fail the whole page, never establish EOF. No offset cursor is used
// because delete reconciliation changes the result set between pages.
// The target must permit its native search and file-list APIs; their license or
// permission refusals fail the operation instead of authorizing an empty scope.
//
// # References
//
//   - https://github.com/weaveplatform/artifactweave/issues/1761.
//   - https://docs.jfrog.com/artifactory/docs/aql-query-output.
//   - https://docs.jfrog.com/artifactory/docs/aql-query-execution.
//   - https://docs.jfrog.com/artifactory/reference/getstorageitem.
//   - https://docs.jfrog.com/artifactory/reference/updateItemProperties.
//   - https://docs.jfrog.com/artifactory/reference/deleteItem.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// P11 REP-02 single-push target adapter. The federation module opens its factory
// at application startup and closes it after the owning workers stop.
//
// # Testing
//
// Owned chi HTTP fixtures verify paths, authenticated effects, metadata
// replacement, safe errors, bounded listing and current connection policy.
// test/server/federation separately qualifies the production adapter against a
// real target process on SQLite/PostgreSQL, including interrupted acknowledgements,
// restart, revocation, native refusals, metadata discrepancies and timeouts.
package pushhttp
