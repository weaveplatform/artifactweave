# RPM remote metadata download policy (#1635)

Reviewed 2026-11-18; baseline main `d9c304f54218fa7816dc23b4b4aab53c5c7ca681`,
with runtime qualification integrating main `9af4ece680cd1d28295e15d391d37e4014579aa4`.
Later main integrations add VCS acceptance/recipe changes (`3a42a0d47`) and a
Bower-only fix (`38e8e4aec`). Neither changes the counted RPM/itemkit code, the
remote Source identity or dependency pins; focused RPM/consistency checks are
refreshed after integration.
No Artifactory trial or live failure capture was used.

## Primary evidence

The current [RPM guide](https://docs.jfrog.com/artifactory/docs/rpm-repositories#rpm-remote-repositories)
describes remote RPM support without special repository configuration and points
YUM/DNF at the directory containing `repodata/`. It documents no exemption from
remote download patterns for those files. The current
[remote repository settings](https://docs.jfrog.com/artifactory/docs/remote-repositories)
say include/exclude patterns filter artifact queries, including administrator
requests. The [patterns KB](https://jfrog.com/help/r/how-to-use-include-exclude-patterns)
(last modified 2025-05-15) explains that an exclusion prevents the corresponding
remote query. Searches of the public KB/Jira found no RPM-specific exception.

The verified official
[7.161.26 OSS source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
has SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`.
Under `backend/core/src/main/java/org/artifactory/repo/`:

- `RemoteRepoBase.getInfo`, lines 416–435, derives the repository path from the
  requested resource before checking its download status:

  ```java
  String path = context.getResourcePath();
  RepoResource repoResource = checkResourceStatusBeforeDownload(context, repoPath);
  ```

- `RealRepoBase.checkResourceStatusBeforeDownload`, lines 193–205, invokes the
  download-allowed check before retrieval. Its selector, lines 156–162, has a
  Maven/non-unique-snapshot distinction, with no RPM exception.
- `RemoteRepoBase.checkDownloadIsAllowed`, lines 744–758, applies:

  ```java
  BasicStatusHolder status = assertValidPath(repoPath, true);
  ```

  It then delegates Read authorization to the cache repository. The path remains
  the requested file.
- `RealRepoBase.assertValidPath`, lines 126–145, uses that path for blackout,
  release/snapshot policy and include/exclude errors. The download exclusion
  status is 404.

This settles the file-policy boundary. **[I]:** exact modern message/envelope
parity is still a comparison limit. The existing
[#216 policy comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6804860833)
asks for RPM cache-alias messages in **F-1**, metadata-excluded member discovery in
**F-3**, and hosted/virtual documents in **RPM 3**. Those are comparison requests,
not live vendor observations; F-1 records an older cache-alias naming answer.
They do not specifically cover this direct remote file response or its independent
initial folder Read scope. No matching existing comparison entry for that Read
scope was verified; a local comparison draft records the remaining question.
The implementation retains the existing scope and does not redefine metadata
authorization.

## Implementation and retained boundaries

`RPMSourceRequest` sets `PolicyPath` to the requested `repodata/` file while
retaining `PermissionFolder` and `PermissionPath`. The source's initial folder
Read authorization and the later cached-file Read check remain separate. A reader
with only `pool/**` still receives the existing exact cache-file Read 403 for a
metadata file, even after an administrator warms it.

Virtual package discovery reads repodata to find the requested package; it is not
a download of the metadata document. Its existing `download` intent now reaches
the source request as `BlackoutOnly`. Direct metadata and virtual listings retain
full file policy. The virtual walk and final package delivery still check the
package's own patterns and current permissions. The initial file-policy change
exposed this distinction by failing the existing package-path regression; the
repair and actual SQLite/PostgreSQL runtimes cover storage enabled and disabled.
No shared source minting, cache admission or publication guard was changed.

## Other metadata callers

The audit inspected the other `PermissionFolder`/`PermissionRoot` source builders,
including explicit `PolicyPath`/`BlackoutOnly` bindings and separate format route
checks. These flags also represent genuine directory discovery, native registry
queries and internal package lookups; their presence alone is not a defect.

The closest raw-file cases were exercised through the assembled server, twice:

| Kind | Requested metadata | Direct and cache alias |
|---|---|---|
| Alpine | `main/x86_64/APKINDEX.tar.gz` | Exact-file blackout/exclusion 404, no origin call |
| CRAN | `src/contrib/PACKAGES.gz` | Same |
| Opkg | `feed/Packages.gz` | Same |
| Debian | `dists/stable/main/binary-amd64/Packages.gz` | Same |

Their format overlays/prechecks or source overrides already protect the actual
file boundary. No additional cross-kind defect was established, so no new issue
was filed. This is separate from #1251's intended computed-list behavior, #1291's
Terraform registry/cache Read identity question, and #1670's trusted policy path
through cold publication.

## Verification scope

Permanent tests cover root and nested metadata, `repomd.xml` and a hashed primary,
GET/HEAD, direct/cache-alias names and suffixes, no upstream query on refusal,
accepted RPM bytes and unchanged narrow-reader refusal. Actual application
runtimes select SQLite or PostgreSQL scratch schemas explicitly.

The public adapter contract pins its initial folder scope independently from
HTTP delivery. Existing RPM virtual package-path tests plus real runtime cases
verify lookup-only metadata exclusions, actual package exclusions and blackout.
HTTP-client evidence is distinct from DNF installer qualification: CI's pinned
`@rpm` scenarios cover remote/cache and mixed virtual installs; local curl or Go
HTTP checks do not stand in for that installer run. The exclusion-specific
virtual discovery regression has explicit real-runtime tests here.
