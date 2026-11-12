// Package golanghttp serves Go module repositories at
// /artifactory/api/go/{repoKey}/ through the router kit (decisions 0032 and
// 0035): the GOPROXY reads the go command makes, and the deploys of
// jf go-publish. Paths, version selection and the package index are the
// format package's (artifacts/ecosystem/golang); this package answers HTTP
// with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native GOPROXY protocol of the Go
// modules slice. It is named golanghttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type go: GET and HEAD for
//     @v/list, @latest, @v/latest.info and a version's .info, .mod and .zip,
//     and PUT for the version files jf go-publish deploys.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, downloads and stores
//     items and writes errors; and the format package, which parses paths,
//     reads the package index and chooses versions.
//
// # Outputs
//
//   - Protocol: the routes, the absent sumdb/ prefix and the plain text
//     error profile, which the go command needs to show an error's body.
//   - Answers: version lists, .info documents (stored, or made from the
//     package index), .mod and .zip downloads, and deploys.
//
// # Invariants
//
//   - A path that names no Go operation answers 404 to every method; only
//     one that does answers 405 to a method no route serves.
//   - A version list says nothing about what the caller may not read: a
//     module with no version the caller may read answers 404.
//   - A deploy of different content over a stored .zip or .mod answers 409
//     even for a caller who may delete, since go.sum pins it.
//   - A version is visible by what is stored, and the download policy
//     (decision 0032) refuses only the file a read serves: a blacked-out
//     repository's, or one the patterns reject, while a .mod they accept
//     is served beside a .zip they reject. A .info made from the package
//     index answers the policy at its own path, as a stored .info does
//     (RTDEV-60689).
//   - A blacked-out repository refuses @v/list and @latest too, once the
//     caller may read a version, so read permission still comes first; the
//     patterns never refuse them (#1237).
//   - Statuses and error texts stay byte-identical: the go command and the
//     JFrog CLI read them.
//
// # References
//
//   - Decision 0035: docs/research/decisions/0035-go-modules.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers).
//   - docs/implementation/go-upstream-source.md (remote Go repositories).
//   - Research 26: docs/research/26-go-modules.md, section 1.1 (errors the
//     go command shows).
//   - JFrog Go registry documentation, as decision 0035 cites it:
//     https://docs.jfrog.com/artifactory/docs/go-modules.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 230 (ecosystem/golang and its http package) and line 1023 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1014 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P7, with remote retrieval in P9. Moved here in P9d.
//
// # Testing
//
//   - golang_test.go: version lists, @latest, .info, .mod and .zip reads,
//     repositories and paths, absent paths, store faults and paged lists.
//   - publish_test.go: deploys, republishing, refusals and a .info body
//     that cannot be read.
//   - download_policy_test.go: a blacked-out repository and excluded files.
//   - All three mount the protocol as the server mounts it, with the access
//     test kit (access/accesstest) for permissions.
//   - The guard suite test/server/artifacts/golang drives the protocol
//     through the whole server.
package golanghttp
