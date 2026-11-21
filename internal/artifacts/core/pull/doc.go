// Package pull defines verified selection into an owning remote cache.
//
// # Role
//
// This artifacts core package owns the private peer-selection contract consumed
// by remote-cache pull orchestration. Protocol decoding belongs to an adapter.
//
// # Inputs
//
// Peer receives a context, selection-relative upstream paths and bounded pages.
// The owning source service supplies current source/configuration authority.
//
// # Outputs
//
// Entry contains computed size/checksums and upstream item timestamps. Peer
// returns complete bounded file windows and optional property snapshots.
//
// # Invariants
//
// An entry is descriptive data, never an enduring permission or a successful
// cache-publication acknowledgement. The owner must map and authorize it before
// fetching, then revalidate authority before checkpointing effects. This
// contract supplies no upstream write or arbitrary URL operation.
//
// # References
//
// Decisions 0007 (remote repositories), 0032 (source authority), 0044 (module
// ownership), and P11 REP-03 / GitHub issue 1762.
//
// # Status
//
// The peer contract and ordinary storage adapter are implemented. Durable
// pull progress and execution remain separate integration work.
package pull
