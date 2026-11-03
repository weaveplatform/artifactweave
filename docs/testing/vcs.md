# VCS remote REST client

Artifactory VCS repositories serve source archives and Git references through
`/artifactory/api/vcs`. They are remote repositories. Use `curl` for this REST
contract; `git clone`, `fetch`, `pull`, and `push` are not VCS repository
operations. See the [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories)
and [Git client clarification](https://jfrog.com/help/r/artifactory-git-client-commands-git-clone-fetch-pull-push-are-not-supported-against-a-vcs-remote-repository).

Create a remote VCS repository with an approved upstream host, for example:

```sh
curl --fail-with-body --user "$ADMIN_USER:$ADMIN_PASSWORD" \
  --request PUT "$REGISTRY/artifactory/api/repositories/vcs-github" \
  --header 'Content-Type: application/json' \
  --data '{"rclass":"remote","packageType":"vcs","url":"https://github.com","vcsGitProvider":"GITHUB"}'
```

List references and fetch an unchanged upstream archive:

```sh
curl --fail-with-body --user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
  "$REGISTRY/artifactory/api/vcs/tags/vcs-github/OWNER/REPOSITORY"
curl --fail-with-body --user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
  "$REGISTRY/artifactory/api/vcs/branches/vcs-github/OWNER/REPOSITORY"
curl --fail-with-body --user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
  "$REGISTRY/artifactory/api/vcs/downloadTag/vcs-github/OWNER/REPOSITORY/TAG?ext=tar.gz" \
  --output source.tar.gz
tar -tzf source.tar.gz
```

For GitLab projects in a subgroup on the current target release, set
`vcsGitProvider` to `GITLAB`, point the remote URL to the GitLab host, and use
the full group/subgroup path. The `archive/refs` form is required for subgroup
downloads in [JFrog's current GitLab instructions](https://jfrog.com/help/r/artifactory-how-to-proxy-gitlab-in-vcs-repository-and-resolve-packages):

```sh
curl --fail-with-body --user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
  "$REGISTRY/artifactory/api/vcs/tags/vcs-gitlab/GROUP/SUBGROUP/REPOSITORY"
curl --fail-with-body --user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
  "$REGISTRY/artifactory/api/vcs/downloadBranch/vcs-gitlab/GROUP/SUBGROUP/REPOSITORY/archive/refs/BRANCH?ext=zip" \
  --output branch.zip
unzip -t branch.zip
```

For a GitLab branch such as `feature/one`, put its literal slash after
`archive/refs`, for example `.../REPOSITORY/archive/refs/feature/one`.
This routing interpretation is covered by the local curl acceptance test;
JFrog's public API does not show a slash-bearing ref example, so target-version
wire parity remains part of [#216](https://github.com/weaveplatform/artifactweave/issues/216).
The ordinary download route documents a one-segment branch parameter; its
slash encoding is not asserted here.

The implementation validates refs and archive contents before cache admission,
keeps authorization scoped to the source path, and applies the configured
remote source transport's redirect and credential policy. A branch or tag
archive download is cached under its resolved commit; cached content remains readable
when the remote is offline, subject to current repository permission. Cache
delete, zap, and metadata refresh use the repository's generic cache maintenance
routes; JFrog does not document a VCS-specific reindex endpoint or a cache
restore operation.

`STASH` supports Bitbucket Server/Data Center 5.1+ using its native REST
archive API. `OLDSTASH` uses the older archive-plugin servlet, verified against
plugin releases 1.3.2 and 2.1.5. Set their remote URL to the server root,
including its deployment context, such as `https://git.example/bitbucket`.
The older provider requires the archive plugin; after upgrading Bitbucket to
5.1+, change the provider to `STASH`.

`ARTIFACTORY` uses a peer VCS API URL such as
`https://peer.example/artifactory/api/vcs/source-vcs`. Change an older plain
repository URL to this explicit API form before selecting this provider.
The peer's documented tag/branch JSON endpoints resolve immutable commits;
`downloadCommit` supplies the unchanged source archive. Peer selected-file APIs
return a validated, unchanged ZIP with the exact requested file; these ref-based
responses use mutable cache freshness. Metadata zap leaves these binary entries
intact; expiry or explicit cache deletion refreshes them. Stash file selection
uses exact rootless paths. Git commit markers in archives must match the
requested commit. CUSTOM templates retain both rootless and wrapped archive
compatibility: an exact path wins over any nested basename, and ambiguous
wrapper matches are rejected. Successful peer file delivery updates cache usage;
HEAD, denied reads and failed response writes do not. STASH, OLDSTASH and
ARTIFACTORY restrict redirects to the exact origin, path and query. Configure private
upstream credentials on the remote repository, separately from client tokens.
See [primary contract evidence](../research/p9c-vcs-legacy.md) for versions,
publisher source hashes and the remaining #216 comparison scope.

The authored contract notes record the publisher tag/branch object fields. Its `refs`
string content remains unspecified; artifactweave's outward `refs` wire format
still requires a live comparison. Peer discovery avoids that unspecified field.

CI runs `TestAccVCSRemoteRESTAndSubgroup` with the runner's recorded `curl`
version, exercising standard and subgroup routes, unchanged archive bytes,
permissions, and malformed requests. Focused local checks:

```sh
ARTIFACTWEAVE_REQUIRE_CURL=1 go test ./internal/server -run '^TestAccVCSRemoteRESTAndSubgroup$' -count=1 -v
go test ./internal/server/vcs -count=1
```

Run the complete pinned-client provider checks locally:

```sh
docker build --platform linux/amd64 -f test/acceptance/clients/maven.Dockerfile -t artifactweave-vcs-client .
GOWORK=off ARTIFACTWEAVE_ACCEPT_LOCAL=1 ARTIFACTWEAVE_ACCEPT_DOCKER=1 \
  ARTIFACTWEAVE_MAVEN_IMAGE=artifactweave-vcs-client ARTIFACTWEAVE_ACCEPT_TAGS=@vcslegacy \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1 -timeout=15m
```

The scenarios use a fresh curl output destination on every request and verify
actual bytes from controlled public/private sources, metadata refresh, offline
cache access, deletion and non-leaking failures. CI's JVM native-client partition
includes these `@maven` scenarios.
