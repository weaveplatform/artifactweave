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
coordinates. A `.deb` it cannot read as a package is still stored, as JFrog's
"Use Debian Metadata Validation" describes, but left out of every index: its
`deb.index.status` property gives the reason, so a property search or an AQL
query on `deb.index.status` finds it, and a readable package deployed in its
place is indexed and loses the property. The server then builds Packages,
Packages.gz, Release, InRelease,
Release.gpg and SHA-256 by-hash metadata. Set the repository's
`optionalIndexCompressionFormats` to any of `bz2`, `lzma` and `xz` to also
write Packages.bz2, Packages.lzma and Packages.xz; the Release lists each
form with its by-hash copy, and the setting takes effect at the next index
rebuild. A Sources index keeps its plain and gzip forms. The repository's
Release signer binds the index checksum; apt checks both the Release
signature and package checksum. `POST /artifactory/api/deb/reindex/<repo>`
rebuilds a local index: `async=1` queues the rebuild and answers at once, and
`async=0` or none waits for it; `true` and `false` work too, as JFrog's
earlier REST API reference wrote them. Deleting or restoring a package also
reconciles metadata. A remote repository validates Release structure, package
bytes and cache freshness, and caches a Packages or Sources index as its
upstream sent it, whether or not it decodes, as 7.x does until RTFACT-27303
is done; plain, gzip, xz, bzip2, lzma and zstd index encodings are read. An
index short of the size the upstream declared, or of no declared size, still
answers 502. It caches a `.ddeb` whatever `ddebSupported`
says, since that setting governs a local repository's indexing; a local or
federated repository without it refuses a `.ddeb` deploy with 400. It also
asks its upstream for an installer's
Packages index and for any `by-hash/SHA256` file, checked against the
SHA-256 its name gives; a Packages or Sources index's by-hash copy is served
to a caller who may read every package it lists, as the index is. Any other
file under `dists/` that a local repository would derive, such as a
component's Release in a `binary-<arch>` or `source` folder, a
`Packages.diff/Index`, or the Release of a distribution named with a slash
(`dists/buster/updates/Release`), is fetched from the upstream too and
renewed per the Retrieval Cache Period (#1375). The remote's `-cache` alias
serves a Release, an InRelease, an installer index, a Contents index's
by-hash copy and those files once the remote has cached them, without
asking the upstream, and answers 404 "Could not find resource" before
(#1382), as it serves a Packages or Sources index and its by-hash copy.
A virtual repository merges authorized package records in member order and
signs its own view, in plain and gzip forms and the forms its own
`optionalIndexCompressionFormats` names. It leaves out a remote member's
index that does not decode or does not have the digest the member's Release
gives it, as it would a missing one. Any other file under `dists/`, such
as a component Release, a Contents index, a translation, an installer file
or a Sources index, comes from the first member, in member order, that may
serve it and holds it; a remote member asks its upstream. The virtual
repository's Release does not list those files.

A GET of an index, a virtual repository's metadata or a virtual package takes
the query parameters Artifactory documents for any file, such as
`skipUpdateStats`, and matrix parameters, matched against the file's
properties as on any other GET, once the caller's read permission is settled.
A virtual repository's composed metadata carries no properties, so only a
mandatory key (`;key+=value`) refuses it, and a member whose file does not
match is passed over for the next. A remote repository's Release and index
take a query as any remote file does: it reaches the upstream only when the
repository's Propagate Query Params is set. Its index takes matrix
parameters as any remote file does: they go to the upstream after the path,
and a copy the cache already holds is matched against them first (#1371).

For source packages, deploy the `.dsc` with `dsc.distribution` and
`dsc.component`, plus every archive named by that descriptor at the same
directory. The source only appears in `Sources` once its parts and checksums
are complete. Use `deb-src` with the same repository URL and run
`apt-get source --download-only <package>`. Source packages are served through
local and remote repositories; the JFrog guide excludes virtual source
repositories. The deprecated trivial layout does not support sources or
virtual composition.

## Metadata validation

A local or federated repository checks each binary's control data before it
indexes the package, as JFrog's "Use Debian Metadata Validation" describes:
no field may have an empty value, and `Maintainer` and `Description` must be
there. A package that fails is stored, marked with `deb.index.status` and
left out of the index. 7.x turns the check off with its
`debian.metadata.validation` system property; artifactweave's
`debian.metadata_validation` key (`ARTIFACTWEAVE_DEBIAN_METADATA_VALIDATION`,
on by default, see the
[configuration reference](../operations/configuration-reference.md)) does the
same (#1374). With it off, such a package is indexed and its empty fields
are left out of its Packages stanza. A file that is not a readable package,
or whose control data has no valid `Package`, `Version` or `Architecture`,
is still stored, marked and left out, as no index can be read from it. The
setting applies to deploys and index rebuilds after the server restarts with
it; reindex a repository (`POST /artifactory/api/deb/reindex/<repo>`) to
apply it to the packages the repository already holds.

## Snapshots

Create a local Debian target repository, then call:

```text
POST /artifactory/api/deb/<source-repo>/snapshot?async=0
{"tag":"first","targetRepo":"<target-repo>","distribution":"stable"}
```

`async=1` queues durable work; `async=0` or omission waits for completion.
The caller needs Read on the source metadata and packages and Write/Manage on
the target snapshot path. A caller who may read the source's Release but not
a file the snapshot copies, or a package an index lists, gets 403, or 401
when anonymous, and no snapshot is created. The server copies
checksum-verified metadata only, stores `deb.snapshot.source` on the snapshot
folder, and rejects reuse of a completed tag. Point apt at the snapshot root:

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
the [pinned Ubuntu client image with apt](ci-client-images.md) and genuine `dpkg-deb`
binary and source fixtures. It proves fresh apt installs through local,
remote, virtual and one-node federated repositories; ordered virtual member
selection; remote refresh and offline cache reuse; source download through
local and remote; malformed metadata and archive rejection; reindex;
snapshot creation, duplicate-tag refusal, async work, real apt snapshot
install and remote/virtual snapshot resolution. A fixture mismatch is
investigated against apt's actual expected wire format before changing the
fixture.
