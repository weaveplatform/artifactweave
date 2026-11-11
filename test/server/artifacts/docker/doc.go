// Package docker runs the Docker and OCI registry routes of the assembled
// server: the route and principal matrix, the status table, the registry
// token flow, listings, manifests, blobs and deletes.
//
// # Role
//
// Server-assembly suite, outside the modules: it drives the production router
// through HTTP requests. It is a package of its own so that CI selection can
// run it alone; its owner in tools/citestselection/test_ownership.json is
// docker.
//
// # Inputs
//
//   - The world of test/server/support/registryfixture and the registry
//     fixtures of test/server/support (PushImage, RegistryManifest,
//     RegistryConfig and RegistryLayer).
//   - support.DockerRoutes, the route inventory.
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission,
//     beside which the tests retype the remote or add the repositories they
//     need.
//
// # Outputs
//
//   - TestAccDockerRouteTablesCoverImplemented fails while a route of
//     support.DockerRoutes has no status row or no principal row.
//   - TestAcc results for the token flow, OIDC users, probe headers,
//     listings, manifest redirects, blob downloads, subdomain and
//     reverse-proxy routing, and multi-architecture deletes.
//   - TestAcc results for registry remotes (registry_remote_test.go and
//     registry_remote_fault_test.go): pulls through refresh and offline
//     answers, indexes and referrers, no-store and native routes, invalid
//     index children, integrity and denied paths, foreign layers and a
//     revocation during a fetch.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md.
//   - Research 23: docs/research/23-docker-oci-registry.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md;
//     docs/operations/registry-remote.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P6; P9b moved the tests here from the server root (issue 673),
// and P9d the registry remote suites from internal/app (TST-07, issue 1096).
package docker
