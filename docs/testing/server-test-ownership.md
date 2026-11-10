# Server test ownership

`tools/citestselection/test_ownership.json` records who owns each directory of
server tests. The CI test selector
([#673](https://github.com/weaveplatform/artifactweave/issues/673)) embeds it to
decide whether a change to one format can run a narrow profile, and which tests
that profile runs; the [CI guide](ci.md) describes the profiles. Ownership is
drawn by Go package, and file by file in the shared directories that list their
files; within those boundaries the CI lanes pick tests by their `TestUnit` and
`TestAcc` names. The [server test package guide](server-test-packages.md)
describes the suites themselves.

## The inventory

The file is version 2 and keys everything by directory, so a test that moves is
recorded by editing paths, never by a new field. The selector rejects any other
key, and anything after the object.

- `packages` maps every directory that holds server tests to its owner: a
  format, or `shared`.
- `shared_files` lists, for a shared directory whose files are reviewed one by
  one, the bare name of every test file in it. A test file in such a directory
  that the list leaves out has no owner. The keys today are `migration/app`; the
  HTTP adapters `migration/access/adapters/http`,
  `migration/artifacts/adapters/http`,
  `migration/artifacts/adapters/http/itemkit` and
  `migration/configuration/adapters/http`; and the module suites
  `test/server/access`, `test/server/artifacts` and `test/server/configuration`.
- `format_dependencies` maps a format to the repository paths of the shared
  test files that exercise it. Each path must name a file that its directory's
  `shared_files` list holds.

`go test ./tools/citestselection` checks the inventory against the tree: every
test file in an owned test directory needs an owner, every `packages` key must
hold tests, and every listed shared file and dependency path must exist. It
fails until the inventory and the tree agree.

## Owned test directories

`ownership.OwnedTestDir`, in `tools/citestselection/ownership`, decides which
`_test.go` files are server tests that need a reviewed owner: those in one of
these directories, where `<root>` is `migration` or `internal`. P9d stages the
target tree under `migration/` and renames it to `internal/` at promotion, so
both roots are accepted until then.

- `test/server` and everything below it;
- `<root>/app`;
- `<root>/<module>/adapters/http` and everything below it;
- `<root>/artifacts/ecosystem/<type>/http` and everything below it.

Every `packages` key must be one of these directories, written in clean form. A
test in a format's directory (`test/server/artifacts/<type>` or
`<root>/artifacts/ecosystem/<type>/http`) counts as that format's code on a
narrow PR only when its package owner is that format; any other owner, `shared`
included, selects full CI.

## Package boundaries

The owners and their test packages, as `packages` records them:

| Owner | Test packages |
| --- | --- |
| `agentpackages` | `test/server/artifacts/agentpackages` |
| `agentplugins` | `migration/artifacts/ecosystem/agentplugins/http`, `test/server/artifacts/agentplugins` |
| `aieditorextensions` | `migration/artifacts/ecosystem/aieditorextensions/http` |
| `alpine` | `migration/artifacts/ecosystem/alpine/http` |
| `ansible` | `migration/artifacts/ecosystem/ansible/http` |
| `bazelmodules` | `migration/artifacts/ecosystem/bazelmodules/http` |
| `bower` | `migration/artifacts/ecosystem/bower/http` |
| `cargo` | `migration/artifacts/ecosystem/cargo/http` |
| `chef` | `migration/artifacts/ecosystem/chef/http`, `test/server/artifacts/chef` |
| `cocoapods` | `migration/artifacts/ecosystem/cocoapods/http` |
| `composer` | `migration/artifacts/ecosystem/composer/http` |
| `conan` | `migration/artifacts/ecosystem/conan/http`, `test/server/artifacts/conan` |
| `conda` | `migration/artifacts/ecosystem/conda/http`, `test/server/artifacts/conda` |
| `cran` | `migration/artifacts/ecosystem/cran/http`, `test/server/artifacts/cran` |
| `debian` | `migration/artifacts/ecosystem/debian/http`, `test/server/artifacts/debian` |
| `docker` | `migration/artifacts/ecosystem/docker/http`, `test/server/artifacts/docker` |
| `gems` | `migration/artifacts/ecosystem/gems/http`, `test/server/artifacts/gems` |
| `gitlfs` | `migration/artifacts/ecosystem/gitlfs/http` |
| `golang` | `migration/artifacts/ecosystem/golang/http`, `test/server/artifacts/golang` |
| `helm` | `migration/artifacts/ecosystem/helm/http`, `test/server/artifacts/helm` |
| `helmoci` | `test/server/artifacts/helmoci` |
| `hex` | `migration/artifacts/ecosystem/hex/http`, `test/server/artifacts/hex` |
| `huggingface` | `migration/artifacts/ecosystem/huggingface/http`, `test/server/artifacts/huggingface` |
| `jetbrainsplugins` | `migration/artifacts/ecosystem/jetbrainsplugins/http` |
| `luarocks` | `migration/artifacts/ecosystem/luarocks/http` |
| `machinelearning` | `migration/artifacts/ecosystem/machinelearning/http` |
| `maven` | `migration/artifacts/ecosystem/maven/http`, `test/server/artifacts/maven` |
| `nimmodel` | `migration/artifacts/ecosystem/nimmodel/http` |
| `nix` | `migration/artifacts/ecosystem/nix/http`, `test/server/artifacts/nix` |
| `npm` | `migration/artifacts/ecosystem/npm/http`, `test/server/artifacts/npm` |
| `nuget` | `migration/artifacts/ecosystem/nuget/http`, `test/server/artifacts/nuget` |
| `opkg` | `migration/artifacts/ecosystem/opkg/http`, `test/server/artifacts/opkg` |
| `p2` | `migration/artifacts/ecosystem/p2/http`, `test/server/artifacts/p2` |
| `pub` | `migration/artifacts/ecosystem/pub/http` |
| `puppet` | `migration/artifacts/ecosystem/puppet/http`, `test/server/artifacts/puppet` |
| `pypi` | `migration/artifacts/ecosystem/pypi/http`, `test/server/artifacts/pypi` |
| `rpm` | `migration/artifacts/ecosystem/rpm/http`, `test/server/artifacts/rpm` |
| `skills` | `migration/artifacts/ecosystem/skills/http` |
| `swift` | `migration/artifacts/ecosystem/swift/http` |
| `terraform` | `migration/artifacts/ecosystem/terraform/http`, `test/server/artifacts/terraform` |
| `terraformbackend` | `migration/artifacts/ecosystem/terraformbackend/http`, `test/server/artifacts/terraformbackend` |
| `vagrant` | `migration/artifacts/ecosystem/vagrant/http` |
| `vcs` | `migration/artifacts/ecosystem/vcs/http`, `test/server/artifacts/vcs` |
| `shared` | `migration/access/adapters/http`, `migration/app`, `migration/artifacts/adapters/http`, `migration/artifacts/adapters/http/itemkit`, `migration/artifacts/adapters/http/pkgroute`, `migration/configuration/adapters/http`, `test/server/access`, `test/server/artifacts`, `test/server/artifacts/generic`, `test/server/configuration` |

A narrow PR for a format runs the owner's packages, the packages of the format's
code that the selector's format table names, and the shared tests that
`format_dependencies` lists for it. Docker's also runs
`test/server/artifacts/helmoci` and `migration/artifacts/ecosystem/nimmodel/http`,
which exercise the same registry protocol stack. No format owns the generic
remote repository, so `test/server/artifacts/generic` is shared and runs in the
full profile only.

`test/server/support` and the packages below it (`harness`, `agentcommon` and
`registryfixture`) hold fixtures and helpers, not tests, so the inventory has no
key for them. A change to one of them selects full CI, as does a change to
shared server code, authentication, routing, stores or registrations.

## Shared guard dependencies

Some tests in shared directories exercise one format's code: whole-router guards
in `migration/app` that hold a format's rows or call its rules, and module suites
that use a format's fixtures. `format_dependencies` lists them by repository
path, and in each shared directory a narrow PR runs only the `TestUnit` and
`TestAcc` entry points of the files listed for its format:

| Format | Listed shared tests | What they cover |
| --- | --- | --- |
| `agentpackages`, `agentplugins`, `hex`, `luarocks`, `nimmodel` | `test/server/artifacts/repositories_format_class_test.go` | The repository classes each of these formats accepts |
| `cargo` | `migration/app/spec_test.go` | The specification checks, among them that every Cargo operation the package router owns is served |
| `chef` | `migration/app/status_chef_test.go` | Chef's rows of the status table and route matrix |
| `composer` | `migration/app/server_test.go` | The whole-router checks, among them that the native Composer routes avoid the handwritten stubs |
| `cran` | `migration/app/status_cran_archive_test.go` | The CRAN archive rows of the status table and route matrix |
| `docker` | `migration/app/registry_shared_test.go` | The registry's refusal of a request without a permission decider, and the content type that the shared middleware keeps |
| `helm` | `migration/app/reindex_internal_test.go`, `test/server/artifacts/reindex_test.go`, `test/server/artifacts/sidecar_rights_test.go` | Reindex and its refusals, and the rights a copy of a chart's sidecar needs |
| `huggingface` | `migration/app/status_huggingface_xet_test.go` | The statuses of the Xet routes |
| `machinelearning` | `migration/app/status_machinelearning_test.go` | The machine learning rows of the status table and route matrix |
| `maven` | `test/server/artifacts/searches_upstream_test.go`, `test/server/artifacts/searches_upstream_mapped_test.go` | Upstream search and cache mapping with Maven coordinates |
| `nix` | `migration/app/status_nix_test.go` | Nix's rows of the status table and route matrix |
| `npm` | `migration/app/error_profiles_p8_test.go` | Error envelope selection, whose rules call npm's path predicates |
| `nuget` | `test/server/artifacts/repositories_test.go` | Repository configuration, with NuGet contexts |
| `puppet` | `migration/app/status_puppet_test.go` | Puppet's rows of the status table and route matrix |
| `pypi` | `migration/app/reindex_internal_test.go`, `test/server/artifacts/reindex_test.go` | Reindex and its refusals |
| `terraform` | `test/server/access/keypairs_test.go`, `test/server/access/permissions_test.go`, `test/server/artifacts/storage_list_test.go` | The key pair, permission and storage list APIs with Terraform fixtures |
| `vcs` | `migration/app/vcs_tables_test.go` | The VCS REST rows of the status table and route matrix |

Six of these files hold only a format's rows: `status_chef_test.go`,
`status_cran_archive_test.go`, `status_machinelearning_test.go`,
`status_nix_test.go`, `status_puppet_test.go` and `vcs_tables_test.go`. The
status table in `status_test.go` and the route matrix in `matrix_test.go` run
those rows, and the six files declare no entry point of their own, so a narrow
PR runs none of them; they run in the full profile.

A change to a test file in a shared directory selects full CI, and so does a
new, removed or moved server test, or a test file that no `packages` key or
`shared_files` list covers, until its ownership has been reviewed and recorded
in the inventory.

## Moving a root test

When a test file leaves a shared directory such as `migration/app` on its own:

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
