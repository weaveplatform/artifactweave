# Server test ownership after Phase 9b extraction

`tools/citestselection/test_ownership.json` is the machine-checked package inventory
for the selective CI planner in #673. Version 2 keys everything by directory, so
tests can leave `internal/server` and `internal/app` over many changes without a
new schema:

- `packages` maps every directory with server tests to its owner: a format, or
  `shared`.
- `shared_files` lists, by file name, every test file in a shared directory whose
  files are reviewed one by one. Today these are `internal/server` and
  `internal/app`.
- `format_dependencies` maps a format to the repository paths of the shared test
  files that exercise it.

The `tools/citestselection` inventory test compares the inventory with the working
tree. A new server test file, a moved test, or an unmapped test package selects full CI
until its ownership has been reviewed. Format ownership uses Go package and
reviewed shared-directory file boundaries; the CI lanes use `TestUnit` and
`TestAcc` names within those boundaries.

## Owned test directories

The selector treats a `_test.go` file as a server test, which needs a reviewed
owner, when it lies in one of these directories. `<root>` is `migration` or
`internal`, because staged code under `migration/` becomes `internal/` at the end
of P9d:

- `internal/server` and everything below it;
- `test/server` and everything below it, including `test/server/artifacts/<type>`;
- `<root>/app`;
- `<root>/artifacts/ecosystem/<type>/http` and everything below it;
- `<root>/<module>/adapters/http` and everything below it.

Every `packages` key must be one of these directories. A test in a format's
directory (`internal/server/<type>`, `test/server/artifacts/<type>` or
`<root>/artifacts/ecosystem/<type>/http`) counts as that format's code on a
narrow PR only when its package owner is that format; any other owner, `shared`
included, selects full CI.

## Moving a root test

When a test file leaves a shared directory such as `internal/server`:

1. Remove its name from that directory's `shared_files` list. When the list is
   empty and no tests remain there, remove the key and its `packages` entry.
2. Add its new directory to `packages` with the reviewed owner. If the new
   directory is shared and its files are reviewed one by one, list the file under
   its `shared_files` key.
3. Update the `format_dependencies` paths that named the file: drop a path when
   the file now sits in a package its format owns, otherwise point it at the new
   path. A dependency path must lie in a `shared_files` directory that lists its
   file name.

`go test ./tools/citestselection` fails until the inventory and the tree agree.

## Final package boundaries

| Owner | Independently runnable server test packages |
| --- | --- |
| Agent Packages | `test/server/artifacts/agentpackages` |
| Agent Plugins | `test/server/artifacts/agentplugins` |
| Docker/OCI | `migration/artifacts/ecosystem/docker/http`, `test/server/artifacts/docker` |
| Go modules | `migration/artifacts/ecosystem/golang/http`, `test/server/artifacts/golang` |
| Helm | `migration/artifacts/ecosystem/helm/http`, `test/server/artifacts/helm` |
| Helm OCI | `test/server/artifacts/helmoci` |
| LuaRocks | `internal/server/luarocks` |
| Maven | `test/server/artifacts/maven` |
| NIM model | `migration/artifacts/ecosystem/nimmodel/http` |
| npm | `migration/artifacts/ecosystem/npm/http`, `test/server/artifacts/npm` |
| NuGet | `migration/artifacts/ecosystem/nuget/http`, `test/server/artifacts/nuget` |
| PyPI | `migration/artifacts/ecosystem/pypi/http`, `test/server/artifacts/pypi` |
| Dart pub | `internal/server/pub` |
| RubyGems | `internal/server/gems`, `test/server/artifacts/gems` |
| Skills | `internal/server/skills` |
| Terraform registry | `migration/artifacts/ecosystem/terraform/http`, `test/server/artifacts/terraform` |
| Terraform backend | `internal/server/terraformbackend`, `test/server/artifacts/terraformbackend` |
| Shared | `internal/server`, `internal/server/pkgroute`, `test/server/access` |

`test/server/support` and the packages under it (`harness`, `agentcommon` and
`registryfixture`) contain fixtures and helpers, not test suites. A change
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
`format_dependencies` records the format-to-root link as `internal/server/<file>`
paths so #673 can include root tests when that format changes:

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
server. The inventory lists every `internal/app` test file under `shared_files`,
and `format_dependencies` maps the format-specific ones to format owners as
`internal/app/<file>` paths. A narrow format PR selects only the mapped app files
and runs their `TestAcc` entries in the Go acceptance lane. The current map covers
the Conda, Docker registry, Maven, Nix and Puppet remote tests and the NuGet
reindex tests. New format app tests must be added to the map after ownership
review; an app test change itself selects full CI until then.

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
