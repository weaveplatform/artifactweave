// Package support holds server-route test data and request helpers shared by
// independently runnable protocol test packages. Production code must not
// import this package.
//
// # Role
//
// Test support, outside the modules, for the server-assembly suites under
// test/server and for the router's guards in migration/app.
//
// # Inputs
//
//   - The stores, authentication and format packages the fixtures are built
//     from.
//
// # Outputs
//
//   - The route inventory of each format (DockerRoutes, GoRoutes, HelmRoutes,
//     MavenRoutes, NPMRoutes, NuGetRoutes, PyPIRoutes, TerraformRoutes,
//     TerraformBackendRoutes). Each format's suite holds its status and
//     principal tables to its inventory, and the router's guards in
//     migration/app count those routes as covered; DockerHandwrittenRoutes and
//     NPMGeneratedRoutes give those guards the parts they treat apart.
//   - Request helpers (Do, Basic, Bearer, ArtifactoryError) and seeding (Seed,
//     over any SeedStore, with the fast TestHash and CheapHash, the hash of
//     the shared password).
//   - The artifacts module's Repositories and Layouts, bound as the server
//     binds them, for the tests of other modules that apply through them.
//   - Format fixtures: npm publish bodies and tarballs, NuGet packages and
//     PDBs, Helm charts, OCI images and manifests, Terraform repositories and
//     the backend keyring, and the gzip-compressed tar archives of Conan and
//     CRAN (TarGz).
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/673
//
// # Status
//
// Implemented P9b (issue 673).
package support
