# Bower native client and repository proof

Checked 2026-11-03 against the current [JFrog Bower repository guide](https://docs.jfrog.com/artifactory/docs/bower-repositories), [JFrog remote repository fields](https://docs.jfrog.com/artifactory/docs/remote-repositories), [Git smart HTTP protocol](https://git-scm.com/docs/http-protocol), and the pinned `bower@1.8.14` with `bower-art-resolver@2.0.10` in [bower.Dockerfile](../../test/acceptance/clients/bower.Dockerfile). The Bower client installs from the `/artifactory/api/bower/{repoKey}` registry. JFrog's resolver uses `packages`, `refs`, and `binaries` requests; the upstream Bower registry maps a package name to a Git URL. The source policy checks that URL against the remote repository's configured Git host and path before fetching it.

## Client connection

For a repository named `bower-local`, put this `.bowerrc` in the Bower project. Replace the base URL and credentials with a principal that can read the repository. Use a secret store or other client configuration in place of embedding a real password in a checked-in file.

```json
{
  "registry": {
    "search": ["https://USER:PASSWORD@HOST/artifactory/api/bower/bower-local"],
    "register": "https://USER:PASSWORD@HOST/artifactory/api/bower/bower-local"
  },
  "resolvers": ["bower-art-resolver"],
  "interactive": false
}
```

Install with `bower install hello#1.0.0`. Bower does not publish artifacts into a local repository; publish a versioned tree through the item API. For example, `PUT /artifactory/bower-local/hello/1.0.0/bower.json` with `{"name":"hello","version":"1.0.0"}`, then upload its other files beneath the same path. The manifest name/version must agree with that path. `POST /artifactory/api/bower/bower-local/reindex` rebuilds discovery after restore or repair.

For the public Bower registry and GitHub, create a remote Bower repository with `url: https://github.com/`, `bowerRegistryUrl: https://registry.bower.io`, and `vcsGitProvider: GITHUB`; use its `/api/bower/{repoKey}` URL in `.bowerrc`. A private HTTPS Git source uses its approved Git origin in `url`, a configured registry in `bowerRegistryUrl`, and repository credentials for that Git origin. Registry pointers outside the configured Git origin/path are rejected. The source fetcher constrains redirects, DNS destinations, credentials, archive size, file count, paths, the package manifest and gzip integrity. Git tags supply the version when an upstream `bower.json` omits it, as the unchanged Bootstrap 3.4.1 archive does.

For an Artifactory-compatible upstream, set `vcsGitProvider: ARTIFACTORY` and its Bower API base in `url`. This selects the upstream `refs`/`binaries` exchange. A virtual Bower repository resolves member content in configured order; a federated repository has the same one-node publication and resolution behavior as local. Cross-peer federation is tracked in P13.

## Evidence

The deterministic [Bower acceptance suite](../../test/acceptance/features/bower.feature) covers local and one-node federated publication, ordered virtual selection, Artifactory remote cache and offline reuse, Git-pointer remotes with public-style and credentialed private HTTPS fixtures, search, lifecycle/reindex, refresh, same-origin redirect, plain Git tag mapping, and corrupt archive rejection. It uses the real pinned CLI/resolver in Docker. On macOS with Docker Desktop, run:

```sh
ARTIFACTWEAVE_ACCEPT_LOCAL=1 ARTIFACTWEAVE_BOWER_DESKTOP=1 \
  ARTIFACTWEAVE_ACCEPT_TAGS=@bower go test -timeout=15m -count=1 \
  ./test/acceptance -run '^TestAccFeatures$'
```

The separate read-only [live public probe](../../test/acceptance/features/bower_live.feature) installed Bootstrap 3.4.1 from `registry.bower.io` and GitHub on 2026-10-25 using the same pinned client. It is opt-in because the external hosts are not under CI control. Run it with `ARTIFACTWEAVE_ACCEPT_TAGS=@bowerlive` and the same acceptance command. The unchanged public refs response was about 1.50 MB; the GitHub tarball was about 4.25 MB and contained a PAX global header and a root directory. The implementation accepts those structures while retaining its path, manifest, expansion and integrity checks.

## Private registration and external dependency rewrite

The native suite now proves LAN-08 and LAN-09 with Bower 1.8.14, bower-art-resolver 2.0.10, and Node 26.13.0. See the [contract research](../research/38-bower-registration-and-external-dependencies.md) for the provider/client source evidence.

For private Stash, configure a remote with `url: https://stash.example/context`, `vcsGitProvider: STASH`, and its HTTP credentials. Use `OLDSTASH` for the older archive-plugin servlet. Then, through that remote or an aggregating virtual registry, run:

```sh
bower register hello ssh://git@stash.example:7999/team/hello.git
bower install hello#1.0.0
```

The native Bower command first verifies the Git repository using the client's own SSH key and trusted host key. The application treats this SSH address as a same-host project/repository locator and uses the configured provider HTTP service for refs and archives. It does not run a Git checkout over SSH on the server. Registration needs write permission on the package in the addressed repository and selected remote. Registrations are immutable; duplicates use the pinned client's documented 403 response. They survive cache zap and server restart, and are removed with their repository. Private Stash registration does not consult the public Bower registry.

Enable external rewriting on a virtual repository and select an aggregated remote cache:

```json
{
  "rclass": "virtual",
  "packageType": "bower",
  "repositories": ["bower-local", "bower-remote"],
  "externalDependenciesEnabled": true,
  "externalDependenciesPatterns": ["https://git.example/**", "https://cdn.example/packages/**"],
  "externalDependenciesRemoteRepo": "bower-remote"
}
```

Downloaded `bower.json` dependencies and development dependencies are rewritten to resolver addresses. Direct TAR/GZIP or ZIP dependencies are validated and cached before the parent is delivered. Git/shorthand targets retain native Bower release selection for exact, partial and wildcard versions (`2.0.0`, `2`, `2.0`, `2.x`, `2.0.x`); the client selects the actual `v2.0.0` tag. Non-semantic branch targets also retain native fetch behavior. Each selected archive is validated against its requested version and current virtual/member manifest permissions before admission and delivery, including after transitive rewriting; versionless Git manifests remain valid. Transitive package manifests are rewritten on delivery. The selected cache member and virtual repository both require read permission, and every external target or redirect must match the virtual allowlist, including the actual Git `info/refs?service=git-upload-pack` metadata endpoint. Every disclosed version rechecks current virtual and member manifest permissions. Credentials stay scoped to the configured HTTP origin. Cached/offline content remains subject to current allowlist, membership and caller authority.

For project-level shorthand, add `"shorthand-resolver": "art://{{owner}}/{{package}}"` to `.bowerrc` and run `bower install team/hello#1.0.0`, or use `bower install art://team/hello#1.0.0` directly. The selected remote supplies the approved Git origin and provider. The existing `art://{repoKey}/{name}` addresses continue to select registered/indexed repository packages.

The 2026-11-03 deterministic run passed all Bower scenarios, including actual authenticated SSH client registration with pinned host keys for STASH/OLDSTASH, fresh remote and virtual installs, fresh offline installs, external dependency rewriting, exact Git versions with `v`-prefixed tags, native local `art://` dependencies, and shorthand. The archive suite also covers unsafe paths/links, corrupt compression, invalid identity, malformed registration, disallowed URLs/cache members and in-flight authority changes.
