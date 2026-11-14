// Package permission manages permission documents (decision 0006): the
// single internal shape behind the v1 and v2 permission-target APIs and the
// Access v2 permissions API. Each API converts its wire form to the model
// form and calls the service, so validation and storage happen once
// whichever form a client writes.
//
// # Role
//
// access module, core layer: the permission service. The permission target and
// Access permission handlers in access/adapters/http build a Service over the
// configured store and the artifacts module's repositories, and call it; the
// evaluator in core/authz reads the stored documents through the permission
// store, not through this package. The package imports the standard library,
// the kernel, access/api (resource types, targets and actions), core/model and
// core/port, and artifacts/api (Repositories, and repo with its ResolveKey).
//
// # Inputs
//
//   - Service: a Store (the access ports with their unit of work), which the
//     metadata store satisfies; the artifacts module's Repositories over the
//     same unit of work; a clock.
//   - Permission documents in model form, one section of one (PutResource,
//     MergeResource, DeleteResource), and the principals a filtered listing
//     asks about.
//   - For a holder of MANAGE who is not an administrator, its Manager, which
//     CheckManagerEdit judges an edit against.
//
// # Outputs
//
//   - Permissions in the permission store, ordered by name when listed, and
//     those granting to given users or groups (Granting). Page reads either
//     list a page at a time from the store, for the principals PrincipalsOf
//     gathers as Granting does (#1408).
//   - ErrInvalid with the message a handler returns after the sentinel,
//     ErrNotFound, ErrConflict and ErrForbidden; store errors come back
//     wrapped with "permission: ".
//
// # Invariants
//
//   - Each write stores in one transaction of the Store. The repository
//     reads take the context they are given, so a read made inside that
//     transaction joins it.
//   - A target names a special class of repositories, an existing
//     repository that is not virtual, or a remote repository's "-cache"
//     alias (decision 0006).
//   - A holder of MANAGE grants or revokes only what it holds, judged
//     against the stored grants, never the ones it sends.
//
// # References
//
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (modules reach each other through api packages; module stores).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 281 (core/permission) and line 1032 (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1065
//   - https://github.com/weaveplatform/artifactweave/issues/1408 (paging
//     in the store).
//
// # Status
//
// Implemented P4: the permission target and Access permission APIs (pull
// request 104), a MANAGE holder's limits (pull requests 435 and 459) and the
// Manage Resources role and listing filters (pull request 470). Moved here in
// P9d, where Service takes Store and reads repositories through the artifacts
// module's API.
//
// # Testing
//
//   - permission_test.go: TestUnitCreateGetDelete, TestUnitValidate,
//     TestUnitPutAndPutResource, TestUnitWrite, TestUnitCheckManagerEdit,
//     TestUnitMergeAndDeleteResource, TestUnitGrantingAndList,
//     TestUnitValidResourceType, TestUnitDefaultClock, TestUnitNarrow and
//     TestUnitFoldUsers.
//   - username_test.go: TestUnitUserGrantsFold and
//     TestUnitManagerOwnEntryFolds (user grants in any case, #1432).
//   - page_test.go: TestUnitPrincipalsOfAndPage (a walk of Page at every
//     page size visits what Granting collects).
//   - fault_test.go: TestUnitStoreFailures.
//
// The tests run on the metadata store's in-memory store, with a stand-in
// for the artifacts module's repositories (repositories_test.go);
// export_test.go lets a test cross a listing page cheaply.
package permission
