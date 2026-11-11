// Package roles manages global roles and global environments: the Access
// API's /api/v1/roles and /api/v1/environments (issue #77, decision 0013).
// The built-in roles and environments (model.BuiltinGlobalRoles,
// accessapi.BuiltinEnvironments) are not stored; this package merges them with
// the stored custom ones, and it is how the permission evaluator reads a
// global role (Role, then model.RoleActions).
//
// # Role
//
// access module, core layer: the roles service. The roles and environments
// handlers in access/adapters/http build a Service over the configured store
// and the artifacts module's repositories, and call it. The package imports
// the standard library, the kernel, core/model and core/port, and
// artifacts/api for the repository configurations that name an environment.
//
// # Inputs
//
//   - Service: a Store (the access ports with their unit of work), which the
//     metadata store satisfies; the artifacts module's Repositories over the
//     same unit of work; the global environment limit.
//   - Environment names, and roles in model form.
//
// # Outputs
//
//   - Global environments and custom roles, and the stored changes to the
//     predefined roles, in the project store; environment names rewritten in
//     roles, project assignments and repository configurations by a rename.
//   - ErrInvalid, ErrBuiltin, ErrNotFound, ErrConflict and ErrInUse, whose
//     text the Access API returns as it is; store errors come back wrapped
//     with "roles: ".
//
// # Invariants
//
//   - A rename updates roles, project assignments and repository
//     configurations in one transaction: the repository updates take the
//     context of the Store's transaction, so they join it and roll back with
//     it (decision 0013).
//   - DEV and PROD, and the ADMIN role, never change; an environment a
//     repository or a global role still names is not deleted.
//   - Environment names are compared without regard to case, across global
//     and project environments.
//
// # References
//
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (modules reach each other through api packages; module stores).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 282 (core/roles) and line 1035 (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/77
//   - https://github.com/weaveplatform/artifactweave/issues/1065
//
// # Status
//
// Implemented P4: global roles and environments (pull request 93), editable
// built-in roles (pull request 103) and the environment and role limits
// (pull request 470). Moved here in P9d, where Service takes Store and
// rewrites repository configurations through the artifacts module's API.
//
// # Testing
//
//   - roles_test.go: TestUnitEnvironments, TestUnitRoles,
//     TestUnitCustomRoleLimit and TestUnitGlobalEnvironmentLimit.
//   - fault_test.go: TestUnitStoreFailures (each store failure an operation
//     can meet, the repository calls among them) and
//     TestUnitRenameEnvironmentRollsBackRepositoryUpdates (a failed second
//     repository update leaves the environment, the role, the assignment and
//     both repositories as they were).
//
// The tests run on the metadata store's in-memory store, with a stand-in
// for the artifacts module's repositories (repositories_test.go).
package roles
