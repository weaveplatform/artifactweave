# Bower native client and repository proof

Checked 2026-10-25 against the current [JFrog Bower repository guide](https://docs.jfrog.com/artifactory/docs/bower-repositories), [JFrog remote repository fields](https://docs.jfrog.com/artifactory/docs/remote-repositories), [Git smart HTTP protocol](https://git-scm.com/docs/http-protocol), and the pinned `bower@1.8.14` with `bower-art-resolver@2.0.10` in [bower.Dockerfile](../../test/acceptance/clients/bower.Dockerfile). The Bower client installs from the `/artifactory/api/bower/{repoKey}` registry. JFrog's resolver uses `packages`, `refs`, and `binaries` requests; the upstream Bower registry maps a package name to a Git URL. The source policy checks that URL against the remote repository's configured Git host and path before fetching it.

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
  ./test/acceptance -run '^TestFeatures$'
```

The separate read-only [live public probe](../../test/acceptance/features/bower_live.feature) installed Bootstrap 3.4.1 from `registry.bower.io` and GitHub on 2026-10-25 using the same pinned client. It is opt-in because the external hosts are not under CI control. Run it with `ARTIFACTWEAVE_ACCEPT_TAGS=@bowerlive` and the same acceptance command. The unchanged public refs response was about 1.50 MB; the GitHub tarball was about 4.25 MB and contained a PAX global header and a root directory. The implementation accepts those structures while retaining its path, manifest, expansion and integrity checks.

## Remaining Phase 9c Bower paths

This core implementation does not provide the documented private `ssh://` Stash registration and Git transport, or virtual external-dependency rewrite/allowlist and `art://` shorthand. They remain in Phase 9c as **LAN-08** and **LAN-09** under [EPIC-LAN](https://github.com/weaveplatform/artifactweave/issues/761). The language-format epic requires their native-client proof before Bower parity is complete. The current Bower resolver's synthetic `art://{repoKey}/{name}` address is a repository-local routing convention; it does not implement the external shorthand rewrite.
