// Package terraformstate is the Terraform backend's storage (decision 0041):
// the workspaces, state versions and locks of terraformbackend repositories.
//
// # Role
//
// artifacts module, core layer. Workspaces, states and locks are items under
// workspaces/<ws>/, written through the item service (artifacts/core/item),
// so the Terraform backend format's hooks index them, a delete puts them in
// the trash can, and the storage API, AQL and replication see them as any
// other item. The TFE-shaped API that Terraform's remote backend calls is
// built on it. It checks no permission: the API does, on the paths the
// format names (Folder, RecordPath, LatestPath), before each call. It
// imports the Terraform backend format package, never an ecosystem http
// package or the module's adapters (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the Terraform backend API (workspaces, state versions,
//     locks and uploads), and keys rotate, which re-seals stored states
//     through the composition root (Reseal).
//   - Driven: the item service (Items), whose formats give the Terraform
//     backend's the same Keys; the package index; and the master keyring
//     (platform/crypt.Keyring), which seals a state while the repository's
//     encryptStates is on and opens it again.
//
// # Outputs
//
//   - Workspaces created, read, listed, locked, unlocked and deleted
//     (Workspace, Lock), and state versions written and read (State,
//     StateWrite, LatestVersion), each an item.
//   - ResealReport: what a re-seal changed.
//   - Errors the API maps onto statuses: ErrNoRepository, ErrInvalidName,
//     ErrTaken, ErrWritten, ErrWorkspaceChanged, ErrNoKeyring, LockedError,
//     NotLockedError and InvalidStateError.
//
// # Invariants
//
//   - A state is sealed under the master key while encryptStates is on and
//     opens only with it (decision 0017); the sealed form is stored, so it
//     stays byte-identical.
//   - A state version's milliseconds name it, so a write stores it at the
//     time it names, and the current state's creation time is how a reindex
//     reads them back.
//   - LockedError's text is the one go-tfe reads, so it never changes.
//
// # References
//
//   - Decision 0041: docs/research/decisions/0041-terraform-backend.md.
//   - Research 32: docs/research/32-terraform-backend.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 209 (core/terraformstate), line 1036 (the mapping row) and rule 2
//     at lines 35 to 36.
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals
//     (the Terraform backend, #557 and #558) and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1011
//
// # Status
//
// Implemented P8 to P9c. Staged in P9d.
//
// # Testing
//
//   - workspace_test.go: workspaces, locks, force-unlock and deletes.
//   - state_test.go: state versions, sealing while encryptStates is on, and
//     re-sealing.
//   - fault_test.go: every store failure surfaces; helpers_test.go builds
//     the service over a store.
package terraformstate
