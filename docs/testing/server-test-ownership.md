# Server test ownership after Phase 9b extraction

`tools/citestselection/test_ownership.json` is the machine-checked package inventory
for the selective CI planner in #673. Version 2 keys everything by directory, so
tests can leave `internal/server` and `migration/app` over many changes without a
new schema:

- `packages` maps every directory with server tests to its owner: a format, or
  `shared`.
- `shared_files` lists, by file name, every test file in a shared directory whose
  files are reviewed one by one. Today these are `internal/server`,
  `migration/app` and the module suites `test/server/access`,
  `test/server/artifacts` and `test/server/configuration`.
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
| Chef | `migration/artifacts/ecosystem/chef/http`, `test/server/artifacts/chef` |
| Conan | `migration/artifacts/ecosystem/conan/http`, `test/server/artifacts/conan` |
| Conda | `migration/artifacts/ecosystem/conda/http`, `test/server/artifacts/conda` |
| CRAN | `migration/artifacts/ecosystem/cran/http`, `test/server/artifacts/cran` |
| Debian | `migration/artifacts/ecosystem/debian/http`, `test/server/artifacts/debian` |
| Docker/OCI | `migration/artifacts/ecosystem/docker/http`, `test/server/artifacts/docker` |
| Go modules | `migration/artifacts/ecosystem/golang/http`, `test/server/artifacts/golang` |
| Helm | `migration/artifacts/ecosystem/helm/http`, `test/server/artifacts/helm` |
| Helm OCI | `test/server/artifacts/helmoci` |
| Hex | `migration/artifacts/ecosystem/hex/http`, `test/server/artifacts/hex` |
| Hugging Face | `migration/artifacts/ecosystem/huggingface/http`, `test/server/artifacts/huggingface` |
| LuaRocks | `migration/artifacts/ecosystem/luarocks/http` |
| Maven | `test/server/artifacts/maven` |
| NIM model | `migration/artifacts/ecosystem/nimmodel/http` |
| Nix | `migration/artifacts/ecosystem/nix/http`, `test/server/artifacts/nix` |
| npm | `migration/artifacts/ecosystem/npm/http`, `test/server/artifacts/npm` |
| NuGet | `migration/artifacts/ecosystem/nuget/http`, `test/server/artifacts/nuget` |
| Opkg | `migration/artifacts/ecosystem/opkg/http`, `test/server/artifacts/opkg` |
| P2 | `migration/artifacts/ecosystem/p2/http`, `test/server/artifacts/p2` |
| Puppet | `migration/artifacts/ecosystem/puppet/http`, `test/server/artifacts/puppet` |
| PyPI | `migration/artifacts/ecosystem/pypi/http`, `test/server/artifacts/pypi` |
| RPM | `migration/artifacts/ecosystem/rpm/http`, `test/server/artifacts/rpm` |
| Dart pub | `migration/artifacts/ecosystem/pub/http` |
| RubyGems | `migration/artifacts/ecosystem/gems/http`, `test/server/artifacts/gems` |
| Skills | `migration/artifacts/ecosystem/skills/http` |
| Terraform registry | `migration/artifacts/ecosystem/terraform/http`, `test/server/artifacts/terraform` |
| Terraform backend | `migration/artifacts/ecosystem/terraformbackend/http`, `test/server/artifacts/terraformbackend` |
| VCS | `migration/artifacts/ecosystem/vcs/http`, `test/server/artifacts/vcs` |
| Shared | `internal/server`, `migration/artifacts/adapters/http/pkgroute`, `test/server/access`, `test/server/artifacts`, `test/server/artifacts/generic`, `test/server/configuration` |

`test/server/support` and the packages under it (`harness`, `agentcommon` and
`registryfixture`) contain fixtures and helpers, not test suites. A change
to one of them must select its dependent test packages. A change to shared
server code, authentication, routing, store contracts, or registrations must
select every affected owner. #673 implements those reverse dependencies and
CI profiles; this inventory supplies the package boundary.

## Residual root test audit

The 75 direct root test files cover shared server APIs and registration:
authentication, the in-package parts of the access, artifacts management and
ecosystem suites (`<name>_internal_test.go`), AQL relations and transitive
searches, request limits, the format tests that move with their code,
OpenAPI, status and principal matrices. The file names are explicit in the inventory.
A new direct root test cannot be treated as format-owned without review.

Some shared API tests use a format-specific fixture. The inventory's
`format_dependencies` records the link from a format to such a test as the
test's repository path (`internal/server/<file>`, or `test/server/access/<file>`
and `test/server/artifacts/<file>` for the module suites') so #673 can include
those tests when that format changes:

| Shared tests | Shared behavior exercised |
| --- | --- |
| `test/server/access`: `keypairs_test.go`, `permissions_test.go`; `test/server/artifacts`: `storage_list_test.go` | Keypair, permission and storage APIs using Terraform fixtures |
| `test/server/artifacts`: `searches_upstream_test.go`, `searches_upstream_mapped_test.go` | Generic upstream search and cache mapping using Maven coordinates |
| `test/server/artifacts`: `reindex_test.go`; root: `reindex_internal_test.go` | Generic reindex orchestration using PyPI and Helm cases |
| `test/server/artifacts`: `repositories_test.go` | Repository settings using NuGet contexts |
| `test/server/artifacts`: `repositories_format_class_test.go` | Repository class validation across LuaRocks, NIM and Agent formats |
| root: `error_profiles_p8_test.go` | Error envelope selection, whose rules call npm's path predicates for its JFrog operations |
| root: `registry_shared_test.go`; `test/server/artifacts`: `sidecar_rights_test.go` | Shared registry content type and sidecar permission rules using Docker and Helm |

These tests use shared APIs or private server helpers. A format-only change
that could affect one of them must include the package that holds it, the root
package or a module suite; #673 may widen that selection further based on Go
reverse dependencies. Unknown ownership selects full qualification.

## Application acceptance dependencies

The format tests that exercise application startup, the remote cache and the
source policy through a running server no longer live in the mixed `migration/app`
package. They run from their ecosystems' packages under
`test/server/artifacts/<type>`, which open the application runtime through
`harness.App` and `harness.RemoteWorld`, so no `migration/app` file maps to a
format any more: the Conan, Conda, Docker registry, Maven (with the mapped remote
tests), Nix and Puppet remote suites and the NuGet reindex suite run with the rest
of their owner's package, the Conan, Maven mapping and registry fault suites on
narrow Conan, Maven and Docker PRs too. The generic remote suite in
`test/server/artifacts/generic` is shared, since `generic` is no selector owner,
and runs in the full profile only. The inventory still lists every `migration/app`
test file under `shared_files`, so a change to one selects full CI.

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
