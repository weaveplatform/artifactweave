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
//   - The world of test/server/registryfixture and the registry fixtures of
//     test/server/support (PushImage, RegistryManifest, RegistryConfig and
//     RegistryLayer).
//   - support.DockerRoutes, the route inventory.
//
// # Outputs
//
//   - TestAccDockerRouteTablesCoverImplemented fails while a route of
//     support.DockerRoutes has no status row or no principal row.
//   - TestAcc results for the token flow, OIDC users, probe headers,
//     listings, manifest redirects, blob downloads, subdomain and
//     reverse-proxy routing, and multi-architecture deletes.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md.
//   - Research 23: docs/research/23-docker-oci-registry.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//
// # Status
//
// Implemented P6; P9b moved the tests here from internal/server (issue 673).
package docker
