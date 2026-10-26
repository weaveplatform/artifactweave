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

The pinned acceptance client is Git LFS 3.8.1. Its image verifies upstream's
published archive SHA-256. The [native fixture](../../test/acceptance/features/gitlfs.feature)
tests publish and fresh clone through local, federated and virtual repositories,
plus remote cold and offline fetches. [File locking](https://github.com/weaveplatform/artifactweave/issues/843)
has its own follow-up and is not yet claimed as complete. The current
[JFrog Git LFS guide](https://docs.jfrog.com/artifactory/docs/git-lfs-repositories)
was checked on 2026-10-26; live target differences are tracked by
[#216](https://github.com/weaveplatform/artifactweave/issues/216).
