# Server test ownership after Phase 9b extraction

`tools/ciimpact/test_ownership.json` is the machine-checked package inventory
for the selective CI planner in #673. It lists every Go package with server
tests and every test file still directly in `internal/server`. The
`tools/ciimpact` inventory test compares that list with the working tree. A new
server test file, a moved test, or an unmapped test package selects full CI
until its ownership has been reviewed. Format ownership uses Go package and
reviewed mixed-package file boundaries; the CI lanes use `TestUnit` and
`TestAcc` names within those boundaries.

## Final package boundaries

| Owner | Independently runnable server test packages |
| --- | --- |
| Agent Packages | `test/server/agentpackages` |
| Agent Plugins | `test/server/agentplugins` |
| Docker/OCI | `internal/server/docker`, `test/server/docker` |
| Go modules | `internal/server/golang`, `test/server/golang` |
| Helm | `internal/server/helm`, `test/server/helm` |
| Helm OCI | `test/server/helmoci` |
| LuaRocks | `internal/server/luarocks` |
| Maven | `test/server/maven` |
| NIM model | `internal/server/nimmodel` |
| npm | `internal/server/npm`, `test/server/npm` |
| NuGet | `internal/server/nuget`, `test/server/nuget` |
| PyPI | `internal/server/pypi`, `test/server/pypi` |
| Skills | `internal/server/skills` |
| Terraform registry | `internal/server/terraform`, `test/server/terraform` |
| Terraform backend | `internal/server/terraformbackend`, `test/server/terraformbackend` |
| Shared | `internal/server`, `internal/server/apierror`, `internal/server/baseurl`, `internal/server/pkgroute`, `test/server/shared` |

`test/server/agentcommon`, `test/server/registryfixture`, and
`test/server/support` contain fixtures and helpers, not test suites. A change
to one of them must select its dependent test packages. A change to shared
server code, authentication, routing, store contracts, or registrations must
select every affected owner. #673 implements those reverse dependencies and
CI profiles; this inventory supplies the package boundary.

## Residual root test audit

The 90 direct root test files cover shared server APIs and registration:
authentication, access tokens, roles, permissions, repositories, generic item
delivery, cache and source handling, search, AQL, builds, storage, trash,
transfer, OpenAPI, status and principal matrices. The file names are explicit
in the inventory. A new direct root test cannot be treated as format-owned
without review.

Some shared API tests use a format-specific fixture. The inventory's
`root_format_dependencies` records the format-to-root link so #673 can include
root tests when that format changes:

| Root tests | Shared behavior exercised |
| --- | --- |
| `keypairs_test.go`, `permissions_test.go`, `storage_list_test.go` | Keypair, permission and storage APIs using Terraform fixtures |
| `searches_upstream_test.go`, `searches_upstream_mapped_test.go` | Generic upstream search and cache mapping using Maven coordinates |
| `reindex_test.go` | Generic reindex orchestration using PyPI and Helm cases |
| `repositories_test.go` | Repository settings using NuGet contexts |
| `repositories_format_class_test.go` | Repository class validation across LuaRocks, NIM and Agent formats |
| `npm_auth_paths_test.go` | Private middleware path recognition and log redaction for npm and LuaRocks |
| `registry_shared_test.go`, `sidecar_rights_test.go` | Shared registry content type and sidecar permission rules using Docker and Helm |
| `encrypt_states_v1_test.go` | Generic v1 repository read using Terraform backend settings |

These tests use shared APIs or private server helpers. A format-only change
that could affect one of them must include the root package; #673 may widen
that selection further based on Go reverse dependencies. Unknown ownership
selects full qualification.

## Application acceptance dependencies

Some native format tests live in the mixed `internal/app` package because they
exercise application startup, remote cache and source policy through a running
server. The inventory's `app_format_dependencies` maps their files to format
owners. A narrow format PR selects only the mapped app files and runs their
`TestAcc` entries in the Go acceptance lane. The current map covers Docker's
registry remote tests, Maven remote tests and NuGet reindex tests. New format
app tests must be added to the map after ownership review; an app test change
itself selects full CI until then.

## Comparison and local timings

Immediately after #793, `go test -list . ./internal/server` listed 374 root
tests. After the Terraform registry, Terraform backend, Agent Packages and
Agent Plugins moves, it lists 367. The seven removed names all appear in
their corresponding new packages: `TestAccJFTerraformPublish`,
`TestAccTerraformModuleInit`, `TestAccTerraformProviderInstall`,
`TestAccTerraformTypeOnTheRepositoriesAPI`,
`TestAccTerraformBackendThroughTheItemRoutes`,
`TestAccAgentPackagesAPMProtocolAndLifecycle`, and
`TestAccAgentPluginMarketplacesTrackTreeAndPermissions`. The status and principal
matrix route rows moved into format packages while their shared root guards
remain. Each package's full `go test` run passed independently with the root
package during its PR. The new format package runs took roughly 0.5–4 seconds
locally; the root package took 33–43 seconds in those paired runs. These are
local measurements, not CI runner budgets. #673 owns the measured selective
CI rollout, coverage policy, and full release gate.
