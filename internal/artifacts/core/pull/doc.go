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
// Source implements api.PullSource: current runtime/source/layout bindings,
// mapped canonical selection, immutable per-entry snapshots, ordinary verified
// cache receipts and current-authority checkpoint transactions. Selected user
// properties share cache ownership rules and preserve local annotations.
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
// Decisions 0007 (remote repositories), 0032 (package format handlers), 0044 (module
// ownership), and P11 REP-03 / GitHub issue 1762.
//
// # Status
//
// The peer adapter/catalog and Generic/Maven cache admission are implemented,
// including virtual upstream discovery. Durable federation pull progress,
// deletion reconciliation and native protocol qualification remain integration
// work; a selected or cached snapshot alone does not complete a logical run.
package pull
