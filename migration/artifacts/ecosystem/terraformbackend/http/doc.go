// Package terraformbackendhttp serves the TFE-shaped API used by Terraform's
// remote backend (decision 0041), under /artifactory/api/terraform/remote/v2:
// the account, the organisation (a terraformbackend repository) and its
// entitlements, workspaces with their locks, and state versions with their
// uploads and downloads. Repository items remain its source of truth.
//
// # Role
//
// artifacts module, ecosystem layer: the TFE-shaped state backend API of the
// Terraform backend slice. It is named terraformbackendhttp so that it does
// not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Register with explicit chi routes,
//     since a backend workspace ID resolves against both the repository and
//     the workspace, and asks IsPath and SkipsAuthentication which backend
//     requests its authentication middleware lets through. The Terraform
//     discovery document advertises Prefix.
//   - Driven: the state service (artifacts/core/terraformstate), which
//     Register builds from Options.Items, Keys and Now, and which resolves,
//     reads and writes repositories, workspaces, locks and states; the
//     format package (artifacts/ecosystem/terraformbackend) for the paths
//     and identifiers; the request's principal and decider (kernel,
//     access/api), which authenticate and authorise every route but the
//     probe and the capability upload; the access contracts in Options
//     (access/api), which re-evaluate the user a state upload capability
//     names when it is used; the keyring (Options.Keys), from whose master
//     key the capabilities are signed; and Options.Fault, which records the
//     cause of an unexpected error.
//
// # Outputs
//
//   - Register, Options, Prefix, IsPath and SkipsAuthentication.
//   - Answers: JSON:API documents (application/vnd.api+json, never cached)
//     for the account, the organisation and its entitlement set, the
//     workspaces (list, create, read, delete, lock, unlock and
//     force-unlock), and the state versions (current, list, read, create
//     and download), with paged lists; and, for a state version created
//     without its state, a signed, time-limited upload URL.
//   - Errors as JSON:API error objects in the Terraform backend profile:
//     401 and 403 as every API answers them, 404 for what is missing and for
//     any other route under Prefix, 409 for a lock held or not held and for
//     a state already written, 413 above the body limit, 422 for a name
//     taken or refused and for a state that does not match its attributes,
//     and 403 for an upload URL that does not verify or whose workspace
//     changed.
//
// # Invariants
//
//   - Only the ping probe and the upload of a state version's content skip
//     authentication, and the full route is checked, so a query token never
//     bypasses another API.
//   - An upload capability keeps the issuing token's scope, identity and
//     expiry; when it is used, the user it names is read again and its
//     restriction and permissions are re-evaluated, and a workspace deleted
//     and created again refuses it.
//   - Every write goes through the state service, which marks it for the
//     format's hooks, so the item routes cannot pass the lock or the seal.
//   - Statuses and messages stay byte-identical.
//
// # References
//
//   - Decision 0041: docs/research/decisions/0041-terraform-backend.md
//     ("Discovery", "Identifiers", "Routes", "Documents", "Errors",
//     "Locking", "Permissions", "Implementation notes (#558)"), and research
//     32: docs/research/32-terraform-backend.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md;
//     decisions 0005 and 0006 (authentication and authorisation); decision
//     0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the master key the capability key derives from); decision 0043:
//     docs/research/decisions/0043-api-reliability.md (the operation ledger
//     that names these handlers).
//   - HashiCorp's TFE API documentation, as decision 0041 cites it:
//     hashicorp/web-unified-docs api-docs/workspaces.mdx and
//     api-docs/state-versions.mdx; hashicorp/go-tfe v1.110.0.
//   - JFrog Terraform backend repositories, as research 32 cites it:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/terraformbackend) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/558 (the API),
//     https://github.com/weaveplatform/artifactweave/issues/559 (native
//     acceptance) and https://github.com/weaveplatform/artifactweave/issues/1035
//
// # Status
//
// Implemented P8. Staged in P9d.
//
// # Testing
//
//   - terraformbackend_test.go: the remote backend's sequences that
//     research 32 records, workspace lists and identifiers, refusals, the
//     upload capability and its identity, scope and expiry, concurrent locks
//     and uploads, body limits and disconnects, and route boundaries.
package terraformbackendhttp
