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
download is cached under its resolved commit; cached content remains readable
when the remote is offline, subject to current repository permission. Cache
delete, zap, and metadata refresh use the repository's generic cache maintenance
routes; JFrog does not document a VCS-specific reindex endpoint or a cache
restore operation.

`STASH`, `OLDSTASH`, and `ARTIFACTORY` are JFrog VCS provider configuration
values whose upstream ref/archive mappings require target-version capture.
artifactweave currently rejects those VCS configurations with a clear error;
their adapters remain in scope in [#844](https://github.com/weaveplatform/artifactweave/issues/844). This does not
affect Bower or other package types that store the same provider field.

The public JFrog API reference does not provide a complete response example for
the tag/branch list or `refs` body. Their exact current-release wire shapes
remain an inference until the [live parity capture](https://github.com/weaveplatform/artifactweave/issues/216).

CI runs `TestAccVCSRemoteRESTAndSubgroup` with the runner's recorded `curl`
version, exercising standard and subgroup routes, unchanged archive bytes,
permissions, and malformed requests. Focused local checks:

```sh
ARTIFACTWEAVE_REQUIRE_CURL=1 go test ./internal/server -run '^TestAccVCSRemoteRESTAndSubgroup$' -count=1 -v
go test ./internal/server/vcs -count=1
```
