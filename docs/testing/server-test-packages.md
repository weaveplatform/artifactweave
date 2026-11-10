# Server route test packages

Server-assembly tests drive the production router, with the protocol routes,
authentication, repositories and storage wired together, through requests served
in process. They live in Go packages under `test/server`: one per module and one
per ecosystem, so the test package, not a test-name pattern, is the unit that CI
selects ([#673](https://github.com/weaveplatform/artifactweave/issues/673)). The
router they build is the one `serve` runs, `app.NewRouter` over an
`app.RouterConfig`.

## Building the router

The suites build the router only through `test/server/support/harness`, so a
change to how the router is built edits the harness and no suite.

- `harness.Router(harness.Config{...})` builds the production handler. `Router`
  calls `app.NewRouter`, and `Config` is an alias of `app.RouterConfig`. `Store`
  aliases `app.RouterStore`, the type of `Config.Store`, so a suite's fake can
  embed it and wrap the store it finds there. `Tune` is the function type that
  adjusts a `Config` before the router is built, and `Mirrors`, `MirrorStatus`,
  `SearchLimits` and `DefaultRedirectTTL` alias the configuration types and the
  default that suites name.
- `harness.NewItemWorld` builds the world most route suites start from: the
  router over an in-memory store seeded as a new server seeds it, with the users
  `reader`, `deployer` and `user`, local, remote and virtual repositories, and a
  blob store that signs storage URLs (`StorageSigner`). `Do` sends a request to
  it, `Item` reads a stored item and `WantStatus` checks a response. `RepoFile`
  resolves a fixture path from the repository root.
- `harness.App` opens the application runtime as `serve` does, through
  `app.OpenRuntime`, over `AppConfig` (the default configuration with a temporary
  data directory) and `AppOptions` (a cheap password hash and a small signing
  key), and closes it when the test ends. `harness.NewRemoteWorld` builds the
  router over such a runtime with the generic remote repository `remote` in
  front of a fake upstream; `Request` calls it as one of its users and `Update`
  saves a changed remote. The remote, restart and cache suites use these.

## Support packages

`test/server/support` holds what more than one suite shares and builds no
router: the authentication seed (`Seed` over a `SeedStore`, `Repositories`,
`Layouts`, `TestHash` and `CheapHash`), the request helpers (`Do`, `Basic` and
`Bearer`) and `ArtifactoryError`, which decodes an Artifactory error body. It
also holds package fixtures: NuGet packages and symbols, npm tarballs and
publish bodies, Helm charts, OCI manifests with `PushImage`, Terraform
repositories and keys, and archives. Its route inventories (`NuGetRoutes`,
`NPMRoutes`, `GoRoutes`, `MavenRoutes`, `PyPIRoutes`, `HelmRoutes`,
`DockerRoutes`, `TerraformRoutes` and `TerraformBackendRoutes`) let the guards
in `migration/app` and the format suites check every implemented pattern.

Two packages below it build a world that several suites share.
`test/server/support/registryfixture` assembles the router with the registry's
authentication, repositories and signed blob setup for Docker and Helm OCI, and
`test/server/support/agentcommon` assembles the world of Agent Packages and
Agent Plugins. Like the harness they build the router.

The import rules follow from where the router lives:

- `test/server/support` imports no router package, so the in-package tests of
  `migration/app` can use it.
- The harness, `registryfixture` and `agentcommon` import `migration/app`, so
  neither `test/server/support` nor the in-package tests of `migration/app`
  import them. Those tests keep their own item world (`newItemWorld` in
  `migration/app/fixtures_test.go`), which builds the same world as
  `harness.NewItemWorld`; a change to one is made to the other.
- Production code never imports `test/server/support` or anything below it.

## Package map

| Package | What it tests |
| --- | --- |
| `test/server/access` | The access module: authentication and the public routes, access tokens, users, groups and account policies, permissions in the v1, v2 and Access APIs, global roles and environments, key pairs and certificates, the OIDC token exchange and API restrictions |
| `test/server/configuration` | The configuration module: the base URL behind a chain of proxies and when the operator configures it, the health probes, the service id and the YAML descriptor PATCH |
| `test/server/artifacts` | The artifacts module's management APIs: items with their checksums, properties and sidecars, the storage views and summary, the trash can, copy and move, build-info and promotion, the GET searches and AQL, reindex, repository configuration, remote sources and upstream searches, download redirects, multipart uploads and idempotent build appends |
| `test/server/artifacts/agentpackages` | The Agent Packages routes: the APM protocol and the package lifecycle |
| `test/server/artifacts/agentplugins` | The Agent Plugins routes: the marketplace files that track the repository tree, and their permissions |
| `test/server/artifacts/chef` | The Chef routes: cached remote cookbook metadata and the generic paths that must not bypass it |
| `test/server/artifacts/conan` | The Conan routes: revisions, search, uploads and their refusals, virtual and remote repositories, and the pinned Conan 2 and Conan 1 clients when they are installed |
| `test/server/artifacts/conda` | The Conda routes: the native channel, its indexes and their trust boundary, remote and virtual repositories, and CEP-16 shards |
| `test/server/artifacts/cran` | The CRAN routes: remote and offline indexes, virtual members, POST uploads and the archive migration |
| `test/server/artifacts/debian` | The Debian snapshot routes: their read guards and their refusals of unavailable or corrupt indexes |
| `test/server/artifacts/docker` | The Docker and OCI registry routes: the route and principal matrix, the status table, the registry token flow, listings, manifests, blobs and deletes |
| `test/server/artifacts/gems` | The RubyGems and Bundler routes across local, remote, federated and virtual repositories, and the gem and Bundler clients when they are installed |
| `test/server/artifacts/generic` | The generic remote repository over the application runtime: downloads through the remote cache, sidecars, authorisation and its revocation, restarts and signed delivery |
| `test/server/artifacts/golang` | The Go module proxy routes: the route and principal matrix and the status table |
| `test/server/artifacts/helm` | The classic Helm repository routes: the route and principal matrix and the status table |
| `test/server/artifacts/helmoci` | The requests that `jf helm push` and `jf rt bp` send after Helm pushes a chart to an OCI repository |
| `test/server/artifacts/hex` | The Hex routes: the signed registry of local, virtual and remote repositories, and the routes and classes it refuses |
| `test/server/artifacts/huggingface` | The Hugging Face Xet routes: reconstruction, capabilities, shard authentication and commit provenance, and the pinned client when it is installed |
| `test/server/artifacts/maven` | The Maven routes: the route and principal matrix, the status table, snapshot deploys, metadata rebuilt on read, version resolution and the calculateMetadata and generatePom operations |
| `test/server/artifacts/nix` | The Nix routes over the application runtime: a remote repository's binary cache routes, and the generic and cache routes that must not bypass their validation |
| `test/server/artifacts/npm` | The npm routes: the route and principal matrix, the status tables, login and logout, the auth lines, reindex and the layout conversion |
| `test/server/artifacts/nuget` | The NuGet routes: the route and principal matrix, the status table, API keys, client writes, reindex, the v3 remote and the symbol server |
| `test/server/artifacts/opkg` | The Opkg routes: the signing of a virtual feed |
| `test/server/artifacts/p2` | The P2 routes: remote and virtual repositories, archive suffixes, external composite children and their origins, and the cache's integrity |
| `test/server/artifacts/puppet` | The Puppet routes over the application runtime: a remote module cache through refresh, offline and integrity failures, and a local repository's routes and reindex |
| `test/server/artifacts/pypi` | The PyPI routes: the route and principal matrix, the status table, the JSON routes, uploads, the pip install sequence and reindex |
| `test/server/artifacts/rpm` | The RPM routes: the signing of virtual repository metadata |
| `test/server/artifacts/terraform` | The Terraform registry routes: the route and principal matrix, the status tables, module init, provider install and `jf terraform publish` |
| `test/server/artifacts/terraformbackend` | The Terraform remote state routes: the route and principal matrix, the status table and state read through the item routes |
| `test/server/artifacts/vcs` | The VCS routes: the REST contract and GitLab subgroups, the STASH, OLDSTASH and Artifactory peer providers, file delivery, and `curl` when it is present |
| `test/server/support` | Shared seeds, request helpers, fixtures and route inventories; no tests |
| `test/server/support/harness` | The router, item world, runtime and remote world builders; no tests |
| `test/server/support/registryfixture` | The registry world of Docker and Helm OCI; no tests |
| `test/server/support/agentcommon` | The world of Agent Packages and Agent Plugins; no tests |

Two kinds of server test live outside `test/server`:

- The whole-router guards live in `migration/app`, beside the router: the status
  tables (`status_test.go`, with format rows in files such as
  `status_chef_test.go`), the route and principal matrices (`matrix_test.go`,
  `matrix_pkgtest_test.go` and `matrix_reindex_test.go`), the specification
  checks (`spec_test.go` and `spec_contract_test.go`), the stub check
  (`stub_test.go`), the error profiles (`error_profiles_p8_test.go`) and the API
  reliability ledger (`api_ledger_test.go`), with the tests that need unexported
  app code, such as `reindex_internal_test.go`. They import `test/server/support`
  and never the harness.
- Handler tests sit beside their code, in `<module>/adapters/http` (for example
  `migration/access/adapters/http`) and in
  `migration/artifacts/ecosystem/<type>/http`.

## Running them

`go test ./test/server/...` runs every suite. Each package also runs on its own:

```sh
go test ./test/server/access
go test ./test/server/configuration
go test ./test/server/artifacts
go test ./test/server/artifacts/agentpackages
go test ./test/server/artifacts/agentplugins
go test ./test/server/artifacts/chef
go test ./test/server/artifacts/conan
go test ./test/server/artifacts/conda
go test ./test/server/artifacts/cran
go test ./test/server/artifacts/debian
go test ./test/server/artifacts/docker
go test ./test/server/artifacts/gems
go test ./test/server/artifacts/generic
go test ./test/server/artifacts/golang
go test ./test/server/artifacts/helm
go test ./test/server/artifacts/helmoci
go test ./test/server/artifacts/hex
go test ./test/server/artifacts/huggingface
go test ./test/server/artifacts/maven
go test ./test/server/artifacts/nix
go test ./test/server/artifacts/npm
go test ./test/server/artifacts/nuget
go test ./test/server/artifacts/opkg
go test ./test/server/artifacts/p2
go test ./test/server/artifacts/puppet
go test ./test/server/artifacts/pypi
go test ./test/server/artifacts/rpm
go test ./test/server/artifacts/terraform
go test ./test/server/artifacts/terraformbackend
go test ./test/server/artifacts/vcs
go test ./test/server/support
go test ./test/server/support/agentcommon
go test ./test/server/support/harness
go test ./test/server/support/registryfixture
```

The four support packages hold no tests, so `go test` reports none for them.
The guards run with `go test ./migration/app`. CI's full profile runs every
package's `TestUnit` and `TestAcc` entry points under the coverage gate, and a
narrow profile runs the packages that the
[server test ownership](server-test-ownership.md) inventory gives one format;
the [CI guide](ci.md) describes both.

## Adding or moving a suite

1. Keep the suite's assertions and its `Test` and `Fuzz` names; compare the
   sets of names before and after the change. `make test-names` checks that every
   entry point still declares its kind (`TestUnit` or `TestAcc`).
2. Give the package a `doc.go` and a `fixture_test.go` of thin adapters that keep
   the helper names its tests call (`statusFixture`, `newItemWorld`, `do`,
   `basic` and the like) over the harness and `test/server/support`. An adapter
   delegates; it never keeps another copy of a fixture.
3. Add the package's ownership entry to
   `tools/citestselection/test_ownership.json`, as the
   [server test ownership](server-test-ownership.md) guide describes, and run the
   inventory check, `go test ./tools/citestselection`, which fails until the
   inventory and the tree agree.
4. Name the package in this guide's package map and run list.
