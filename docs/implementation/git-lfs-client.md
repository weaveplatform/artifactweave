# Connect Git LFS to artifactweave

The Git LFS endpoint is `/artifactory/api/lfs/{repo-key}`. Configure it in a
Git working tree, independently of the Git remote:

```sh
git lfs install
git lfs track '*.bin'
git config -f .lfsconfig lfs.url "$ARTIFACTORY_URL/artifactory/api/lfs/$LFS_REPO"
git add .gitattributes .lfsconfig
git lfs env
```

Use the Git credential helper to provide a user or access token with artifact
read or write permission on the repository. `git lfs push origin HEAD` sends
the batch request and raw SHA-256 objects to a local or one-node federated
repository. `git lfs fetch origin HEAD` and a fresh Git clone retrieve them.
A virtual repository reads its configured members in order and sends uploads
to `defaultDeploymentRepo`; a remote repository caches verified upstream
objects and is read-only to Git LFS clients.

The server stores an object as
`objects/{first-two-OID-characters}/{next-two}/{64-character-sha256-OID}`.
The batch request names each object's exact byte size. The server verifies
both size and SHA-256 before publishing locally or caching a remote result;
delete and restore immediately change the batch view. Reindex leaves this
content-addressed object tree intact because there is no derived package
index. Remote batch action URLs are validated before the server fetches them,
and a verified cached object is readable while the upstream is unavailable.
If an upstream basic download action supplies authentication headers, the
server sends bounded, safe headers to that validated action URL only. Redirects
cannot carry action headers to another location, and neither the client-facing
batch response nor the cached object contains them.

## Lock files

Mark tracked files as lockable and use the Git LFS commands against the same
configured URL:

```sh
git lfs track --lockable '*.bin'
git add .gitattributes
git lfs lock asset.bin
git lfs locks
git lfs locks --verify
git lfs unlock asset.bin
git config "lfs.${ARTIFACTORY_URL}/artifactory/api/lfs/${LFS_REPO}.locksverify" true
```

Local and one-node federated repositories persist path locks. A virtual
repository lists readable member locks in configured order and creates new
locks in its `defaultDeploymentRepo`. Permission targets grant access on the
**member repositories**, since a virtual repository cannot be a permission
target. A lock needs read permission to appear in a list; creating and
verifying for a push require write permission on the relevant lock path. To
force-unlock another user's lock, the caller needs both write and delete on
that path. Git LFS verifies path conflicts before a push; a raw object upload
only identifies its SHA-256 OID and cannot identify a locked worktree path.
Git LFS 3.8.1 asks users to enable `locksverify` for the repository URL after
it detects locking support. The pinned client test enables it, then confirms
that Bob's push of Alice's locked path fails and the Git remote ref does not
advance.

A remote repository serves a fresh upstream lock list, including through a
virtual member. Lock creation, unlock and push verification are refused on a
remote repository. If the upstream lock service is unavailable, listing fails
rather than returning a stale or empty success response. Virtual lock
aggregation and remote mutation policy are artifactweave choices pending a
live Artifactory capture in [#216](https://github.com/weaveplatform/artifactweave/issues/216).
The pinned Git LFS 3.8.1 client reports this HTTP 502 as a failing plain
`git lfs locks` command. Its `git lfs locks --json` command instead prints
`[]` and exits zero, even though the server returned 502; callers that need
outage detection should use the plain command or inspect the HTTP response.

The pinned acceptance client is Git LFS 3.8.1. Its image verifies upstream's
published archive SHA-256. The [native fixture](../../test/acceptance/features/gitlfs.feature)
tests publish and fresh clone through local, federated and virtual repositories,
remote cold and offline fetches, and two-user locking through local,
federated and virtual repositories. It also proves remote lock-list freshness
and failure when the upstream stops. The current
[JFrog Git LFS guide](https://docs.jfrog.com/artifactory/docs/git-lfs-repositories)
was checked on 2026-10-26; live target differences are tracked by
[#216](https://github.com/weaveplatform/artifactweave/issues/216).
