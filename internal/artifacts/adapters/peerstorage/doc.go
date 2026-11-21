// Package peerstorage reads ordinary peer Storage API metadata through P9.
//
// # Role
//
// This artifacts adapter implements core/pull.Peer. It selects bounded windows
// from complete ordinary HTTP storage listings without Smart negotiation.
//
// # Inputs
//
// New takes the owning remote source service, current caller/configuration,
// upstream selection root and the owner's canonical permission scope. Files
// accepts a bounded local continuation; Inspect and Properties accept paths
// inside that selection.
//
// # Outputs
//
// Reader returns verified computed file facts, folder identity and separate
// property snapshots. Its cursors bind the source lifetime, options revision and
// selected scope using a hash, never raw credentials or options.
//
// # Invariants
//
// All network work uses P9 authorization, transport, offline and circuit policy.
// Management documents are transient and cannot populate artifact/cache state.
// No arbitrary URLs, writes or upstream pagination parameters are exposed.
// Complete metadata is validated before a page is returned; no transaction
// spans HTTP. The owner authorizes mapped file paths before consuming their
// facts or admitting content.
//
// # References
//
// Decisions 0007 (remote repositories), 0032 (package format handlers), 0044 (module
// ownership), and P11 REP-03 / GitHub issue 1762.
//
// # Status
//
// Ordinary read-only discovery is implemented. Durable pull orchestration,
// native content admission and public execution are separate consumers; this
// package does not claim a completed replication run.
//
// # Testing
//
// TestAcc cases use chi and real P9 transport with an owning repository/store.
// They cover complete bounded pages, restart cursors, metadata integrity,
// computed checksums, offline/no-store/alias refusal, redirect limits and
// blocked response revocation or source/credential changes. Source contract
// unit tests reject unrecognized transient query modes before transport.
package peerstorage
