// Package ansiblehttp serves Galaxy v3 collection requests through the shared
// package protocol, source and item boundaries, for local, remote, virtual
// and federated repositories on Artifactory's /artifactory/api/ansible/{repo}
// prefix: collection discovery, version and dependency metadata, archive
// downloads and collection publishing for ansible-galaxy. The archive
// rules and the index are the format package's (artifacts/ecosystem/ansible);
// this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Galaxy v3 collection
// protocol of the Ansible slice. It is named ansiblehttp so that it does
// not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type ansible.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads multipart uploads, stores a
//     published archive through the item service, where the format's hooks
//     admit and index it, reads the package index, items and blobs, builds a
//     virtual repository's member requests, serves items and source results
//     and writes errors; the remote source service, which fetches a remote's
//     upstream Galaxy metadata and archives into the remote cache; the
//     request's base URL (platform/httpkit/baseurl), onto which download
//     URLs are rewritten; the caller's principal (the kernel), which makes a
//     denied source read answer 401 for an anonymous caller and 403 for any
//     other; and the format package, whose rules check every identity,
//     version, dependency range and archive.
//
// # Outputs
//
//   - Protocol: the routes, the source of each repository class and the
//     Artifactory error profile.
//   - Answers: the API root's available versions; a collection's namespace,
//     name and first and last version times
//     (api/v3/collections/{namespace}/{name}/); its version list, newest
//     first, with count and next link (.../versions/); a version's
//     metadata, with its dependencies, its archive's SHA-256 and a download
//     URL on this repository (.../versions/{version}/); the archive itself
//     (api/v3/plugin/ansible/content/published/collections/artifacts/{file});
//     a publish's import task URL (201, POST api/v3/artifacts/collections/);
//     and that task's completed state (api/v3/imports/collections/{task}/).
//
// # Invariants
//
//   - Collection, version list and version reads take the read grant on
//     the collection's folder, and a download the grant on its archive
//     path; every version is filtered by the caller's read grant on its
//     archive path. Identities, versions and archive names must be valid
//     (400). Version lists take an offset from 0 to 100,000 and a limit
//     from 1 to 100, 100 by default (400).
//   - A local version comes from the package index, and its stored archive
//     must still have the SHA-256 the index records.
//   - A publish needs a local or federated repository (405), the write
//     grant on the collections folder and on the archive's path, a file of
//     at most 128 MiB whose SHA-256 the form's sha256 field names (400) and
//     a valid archive (400); the archive's MANIFEST.json gives its path,
//     and an existing version answers 409. Its import task is the archive's
//     SHA-256.
//   - A virtual repository merges its members in order, the first member
//     that has a version supplying it; a member that is missing or denies
//     the read is skipped, and any other failure stops the read.
//   - A remote's upstream API is its URL as it is when that ends in
//     /api/v3 or the published content root, galaxy.ansible.com's
//     published content root for that host's root, and the URL with
//     /api/v3 appended otherwise. Upstream metadata is JSON of at most
//     2 MiB a document, validated before it is cached: at most 100 pages
//     of at most 100 versions, with no repeated page or version, and next
//     links that stay on the same collection endpoint and origin; and
//     version documents that name the requested identity, a SHA-256, an
//     http or https download URL without credentials or fragment, and
//     valid dependencies. An archive is fetched only for a listed version,
//     and cached only when it has the advertised SHA-256, validates, and
//     names the same identity and dependencies. Source authority and the
//     caller's read grant are rechecked before metadata or an archive is
//     delivered. A missing upstream Last-Modified becomes the current
//     time, so it never pins a client's version cache.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers, and its section "P9c Ansible
//     collection adapter (#782)"); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories); decision 0008:
//     docs/research/decisions/0008-virtual-repositories.md (virtual
//     repositories).
//   - docs/operations/ansible.md (connecting ansible-galaxy, publishing,
//     the remote and virtual rules and the evidence), and
//     docs/client-recipes/ansible.md (the client recipe, whose evidence
//     includes test/acceptance/ansible_steps_test.go).
//   - JFrog Ansible repositories:
//     https://docs.jfrog.com/artifactory/docs/ansible-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/ansible) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/782 and
//     https://github.com/weaveplatform/artifactweave/issues/1051
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - ansible_test.go (external, ansiblehttp_test): publish, resolve and
//     delete; bad archives and denied reads refused; the remote cache and
//     offline reads; and publishes without the right checksum or deploy
//     permission refused.
//   - authority_test.go (external): read permission rechecked before
//     metadata delivery, member and catalog source fences kept through a
//     download, archives from a CDN without credentials and within the
//     repository's patterns, unreadable remote versions filtered, and an
//     archive whose identity differs refused even when its hash matches.
//   - fault_test.go (external): store failures that never expose
//     successful metadata, and remote archive integrity and write denial.
//   - remote_test.go (external): malformed upstream metadata refused,
//     refresh and a cold offline miss, remote pagination and a virtual
//     miss, and no-store or invalid archives never cached.
//   - status_test.go (external): route statuses and the lifecycle, a
//     virtual repository's ordered readable members, and pagination and
//     path permissions.
//   - helpers_test.go: the upstream Galaxy base and the pagination bounds.
//
// The @ansible acceptance scenarios drive ansible-galaxy against local,
// federated, virtual and remote repositories, offline and with bad
// archives and credentials.
package ansiblehttp
