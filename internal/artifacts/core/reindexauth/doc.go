// Package reindexauth rechecks, before a queued remote reindex runs, that
// the caller who requested it still holds the credential and the Manage
// permission it was requested with. Queued work can run long after the
// request, so a revoked token, a changed password or a withdrawn grant must
// stop it.
//
// # Role
//
// artifacts module, core layer: rechecks the credential a queued reindex
// was requested with before it runs. The application installs New's check
// as the item service's ReindexAuthorize, which hands it to the format as
// format.Env.CheckReindex for each queued reindex.
//
// # Inputs
//
//   - The work item's credential reference (format.WorkRequest.Credential,
//     a kernel.CredentialReference) and its requester (WorkRequest.By).
//   - Driven: access/api's Authenticator, whose Current restores the
//     credential as it is now, and its Authorizer, which evaluates the
//     caller's permissions as they are now.
//
// # Outputs
//
//   - New: the check, which allows the reindex (nil) or denies it with a
//     permanent work error, so the job stays parked instead of retrying;
//     other failures are wrapped, so the work retries.
//
// # Invariants
//
//   - Only the credential reference in the queue payload is trusted, never
//     a stored admin flag, group or permission.
//   - The credential reference JSON is persisted in work items and stays
//     byte-identical; internal/kernel/credential_golden_test.go pins it
//     (ACC-02).
//
// # References
//
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 223 (core/reindexauth) and line 1010 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1028
//
// # Status
//
// Implemented P9. Moved here in P9d.
//
// # Testing
//
// reindex_test.go runs the check over the access test kit (accesstest): a
// queued token keeps its scope and the caller's current authority, and a
// caller who lost Manage is refused.
package reindexauth
