// Package generic runs the generic remote repository routes of the
// assembled application: downloads through the remote cache, sidecars,
// authorization and its revocation, restarts and signed delivery. It also
// holds the headers every stored file downloads with.
//
// # Role
//
// Test support: server-assembly suites for generic remote repositories,
// outside the modules. They drive the production router over the
// application runtime through HTTP requests. The package's owner in
// tools/citestselection/test_ownership.json is shared, since generic is no
// selector owner, so the suites run in the full profile only.
//
// # Inputs
//
//   - The application runtime and the production router from
//     test/server/support/harness, through the harness's remote world under
//     the name the tests had in internal/app (fixture_test.go,
//     newRemoteWorld): the generic remote repository remote over a fake
//     upstream, the users reader and denied and the remote-read permission.
//   - Runtimes the restart tests open and close themselves (testConfig and
//     openRuntime over the harness's configuration and costs), and a blob
//     store that signs CDN URLs.
//   - The harness's item world (harness.NewItemWorld) with a Vagrant local
//     repository, for the download headers, and with a PyPI local
//     repository, for a HEAD with Range; and as it is, for the media types
//     a stored file downloads with.
//
// # Outputs
//
// Assertions only:
//   - remote_test.go: the remote download lifecycle, transient sidecars and
//     upstream failures, authorization and revocation, trace fetches,
//     cache identity across a restart, which no configuration descriptor
//     exposes, and cold and warm signed delivery with its checksums.
//   - download_headers_test.go: a file's ETag, the bare SHA-1, and the
//     conditional requests that send it back, and a box's media type, on
//     the item route and the Vagrant box file route (#1266, #1267).
//   - mime_types_test.go: the 7.x type of every name 7.x's default MIME
//     table types differently, on the item route, the deploy and storage
//     info answers, a redirect's signed URL, an archive entry and the trash
//     listing (#1315); and the upstream labels a remote caches a file under,
//     refusing only the block list or the override list that replaces it
//     (#1316).
//   - head_range_test.go: a HEAD with Range answers 200 with the whole
//     length on a local, redirecting, virtual, PyPI and remote file, where
//     a GET still gets the range (#1338).
//   - remote_upstream_nostore_test.go: a generic, Maven, npm and PyPI
//     remote keeps the answers an upstream marks Cache-Control: no-store,
//     serves them again without the upstream and once it is gone, and
//     still refreshes mutable metadata under a zero retrieval cache
//     period. The test repoints the remote world's repository at each
//     package type in turn.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - docs/operations/remote-repositories.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1096
//
// # Status
//
// Implemented P9 (remote repositories). P9d moved the tests here from
// internal/app (TST-07).
package generic
