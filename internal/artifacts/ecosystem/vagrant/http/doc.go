// Package vagranthttp serves the native Vagrant box catalog at Artifactory's
// prefix, /artifactory/api/vagrant/{repo}, on Vagrant local and federated
// repositories: GET or HEAD of a box name answers the catalog that vagrant
// box add, outdated and update read, listing its versions and, for each,
// the providers and architectures whose boxes the caller may read, with
// each box's download URL and SHA-256. The box rules and the index are the
// format package's (artifacts/ecosystem/vagrant); this package answers
// HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Vagrant box catalog of the
// Vagrant slice. It is named vagranthttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type vagrant. Boxes are
//     deployed, deleted and annotated through the ordinary item API, where
//     the format's hooks keep the index; this package only reads.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions and reads the package index, the
//     items and their properties in one transaction; the generic download
//     policy (generic.CheckDownload), which drops a box the repository's
//     include and exclude patterns refuse; the request's base URL
//     (platform/httpkit/baseurl), from which download URLs are built; and
//     the format package, whose rules check the name and verify each box's
//     properties.
//
// # Outputs
//
//   - Protocol: the routes, the local and federated sources and the
//     Artifactory error profile.
//   - Answers: the catalog as JSON, its name and its versions, each with
//     its providers' name, url, checksum_type (sha256), checksum and
//     optional architecture, versions and providers sorted, private and
//     kept by no cache (Cache-Control: private, no-store); and 404 when the
//     box is unknown or no version has a box to list.
//
// # Invariants
//
//   - A box name is <name> or <namespace>/<name> (400), and a request takes
//     no query (400); matrix parameters on the name filter the boxes by
//     their properties.
//   - A box is listed only when the caller may read it, it is in the
//     repository itself (never a remote's cache), the include and exclude
//     patterns allow it and its recorded properties verify. Of several
//     boxes for one version, provider and architecture, the most recently
//     updated is listed, with the path as the tie-break.
//   - Download URLs name the item under /artifactory/{repo}/ and never
//     carry a token.
//   - A catalog lists at most 10,000 versions and 10,000 boxes (503
//     beyond).
//   - A remote or virtual repository has no source and answers 501.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, for which this protocol has no source).
//   - docs/testing/vagrant.md (publishing and installing, updates,
//     permissions and the native-client proof), and
//     docs/client-recipes/vagrant.md (the client recipe, whose evidence is
//     test/acceptance/vagrant_steps_test.go and the @vagrant scenarios).
//   - JFrog Vagrant repositories:
//     https://docs.jfrog.com/artifactory/docs/vagrant-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/vagrant) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/785 and
//     https://github.com/weaveplatform/artifactweave/issues/1049
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
//
// # Testing
//
//   - vagrant_test.go (external, vagranthttp_test), which drives the whole
//     server: catalogs and downloads, the lifecycle of new versions,
//     forged and changed properties, copies, moves, deletes, restores and
//     reindex, statuses and permissions, refused boxes, store failures,
//     and the newest readable box for a duplicate provider.
//
// The @vagrant acceptance scenarios drive the Vagrant client against local
// and one-node federated repositories.
package vagranthttp
