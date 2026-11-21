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
// Cache candidates pin all path representations and format-owned companions;
// exact source absence permits local removal/checkpoint work under the final
// execution fence, without an upstream mutation.
// Public supplies the published ErrPull recovery categories while preserving
// errors.Is causes. Offline and no-store owners refuse admission before peer
// discovery; a transient response cannot become a retained cache receipt.
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
// including virtual upstream discovery and verified cache reconciliation.
// Docker/OCI/Helm OCI storage files use normal native registry/cache identities;
// canonical aliases are deduplicated before bounded pagination. Native absence
// probes protect still-live manifest-list and tag/blob aliases from deletion.
// Federation's bounded Pull driver now persists traversal/intent state and joins
// actual owner receipts before completion. Two-app SQLite/PostgreSQL tests cover
// native manifests, lists and Helm charts, offline reads and storage aliases.
// Broader native/provider CI qualification remains open; a selected or cached
// snapshot alone does not complete a logical run.
package pull
