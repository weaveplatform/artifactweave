# Opkg repository client and qualification

The native Opkg adapter serves local, remote, virtual and one-node federated
repositories registered as `packageType: opkg`. Cross-peer federation is
tracked separately. The current [JFrog Opkg guide](https://docs.jfrog.com/artifactory/docs/opkg-repositories)
documents local, remote and virtual feeds; its
[class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
still marks virtual Opkg unavailable. The client route here follows the
format guide and the repository's versioned class inventory. This source
difference still needs a target Artifactory observation.

## Connect opkg

An Opkg feed is a folder containing `Packages`, `Packages.gz`, `Release`
and `.ipk` files. For example, the feed URL is:

```text
https://<host>/artifactory/<repo>/feed
```

Add a `src/gz` entry to the client's `opkg.conf` and choose the package
architecture. A private feed needs an authorized token or Basic credentials.
The JFrog guide also documents `option http_auth <user>:<token>`. Keep tokens
out of shared configuration files and use HTTPS.

```text
src/gz artifactweave https://<host>/artifactory/<repo>/feed
arch all 10
```

Run `opkg update` and `opkg install <package>`. Deploy a valid IPK with
`PUT /artifactory/<repo>/feed/<name>_<version>_<arch>.ipk`; the documented
Artifactory route also permits a safe noncanonical `.ipk` filename. A
canonical filename must agree with the IPK's `CONTROL/control` identity.
The adapter accepts OpenWrt's gzip(tar) IPK and legacy ar container, checks
the required archive members and bounded control/data streams, and uses the
stored SHA256 to generate package records.

Local and one-node federated feeds rebuild `Packages`, `Packages.gz` and
`Release` after writes, deletes and restores. The
`POST /artifactory/api/opkg/reindex/<repo>` endpoint rebuilds all feeds;
`async=true` queues work without waiting. A configured GPG key pair signs
the uncompressed `Packages` file as `Packages.sig`. The signature authenticates
the index; the index's SHA256sum fields bind downloaded IPKs.

Remote feeds follow same-origin package redirects through the shared source
policy and validate bounded index and package bodies before caching. A
virtual feed combines readable member records in configured order, selects
the same member for IPK downloads, and excludes packages that the caller
cannot read. Direct index reads require access to every listed package.

## Evidence

[`@opkg` acceptance](../../test/acceptance/features/opkg.feature) uses
opkg 0.10.0 built from the Yocto release tarball pinned by SHA256
in the [client image](../../test/acceptance/clients/opkg.Dockerfile). The
controlled IPKs use OpenWrt 24.10's gzip(tar) layout and are installed by
that client. The remote acceptance case serves an authentic OpenWrt 24.10.0
IPK and published `Packages` records, without using the adapter's index
encoder, and downloads it with the same client. Keep these fixtures valid when
server validation exposes a defect.

The fixture records are the first two entries of the published
[OpenWrt 24.10.0 x86_64/base Packages feed](https://downloads.openwrt.org/releases/24.10.0/packages/x86_64/base/Packages).
The IPK is the corresponding
[464xlat_13_x86_64.ipk](https://downloads.openwrt.org/releases/24.10.0/packages/x86_64/base/464xlat_13_x86_64.ipk),
whose published SHA256 is
`3dcda4329728a2b3961083f49daa74be003827f790dc0c5fe36493e5fbd8b9b4`.
The pinned opkg 0.10.0 build needs an `MD5Sum` record to verify downloads,
so the remote test adds an MD5 computed from that IPK to its published
record. A separate negative case changes a valid IPK and updates the MD5
while preserving the original published SHA256; the proxy rejects it and
does not cache it.
