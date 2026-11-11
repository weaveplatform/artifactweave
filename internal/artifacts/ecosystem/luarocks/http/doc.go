// Package luarockshttp serves local and federated LuaRocks repositories
// through the native flat rocks-server URL and the native upload API, and
// remote and virtual ones through the same URL, on Artifactory's
// /artifactory/api/luarocks/{repo} prefix: the manifest files and the
// rockspecs and rocks they name, and the upload client's tool version,
// rockspec check and rockspec and rock uploads. The file rules and the
// index are the format package's (artifacts/ecosystem/luarocks); this
// package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native LuaRocks protocol (local,
// remote, virtual and federated) of the LuaRocks slice. It is named
// luarockshttp so that it does not shadow net/http. Outside tests, only its
// own http subtree, the module's http adapters and app may import it
// (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type luarocks.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads multipart
//     uploads, stores uploaded files and their signatures through the item
//     service, where the format's hooks admit them, reads the package
//     index, items and blobs, builds a virtual repository's member
//     requests, serves items and writes errors; the remote source service,
//     which fetches a remote's upstream manifests and rocks into the remote
//     cache; and the format package, whose rules parse every file name,
//     check every upstream file and map upload version ids.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: a manifest file or a stored file (GET and HEAD of a path);
//     the tool version (api/tool_version); a rockspec check
//     (api/1/{key}/check_rockspec) naming the module and its version id
//     when they exist; a rockspec upload's JSON (api/1/{key}/upload), with
//     the version id and the module URL; and a rock upload's success
//     (api/1/{key}/upload_rock/{versionID}).
//   - RedactAPIKey: a path with the upload API's key replaced, which the
//     server's access and panic logs record instead of the path.
//
// # Invariants
//
//   - Reads take the read grant and uploads the write grant on the
//     repository. An uploaded rockspec needs a valid root rockspec file
//     name (400); a rock's version id must name a stored version (404) whose
//     package and version its file name gives (400). A signature,
//     rockspec_sig or rock_sig, must be <file>.asc of at most 1 MiB (400)
//     and is kept as that file's sidecar.
//   - A remote reads the upstream manifest and manifest-5.1 to manifest-5.5
//     as bounded Lua tables (32 MiB, 200,000 fields and a depth of 24),
//     never running them, and answers each filtered by the caller's read
//     grant; historical entries without a revision are left out. It
//     fetches a rock only when the current upstream manifest lists its
//     exact file name, and caches it only when it passes the format's
//     checks.
//   - A virtual repository's members are distinct local or remote LuaRocks
//     repositories, checked at each request (400). Their manifests merge in
//     member order, each filtered by the caller's grant on that member; the
//     first member that holds an exact rock supplies it; and an upload goes
//     to the default deployment repository, which must be a local member
//     (405 when none is set, 400 when it is missing or not local) that the
//     caller may also write to (403).
//   - Remote and virtual reads take no query (400).
//   - The download policy (decision 0032) is checked after Read permission
//     at the path a client asks for. A local or federated manifest is a
//     stored document 7.x reads through the download path, so a blackout,
//     or patterns that reject the manifest's own path, refuse it, while a
//     rock the patterns reject stays listed and only its download is
//     refused. A remote applies its own policy at the manifest or rock
//     asked for; the manifest lookup behind a rock download takes the
//     blackout alone. A virtual repository's own patterns refuse the
//     manifest or a rock with "No repository found to serve the request";
//     its manifest leaves out a member that is blacked out or whose
//     patterns reject the manifest, and a rock request passes over a
//     member that refuses the rock.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories); decision 0008:
//     docs/research/decisions/0008-virtual-repositories.md (virtual
//     repositories).
//   - docs/testing/luarocks.md (the native operations, the remote and
//     virtual rules and the evidence), and docs/client-recipes/luarocks.md
//     (the client recipe, whose evidence includes
//     test/acceptance/luarocks_steps_test.go).
//   - JFrog LuaRocks repositories:
//     https://docs.jfrog.com/artifactory/docs/luarocks-repositories, and the
//     LuaRocks upload client:
//     https://github.com/luarocks/luarocks/blob/main/src/luarocks/cmd/upload.lua.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/luarocks) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/684,
//     https://github.com/weaveplatform/artifactweave/issues/685,
//     https://github.com/weaveplatform/artifactweave/issues/686,
//     https://github.com/weaveplatform/artifactweave/issues/1050 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Moved here in P9d.
//
// # Testing
//
//   - luarocks_test.go (external, luarockshttp_test): native upload and
//     install from the manifest, refusals that never echo the upload key,
//     bad rocks and denied reads, manifests that follow delete, restore,
//     copy and reindex, the Lua version manifests' constraints, upload
//     validation and store faults.
//   - remote_test.go (external): the remote manifest's filtering and cache,
//     refused requests before any rock is fetched, invalid manifests and
//     rocks, refresh and zap without eager rock fetches, and unlisted or
//     broken upstream answers.
//   - remote_fence_test.go: the manifest's and the file's authority
//     rechecked when a remote rock is delivered.
//   - remote_manifest_test.go: the manifest parser, which keeps allowed
//     metadata, accepts dotted names, skips legacy versions and refuses
//     executable, ambiguous or invalid input without running Lua.
//   - virtual_routes_test.go (external): virtual manifest and rock
//     resolution, uploads to the deployment target, refused members,
//     queries and denied members, the target's checks and grants, empty and
//     failing manifests, member changes at request time and store faults.
//   - virtual_test.go: the virtual source's refusal of a single-item
//     lookup, and manifest merging in member order.
//   - policy_test.go (external): the download policy on the local, remote
//     and virtual manifests and rocks: blackout, patterns on the manifest
//     and on one rock, members passed over or left out, and the virtual
//     repository's own patterns.
//
// The @luarocks acceptance scenarios drive the luarocks client against
// local, federated, remote and virtual repositories.
package luarockshttp
