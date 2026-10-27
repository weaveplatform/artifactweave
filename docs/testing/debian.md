# Debian repository client and qualification

Phase 9c issue [#758](https://github.com/weaveplatform/artifactweave/issues/758)
implements apt metadata and package delivery for local, remote, virtual and
one-node federated Debian repositories. The source contracts are the
[JFrog Debian guide](https://docs.jfrog.com/artifactory/docs/debian-repositories),
[snapshot API](https://docs.jfrog.com/artifactory/reference/createdebiansnapshot)
and [versioned class matrix](../implementation/package-format-matrix.md),
reviewed 2026-10-24. Cross-peer convergence remains P13.

## Connect apt

Create a local or federated repository with `packageType: debian` and a GPG
`primaryKeyPairRef`. A virtual repository lists Debian members in resolution
order and supplies its own GPG `keyPair`. For a remote repository, trust the
upstream Release signer. Use the repository URL before `dists`:

```text
deb [signed-by=/etc/apt/keyrings/artifactweave.asc] https://<host>/artifactory/<repo> stable main
```

Download the public key for a local, federated or virtual repository from
`GET /artifactory/api/security/keypair/public/repositories/<repo>` and save it
at the `signed-by` path. When a virtual repository includes a remote member,
the virtual key signs its combined Release metadata. For private repositories,
give apt credentials in a root-owned `auth.conf` file for the registry host.
Then run `apt-get update` and `apt-get install <package>`.

Publish a `.deb` or `.ddeb` archive to a local or federated repository with
matrix coordinates for every intended distribution, component and
architecture. For example:

```text
PUT /artifactory/<repo>/pool/main/a/alpha/alpha_1.0_amd64.deb;deb.distribution=stable;deb.component=main;deb.architecture=amd64
```

The server checks the dpkg control and data archive, file name and declared
coordinates, then builds Packages, Packages.gz, Release, InRelease,
Release.gpg and SHA-256 by-hash metadata. The repository's Release signer
binds the index checksum; apt checks both the Release signature and package
checksum. `POST /artifactory/api/deb/reindex/<repo>` rebuilds a local index.
Deleting or restoring a package also reconciles metadata. A remote repository
validates Release and index structure, package bytes and cache freshness;
plain, gzip, xz, bzip2 and zstd index encodings are accepted. A virtual
repository merges authorized package records in member order and signs its
own view.

For source packages, deploy the `.dsc` with `dsc.distribution` and
`dsc.component`, plus every archive named by that descriptor at the same
directory. The source only appears in `Sources` once its parts and checksums
are complete. Use `deb-src` with the same repository URL and run
`apt-get source --download-only <package>`. Source packages are served through
local and remote repositories; the JFrog guide excludes virtual source
repositories. The deprecated trivial layout does not support sources or
virtual composition.

## Snapshots

Create a local Debian target repository, then call:

```text
POST /artifactory/api/deb/<source-repo>/snapshot?async=0
{"tag":"first","targetRepo":"<target-repo>","distribution":"stable"}
```

`async=1` queues durable work; `async=0` or omission waits for completion.
The caller needs Read on the source metadata and packages and Write/Manage on
the target snapshot path. The server copies checksum-verified metadata only,
stores `deb.snapshot.source` on the snapshot folder, and rejects reuse of a
completed tag. Point apt at the snapshot root:

```text
deb [signed-by=/etc/apt/keyrings/artifactweave.asc] https://<host>/artifactory/api/deb/<target-repo>/snapshots/first stable main
```

The snapshot retains its signed metadata and redirects package downloads to
the source repository. Keep the source package bytes available: deleting or
overwriting them can make a snapshot package download fail apt's checksum
verification. A local or virtual snapshot uses that repository's signing key;
a remote snapshot retains the upstream signing key.

## Evidence

[`@debian` acceptance](../../test/acceptance/features/debian.feature) uses
the pinned Ubuntu 26.10 client image with apt 3.3.3 and genuine `dpkg-deb`
binary and source fixtures. It proves fresh apt installs through local,
remote, virtual and one-node federated repositories; ordered virtual member
selection; remote refresh and offline cache reuse; source download through
local and remote; malformed metadata and archive rejection; reindex;
snapshot creation, duplicate-tag refusal, async work, real apt snapshot
install and remote/virtual snapshot resolution. A fixture mismatch is
investigated against apt's actual expected wire format before changing the
fixture.
