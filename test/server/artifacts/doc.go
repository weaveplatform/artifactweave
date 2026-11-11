// Package artifacts runs the artifacts management API on the assembled
// server: deploying, reading and deleting items with their checksums,
// properties and sidecars; the storage views and summary; the trash can;
// copy and move; build-info and promotion; the GET searches and AQL;
// reindex; repository configuration; remote sources and upstream searches;
// download redirects; multipart uploads; and idempotent build appends.
//
// # Role
//
// Test support: server-assembly suites for the artifacts management API,
// which drive the production router through HTTP requests. The package's
// owner in tools/citestselection/test_ownership.json is shared, so a change
// to it selects full CI. Its directory also holds the ecosystem suites, one
// package per format under test/server/artifacts/<type>.
//
// # Inputs
//
//   - The production router from test/server/support/harness, over in-memory
//     stores seeded by test/server/support: the admin user, whose password is
//     "pw", and the users, permissions and repositories each world adds.
//   - The worlds of fixture_test.go, which keep the names of the router
//     guards' fixtures in internal/app: the harness's item world
//     (newItemWorld); the guards' status fixture's users, repositories and
//     fixture files, with a token service and the multipart upload service
//     (statusFixture), without what only the guards' format, build, search,
//     overwrite, key pair, permission and token rows need; the repository
//     world over a store whose repository index can be made to fail
//     (newRepoWorld); and the identity world (newIdentityWorld). The router's
//     paths, media types, limits and messages that the tests name are
//     constants there, spelled as the router and its handlers spell them.
//   - The worlds that some files build for themselves: builds and their
//     promotion (aql_builds_test.go), searches (searches_test.go), Maven
//     layouts (searches_layout_test.go), a Maven upstream behind a fake
//     server (searches_upstream_test.go), reindex with a recording format
//     (reindex_test.go) and remote sources over stub backends
//     (source_test.go).
//   - PostgreSQL through internal/app/storetest for the cases that run on
//     it too (idempotency_sql_test.go, properties_long_test.go); they skip
//     without Docker.
//
// # Outputs
//
// Assertions only, one file or group of files per area:
//   - items_test.go: deploy and download, matrix parameters, checksum
//     policies and checksum deploys, folders, repository classes, deletes and
//     item permissions, and AQL find; aql_actions_test.go and
//     aql_builds_test.go: AQL delete, property updates and builds.
//   - storage_test.go, storage_list_test.go and storageinfo_test.go: file and
//     folder info, listings, properties, statistics, permissions and the
//     storage summary; mirrors_test.go: the blob mirrors in the summary and
//     their repair; metadata_stats_test.go and properties_long_test.go:
//     statistics writes and long property values.
//   - trash_test.go, trash_handlers_test.go and trash_views_test.go: the
//     trash can on and off, restore, empty and its storage views;
//     transfer_test.go: copy and move.
//   - builds_test.go, builds_diff_test.go, builds_promote_test.go and
//     builds_routes_test.go: build-info publish, read, rename, diff,
//     promotion and retention; idempotency_test.go and
//     idempotency_sql_test.go: idempotent build appends and their replay.
//   - searches_test.go, searches_builds_test.go, searches_layout_test.go and
//     searches_versions_test.go: the GET searches; searches_upstream*_test.go:
//     searches that reach a remote repository's upstream.
//   - reindex_test.go and reindex_remote_test.go: the reindex operations.
//   - repositories_test.go, repositories_format_class_test.go,
//     repo_environments_test.go and virtual_members_test.go: the repository
//     configuration API.
//   - source_test.go, source_listing_test.go, cache_maintenance_test.go and
//     smart_status_test.go: remote sources, browsing, cache maintenance and
//     the smart remote diagnostic.
//   - redirect_test.go, download_policy_test.go, folder_limit_test.go,
//     checksum_files_test.go, sidecar_rights_test.go, overwrite_test.go,
//     format_test.go and trace_test.go: download redirects and policies,
//     folder downloads, checksum and sidecar files, overwrite refusals, what
//     the format sees, and the trace endpoint.
//   - multipart_test.go: the JFrog CLI's multipart upload.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md.
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Decision 0029:
//     docs/research/decisions/0029-cdn-delivery-and-download-redirect.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/<module> holds each module's server-assembly suites).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1067
//
// # Status
//
// Implemented P3 (repositories, items, storage, copy and move, the trash can,
// the CLI's AQL and, with issue 91, multipart uploads), the mirror
// administration of P2b, P4b (download redirects), P5 (full AQL, searches and
// build-info), P8 (reindex) and P9 (remote sources, browsing, cache
// maintenance and upstream searches), with the idempotent build appends of
// decision 0043 in P9c. P9d moved the router-level tests out of the server
// root (TST-05); the tests that call unexported router code stay beside the
// router in internal/app as <name>_internal_test.go.
package artifacts
