# RPM repository client and qualification

RPM repositories support local, remote, virtual and one-node federated classes
in the [versioned format matrix](../implementation/package-format-matrix.md).
The source contract is the [JFrog RPM repository guide](https://docs.jfrog.com/artifactory/docs/rpm-repositories)
and [Yum metadata API](https://docs.jfrog.com/artifactory/reference/calculateyumrepositorymetadata),
reviewed 2026-10-24. Cross-peer federation remains in P13.

## Connect dnf or yum

Create a repository with `packageType: rpm`. Set `calculateYumMetadata: true`
on a local or federated repository for automatic repodata generation; with it
disabled, run `POST /artifactory/api/yum/<repo>` after uploads and changes.
For a repository rooted at the top level, point the client to:

```text
https://<host>/artifactory/<repo>
```

For a nested layout, configure the client URL at the directory whose child is
`repodata/`; `yumRootDepth` controls that directory level. Upload a package
with its real NEVRA filename, for example:

```text
PUT /artifactory/<repo>/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm
```

The server checks the RPM header, package identity, path, size and SHA-256
before admitting local content or caching remote content. Local and federated
repodata is rebuilt after package changes when automatic calculation is on.
Remote metadata accepts checksum-addressed gzip and zstd primary indexes,
including the zstd output of Fedora 42 `createrepo_c`.
On virtual repositories, the Yum API also accepts
`?path=<directory>/repodata` to validate a particular root. Local and
federated repositories reject that query parameter. Delete and trash restore
reconcile hosted indexes. Virtual repositories compose readable member
metadata in configured order and serve packages from the selected member;
their view is generated from current member metadata.

Use `primaryKeyPairRef` with a GPG keypair to sign local or federated
`repomd.xml`. A virtual repository uses its `keyPair`. Download the public key
from `GET /artifactory/api/security/keypair/public/repositories/<repo>` and set
the client's `repo_gpgcheck=1` and `gpgkey` to that key. Configure package
signature checking separately: repository metadata signatures do not sign the
RPM package itself. For private repositories, use an authorized credential
over HTTPS and avoid placing passwords in shared `.repo` files.

## Evidence

[`@rpm` acceptance](../../test/acceptance/features/rpm.feature) uses a pinned
Fedora 42 image with `dnf5` 5.2.18.0 and a genuine `rpmbuild` package. It installs
through local, remote, virtual and one-node federated repositories, verifies
GPG-signed local and virtual metadata, tests offline cache, metadata refresh
after zap and same-origin redirects, manual Yum calculation, lifecycle rebuilds, malformed metadata,
invalid package paths, corrupt upstream packages and narrow read permissions.
Remote scenarios use an independent [createrepo_c fixture](../../test/acceptance/testdata/rpm-upstream/README.md)
whose primary index is zstd-compressed; the local encoder does not create that
fixture.
The fixture is kept valid for the client even when a real-client run exposes
a server bug.
