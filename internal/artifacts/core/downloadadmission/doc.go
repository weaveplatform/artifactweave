// Package downloadadmission tracks hosted responses independently of public
// download statistics, including local responses that can outlive federation
// migration. It keeps no metadata transaction open over response I/O.
//
// # Role
//
// Artifacts module, core layer. HTTP owners call Begin before serving a hosted
// file or member redirect and call its completion function when the response ends.
// Federation owns cleanup coordination; this package grants no deletion permit.
//
// # Inputs
//
// An external request context, the resolved repository and immutable item
// snapshot, and the artifact store's CleanupExclusions and Items contracts.
//
// # Outputs
//
// A completion function after durable admission and item-snapshot revalidation,
// or a refusal before response bytes. Remote cached and folder responses need
// no hosted-file admission here. Public counters and statistics opt-outs keep
// their separate owner and meaning.
//
// # Invariants
//
// Local and federated hosted responses both acquire private admissions so
// migration and a stale local snapshot cannot evade a current barrier.
// Completion runs independently of request cancellation. Failed or missing
// completion leaves an ambiguous durable admission; no timer expires it.
// The caller owns response I/O and invokes completion only after it ends.
//
// # References
//
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - docs/research/decisions/0022-federated-repositories.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1798.
//
// # Status
//
// Implemented for FED-07 hosted download admission. Federation owns durable
// preparation and terminal release; retention invokes ordinary artifact removal.
//
// # Testing
//
// Item-kit admission and migration cases establish admission before response
// headers, including ranges, opt-outs, cancellation and redirects. Shared store
// contracts check durable exclusion and completion on in-memory and SQL stores.
package downloadadmission
