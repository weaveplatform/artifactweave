# Alpine repository client and qualification

Phase 9c issue [#757](https://github.com/weaveplatform/artifactweave/issues/757)
implements native APK v2 repositories for the inventory's local, remote,
virtual and one-node federated classes. The current source contract is the
[JFrog Alpine repository guide](https://docs.jfrog.com/artifactory/docs/alpine-linux-repositories)
and the [versioned class matrix](../implementation/package-format-matrix.md),
reviewed 2026-10-24. Cross-peer federation remains in P13.

## Connect apk

Create a local or federated repository with `packageType: alpine` and an RSA
`primaryKeyPairRef`. A virtual repository lists Alpine members in resolution
order and supplies its own RSA `keyPair`. The repository index lives at
`<branch>/<component>/<arch>/APKINDEX.tar.gz`. For branch `v3.20` and component
`main`, apk's repository URL ends before the architecture:

```text
https://<host>/artifactory/<repo>/v3.20/main
```

Download the repository's public key from
`GET /artifactory/api/security/keypair/public/repositories/<repo>` and install
it as `<key-alias>.rsa.pub` in apk's trusted key directory. The endpoint
returns the configured primary key for local and federated repositories and
the `keyPair` for an Alpine virtual repository. For a remote repository, trust
the upstream package signer; the remote repository has no local signing key.
If a virtual repository includes remote packages, trust those upstream package
keys as well as the virtual index key. Use an authorized bearer token or Basic
credentials over HTTPS for private repository reads; keep credentials outside
shared repository files where possible. Then add the URL above to apk's
repositories file and run `apk update` and `apk add <package>`.

Deploy an APK v2 package to
`PUT /artifactory/<repo>/<branch>/<component>/<arch>/<name>-<version>.apk`.
The server checks its control and data streams, `.PKGINFO` identity,
compressed-data SHA-256 and per-file hashes before accepting it. Local and
one-node federated indexes are rebuilt asynchronously and signed with the
repository's RSA pair. `POST /artifactory/api/alpine/<repo>/reindex` queues a
full local index rebuild. Delete and trash restore queue the same metadata
reconciliation. A remote repository validates upstream index and package
structure before caching; virtual indexes filter unreadable member packages,
merge readable records in member order, and sign the combined index. A caller
with index-only permission cannot read an index that lists packages they may
not fetch.

## Evidence

[`@alpine` acceptance](../../test/acceptance/features/alpine.feature) uses a
pinned Alpine 3.24 image with apk-tools 3.0.8 and a fresh installation root.
The container runs as the host user, so `apk --initdb` uses `--usermode` to
create its database without root privileges; signature checks remain enabled.
It proves real `apk add` through local, remote, virtual and one-node federated
repositories; first-member selection for duplicate packages, remote members
through a signed virtual index, offline cache reuse, same-origin redirects, index signing,
delete/restore/reindex, malformed local and upstream bytes, and narrow read
and deploy permissions. The controlled APK fixture follows Alpine's
[APK v2 specification](https://wiki.alpinelinux.org/wiki/Apk_spec): signed
control stream, final data stream with explicit directories and per-file
checksums, and an advertised data hash. Keep these client requirements in the
fixture even when they reveal a server validation gap.
