// Package pkgroute is the router kit for package formats with a protocol of
// their own: npm and Go in P7, then PyPI, NuGet, Helm and Terraform
// (decision 0032). Mount claims /artifactory/api/<name>/{repoKey}/... for
// one protocol, or /artifactory/api/<name>/... when the protocol finds the
// repository key elsewhere in the path (Protocol.Resolve), and any routes
// it has at the host root (Protocol.Host). Before the protocol's handlers
// run it resolves the repository, checks its package type and passes it
// through the class gate, so every protocol answers those the same way and
// in its own envelope. The kit's helpers then check permissions, download
// through the item routes' download path, deploy through the item service
// and read multipart uploads (ReadForm).
//
// Docker's registry predates the kit and keeps its own router (decision
// 0031).
//
// # Role
//
// artifacts module, HTTP adapter layer: the router kit for native package
// protocols. The router in app and the ecosystem protocol packages
// (ecosystem/<type>/http) mount their protocols through it. It never imports
// app, whose router hands it what it needs through Options, and it reaches the
// access module only through access/api: the caller is the kernel principal,
// and permissions are checked with the request's accessapi.Decider on
// accessapi.Repo values.
//
// # Inputs
//
//   - Mount: a chi router, Options (the store, the item service, the item
//     routes' download, deploy, error mapping and created answer, and the
//     fault recorder) and the Protocol: its name, package types, error
//     profile, reserved operations, absent paths, key resolution, anonymous
//     challenge, host routes, signed requests and handlers.
//   - The store, a Store: the artifacts module's stores and transactions
//     (artifacts/core/port), with the settings and configuration documents,
//     the work queue and the lease table (platform/settings,
//     platform/configdoc and platform/work), which handlers hand on through
//     DB() to the item service, a format's Env and the package index. It
//     holds no access store: permissions come from the request's decider.
//   - The request's principal and decider in its context, which the
//     server's authentication middleware put there.
//   - Remote: a protocol's opt-in to the common remote source boundary,
//     over the source service, which Kit.Read reads through.
//
// # Outputs
//
//   - The mounted routes, and the Kit each handler receives.
//   - Store, the store the kit resolves repositories and reads items in, and
//     DB() returns. Each composite store in app satisfies it, so the server
//     passes the store it holds.
//   - Permission helpers: Can, CanFolder, Require and RequireFolder.
//   - Download helpers: Item, ResolveItem, ServeItem, ServeFound, Read,
//     ServeResult, ServeStream and SourceError.
//   - Deploy helpers: Store, StoreOnce, StoreNew, StoreResolved, Upload,
//     Deploy, DeployItem, Created, Annotate, Delete and DeleteTree.
//   - Multipart helpers: ReadForm, ReadFormLimited and ReadFormWith; path
//     and header helpers: ParsePath, SplitValues, DeclaredChecksums and
//     SetChecksums.
//
// # Invariants
//
//   - A protocol's handler runs only for an existing repository of one of
//     its package types that the class gate admits; anything else answers
//     in the protocol's own envelope before it.
//   - A deploy goes through the item service, so formats' hooks and the
//     item routes' rules apply as they do to any other write.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the protocol kit and the class gate).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote content through the source boundary).
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md
//     (the permission checks).
//   - Research 24: docs/research/24-package-format-handlers.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 238 to 246 (artifacts/adapters/http) and line 1021 (the package
//     mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals and
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1074 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P7: the kit with npm and Go (pull request 330). P8 added
// host-root routes, encoded repository keys and multipart uploads (pull
// request 580) for PyPI, NuGet, Helm and Terraform; P9 the remote source
// boundary (pull requests 644 and 654); P9b and P9c the protocols that
// followed. Moved here in P9d, which narrowed the kit's store to Store
// (https://github.com/weaveplatform/artifactweave/issues/1089).
//
// # Testing
//
// The package's own tests mount a test protocol over the in-memory store,
// authorised by the access test kit: pkgroute_test.go, resolve_test.go,
// folder_test.go, form_test.go, lifecycle_test.go, storeonce_test.go,
// source_test.go, source_delivery_test.go and path_test.go;
// source_result_test.go tests ServeResult from inside the package. Every
// native protocol's route suite under test/server/artifacts reaches it
// through the router.
package pkgroute
