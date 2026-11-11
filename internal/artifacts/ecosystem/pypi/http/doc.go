// Package pypihttp serves PyPI repositories at /artifactory/api/pypi/{repoKey}/
// through the router kit (decisions 0032 and 0037): the simple repository
// API that pip, uv and Poetry install from, in HTML (PEP 503) and, when
// pypi.simple_json is on, JSON (PEP 691), with the wheels' core metadata
// files (PEP 658) when pypi.core_metadata is on, and the files themselves
// through the item routes' download, so redirects, Range and download
// counts apply to them as to any file. The pages are built on each request
// from what the PyPI format keeps in the package index; nothing is stored
// for them.
//
// # Role
//
// artifacts module, ecosystem layer: the native PyPI simple and upload
// protocol of the PyPI slice. It is named pypihttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type pypi, and again, through
//     PypiAPI's RegisterPypiMetadataRoutes after the generated routes, for
//     the Warehouse JSON operations they declare: GET and HEAD of the
//     simple index, the core metadata files, the JSON API and the files;
//     twine's upload POST; and a REST deploy's PUT.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads and writes items
//     and writes errors; the format package (artifacts/ecosystem/pypi),
//     whose index records and name rules the pages are built from; and the
//     remote source service (Options.RemoteSources), which fetches upstream
//     indexes, metadata and distributions into the remote cache. PypiAPI
//     builds the second mount's Options from the item kit
//     (artifacts/adapters/http/itemkit): its PyPI format settings and
//     remote source service.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - PypiAPI: the kit wrapper whose RegisterPypiMetadataRoutes mounts GET
//     /artifactory/api/pypi/{repoKey}/pypi/{package}/json and
//     .../{package}/{version}/json, served by Protocol.
//   - JSONMetadata and WriteMetadata, the Warehouse-shaped JSON document
//     and its representation; RepoURL; Reads, the caller's read check that
//     the pages filter by.
//   - Answers: for a local or federated repository, the simple index and
//     JSON pages built from the package index, with what the caller may not
//     read left out; for a remote repository, the upstream pages with the
//     distribution links rewritten to this server and their hashes kept,
//     and checked distributions.
//
// # Invariants
//
//   - A page lists only the files the caller may read.
//   - The HTML pages are byte-exact: the goldens under testdata keep eol=lf
//     (.gitattributes) so they compare the same on every platform.
//   - Uploads to a remote repository are refused, and statuses and error
//     texts stay byte-identical: pip and twine read them.
//
// # References
//
//   - Decision 0037: docs/research/decisions/0037-pypi.md ("Routes", "The
//     simple index", "Core metadata files (PEP 658)", "Downloads and
//     deploys", "Upload", "The JSON API", "P9 shared retrieval contract").
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit).
//   - docs/operations/pypi-remote.md (remote repositories).
//   - JFrog PyPI and remote repositories, as decision 0037 cites them:
//     https://docs.jfrog.com/artifactory/docs/pypi-repositories and
//     https://docs.jfrog.com/artifactory/docs/remote-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/pypi), line 403 (the root pypi_routes.go, now
//     routes.go) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epics ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943) and WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/627 (remote
//     Python repositories),
//     https://github.com/weaveplatform/artifactweave/issues/1032,
//     https://github.com/weaveplatform/artifactweave/issues/1086 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P8, with remote retrieval in P9. Moved here in P9d, which also
// moved the server root's Warehouse JSON registrar here (WEB-17).
//
// # Testing
//
//   - simple_test.go, json_test.go, download_test.go and upload_test.go:
//     the routes through a server against the goldens under testdata.
//   - remote_test.go, remote_parse_test.go, remote_archive_test.go and
//     remote_cached_hash_test.go: remote pages, metadata, archives and
//     cached hashes.
//   - fault_test.go: store faults.
package pypihttp
