# artifactweave

An open source, self-hosted artifact repository written in Go, implementing
Artifactory-compatible APIs for packages and container images.

- **Local repositories** for Docker/OCI, npm, Go modules, Maven, PyPI, NuGet, classic
  and OCI Helm, and Terraform modules and providers. Terraform state uses a separate
  `terraformbackend` repository type with the native `remote` backend.
- **One static binary** with metadata in SQLite or PostgreSQL and blobs on local disk,
  S3-compatible storage, Google Cloud Storage, Azure Blob Storage or mirrored backends.
- **Native clients and automation.** Acceptance tests exercise the JFrog CLI, native
  package clients and the implemented resources of JFrog's Terraform providers.
- **Repository management and security.** REST and supported YAML descriptor sections
  manage repositories; users, groups, permission targets, access/reference tokens and OIDC
  exchange control access. AQL, searches, build-info, trash restore and reindex are implemented.
- **Cloud deployment and delivery.** Terraform references cover AWS, GCP, Azure and
  multicloud blob mirroring, with signed download redirects and CDN delivery.

## Status

**Phases P0–P8 are complete as of 2026-10-21**, including the cloud-storage and deployment
phases P2b and P4b. The P9 remote feature set is implemented: shared transport,
security and cache controls; generic and native-format retrieval; cache maintenance;
Smart Remote; permission-safe browsing and search; NuGet v2, symbols and cached-package
reindex; and transitive AQL. The [complete public run](https://github.com/weaveplatform/artifactweave/actions/runs/39807833314)
passed all 15 families on merged main. **P9 is complete:** [#708](https://github.com/weaveplatform/artifactweave/pull/708)
merged its sign-off record, and the [merged-main full gate](https://github.com/weaveplatform/artifactweave/actions/runs/39810238539)
passed. This status describes the code on
`main`; published releases reflect their own tags. The [implementation plan](docs/implementation/plan.md)
records client evidence and remaining scope.
P9b registered five package types omitted from the original inventory:
LuaRocks, NVIDIA NIM, Skills, Agent Plugins and Agent Packages. LuaRocks
local, remote, virtual and federated handling, Skills local publishing and
native installs, Agent Plugins local publishing and client installs, and Agent
Packages APM v1 local/federated handling and NVIDIA NIM's documented remote
model API with a separate Docker remote are implemented. P9b qualification
evidence is tracked in [#693](https://github.com/weaveplatform/artifactweave/issues/693). See the
[P9b contract report](docs/research/33-phase9b-formats.md) and
[qualification ledger](docs/testing/phase9b-signoff.md).
P9c has started from the [versioned package-format matrix](docs/implementation/package-format-matrix.md).
Alpine APK v2, Debian apt binary/source metadata and snapshots, and RPM
repodata are implemented across local, remote, virtual and one-node federated
repositories under [#757](https://github.com/weaveplatform/artifactweave/issues/757),
[#758](https://github.com/weaveplatform/artifactweave/issues/758) and
[#759](https://github.com/weaveplatform/artifactweave/issues/759). Real `apk`,
`apt` and `dnf` clients verify those paths; see the [Alpine](docs/testing/alpine.md),
[Debian](docs/testing/debian.md) and [RPM](docs/testing/rpm.md) client guides.
The remaining P9c formats and combined qualification gate are tracked by the
linked tickets.

| Capability | Current implementation |
|---|---|
| Local package protocols | Implemented for the eight format families above; NuGet includes v2/v3 and PDB symbol serving, and Terraform includes signed provider downloads and the state backend |
| Generic remote proxying | Implemented: authorized HTTP(S) cold fetch, validated cache, cache aliases, ranges/conditions and signed delivery; [operations guide](docs/operations/remote-repositories.md) |
| Remote cache maintenance | Implemented: metadata-only zap, atomic deletion/restore fencing and persistent unused-cache cleanup; [maintenance guide](docs/implementation/remote-cache-maintenance.md) |
| npm remote retrieval | Implemented: full/abbreviated metadata, version documents, dist-tags, filtered search and validated scoped/unscoped tarballs; [operations guide](docs/operations/npm-remote.md) |
| Maven-family remote retrieval | Implemented: native Maven cold/outage proof, authoritative metadata, checksum/admission policies, layouts and durable companions; [operations and client gaps](docs/operations/maven-remote-repositories.md) |
| Upstream version search | Implemented: authoritative Maven metadata and bounded layout listings, current visibility, cache-only aliases and specific GAVC; [operations guide](docs/operations/upstream-search.md) |
| Go upstream source | Implemented below HTTP routing; [source contract and P10 native virtual handoff](docs/implementation/go-upstream-source.md) |
| Python remote retrieval | Implemented: HTML/JSON indexes, Warehouse metadata, validated wheels/sdists, metadata sidecars and advertised signatures; native pip/uv cold and offline installs; [operations guide](docs/operations/pypi-remote.md) |
| Docker/OCI/Helm OCI remote retrieval | Implemented: authenticated manifests/indexes, verified blobs, tags/referrers, foreign-layer policy and offline cache reads; [operations guide](docs/operations/registry-remote.md) |
| NuGet remote retrieval | Implemented: v3 resources and signed packages, bounded v2 feeds with custom contexts, upstream PDB GET/HEAD/range, remote prerelease retention and queue-backed reindex of cached packages; [operations guide](docs/operations/nuget-remote.md) |
| Classic Helm remote retrieval | Implemented: authoritative upstream indexes, constrained rewritten chart URLs, validated archives and preserved provenance; [operations guide](docs/operations/helm-remote.md) |
| Terraform module remote retrieval | Implemented: authoritative versions, validated original archives, resolved subdirectories and credential-bound signed downloads; [operations guide](docs/operations/terraform-remote-modules.md) |
| Smart Remote synchronization | Implemented: authenticated discovery, initial/TTL user properties, bounded durable download statistics and confirmed origin absence; [operations and qualification limits](docs/operations/smart-remote.md) |
| Remote folder browsing | Implemented: opt-in, bounded HTML and Smart Remote folder listings, lossless differing-layout discovery, current canonical permission filtering and no artifact prefetch; [operations guide](docs/operations/remote-repositories.md#remote-folder-browsing) |
| Terraform provider remote retrieval | Implemented: upstream registry signatures, complete package bundles, controlled mirrors and offline reuse; [operations guide](docs/operations/terraform-remote-providers.md) |
| Smart Remote transitive AQL | Implemented: bounded direct/virtual `items.find(...).transitive()`, current permission filtering and transient normal-key discovery; [native CLI usage and limits](docs/operations/transitive-aql.md) |
| P9 exit and virtual resolution | The full public-upstream run passed all 15 families and the merged-main full gate passed after #708. P10 owns virtual package resolution, including native Go virtual proof; [P9 sign-off ledger](docs/testing/phase9-signoff.md) |
| Newly identified Artifactory package types | P9b registers LuaRocks, NVIDIA NIM, Skills, Agent Plugins and Agent Packages for their documented repository classes. LuaRocks local/remote/virtual/federated, Skills local publish/discovery/install, Agent Plugins local publish/install/update, Agent Packages APM v1 local/federated, and NVIDIA NIM remote metadata/model reads with a paired Docker pull are implemented. See the [class/client matrix](docs/testing/phase9b-signoff.md) and [qualification evidence](https://github.com/weaveplatform/artifactweave/issues/693) |
| P9c native package formats | Alpine APK v2, Debian apt and RPM repodata have native local, remote, virtual and one-node federated proof with real `apk`, `apt` and `dnf` clients. Debian also has binary/source metadata and snapshot API/client proof. The [versioned format matrix](docs/implementation/package-format-matrix.md) tracks remaining gaps; see the [Alpine](docs/testing/alpine.md), [Debian](docs/testing/debian.md) and [RPM](docs/testing/rpm.md) client guides |
| Project workflows, webhooks, cleanup policies and replication | Planned for P11; some supporting models and APIs already exist |
| Full system descriptor and LDAP | Planned for P12; the currently applied descriptor sections are listed in the configuration reference |
| Peer federation, Access Federation, Distribution, cold-storage workflows and Workers | Planned for P13–P17 |
| Initial hardening and live Artifactory comparison | Planned for P18 and P19; inferred compatibility details remain tracked in [#216](https://github.com/weaveplatform/artifactweave/issues/216) |
| Enterprise authentication and SCIM | Planned for P21/P22, including SAML, human OIDC/OAuth, Crowd, HTTP-SSO and browser approval |
| Evidence, Lifecycle/release bundles v2, AppTrust and security scanning | Planned for P23–P27, including Xray, Catalog, Advanced Security and Curation |
| Remaining protocol gaps and additional package ecosystems | Planned for P28–P33; the original 28 additional repository package types have owners, with five newly identified types assigned to P9b |
| MySQL, HA, migration/recovery, advanced replication and operations integrations | Planned for P34–P38 |
| Web console and estate/hosted-service integrations | Planned for P39/P40; the expanded platform is qualified in P41 |

The [capability roadmap](docs/implementation/capability-roadmap.md) keeps every identified
capability in scope unless a newer capability supersedes it, with a documented replacement
and migration path. P20 adds operation-level coverage tracking. A current 404/501 or a
missing client test is a delivery gap with an owner, not a permanent exclusion.

The [P8 exit gate](https://github.com/weaveplatform/artifactweave/actions/runs/39595603167)
passed with tests on Linux, macOS and Windows and 97.1% merged coverage. The Linux native
acceptance job completed in 755 seconds within its 900-second budget. The plan records
client-specific limits and deferred features, including browser approval login and npm
package signatures. API schemas also cover later phases; a published schema is not a
claim of runtime support.

## Documentation

| Path | What |
|---|---|
| [docs/research/](docs/research/README.md) | Endpoint inventories, package protocols, Terraform and CLI compatibility, scope tiers, reference materials, open questions |
| [docs/research/decisions/](docs/research/decisions/README.md) | Decision records |
| [docs/implementation/](docs/implementation/plan.md) | Phases, milestones, definition of done, repository layout |
| [Capability roadmap](docs/implementation/capability-roadmap.md) | Scope ownership, remaining gaps, supersession evidence and P20–P41 commitments |
| [docs/operations/](docs/operations/configuration-reference.md) | Configuration, reverse proxy, backup and restore |
| [docs/security/threat-model.md](docs/security/threat-model.md) | Threat model |

## Development

| Target | What |
|---|---|
| `make gate` | Local aggregate: vet, lint, test, cover, vuln, build; CI also runs the acceptance and integration jobs below |
| `make test` | Unit tests with `-race -shuffle=on`, coverage to `cover/unit` |
| `make accept` | Acceptance features against the real binary (needs Docker) |
| `make terraform TERRAFORM_MODE=apply` | Applies, plans clean and destroys the JFrog provider fixtures |
| `make oci-conformance` | OCI distribution-spec conformance checks |
| `make cover` | Merges every `cover/*` directory and enforces `.testcoverage.yml` |
| `make generate` | Regenerates the API servers and clients from `api/*.openapi.yaml` |
| `make verify-generate` | Fails if regeneration changes committed generated code |
| `make build` | Cross-compiles `artifactweave` for six platforms, CGO disabled |

The quality gate requires 95% merged coverage and 90% per package, blocking lint with
`.golangci.yml`, govulncheck and a cross-compile. Commits and PR titles follow Conventional
Commits; release-please derives versions and the changelog.

Client pins, integration prerequisites, deployment checks and coverage inputs are documented
in [the CI guide](docs/testing/ci.md).

## Releases

Merging the release pull request that release-please keeps open tags the version and
creates the GitHub release. The `release` workflow then builds `artifactweave` for Linux,
macOS and Windows on amd64 and arm64 with [GoReleaser](.goreleaser.yaml) and attaches:

- an archive per platform, holding the static binary, this README and the licence;
- `artifactweave_<version>_checksums.txt`, the SHA-256 of every archive and SBOM;
- an SPDX SBOM per archive;
- `artifactweave_<version>_checksums.txt.sigstore.json`, a keyless cosign signature over the
  checksum file, bound to this repository's release workflow.

Verify a download, for example `v0.1.0`:

```console
cosign verify-blob \
  --bundle artifactweave_0.1.0_checksums.txt.sigstore.json \
  --certificate-identity 'https://github.com/weaveplatform/artifactweave/.github/workflows/release.yml@refs/tags/v0.1.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  artifactweave_0.1.0_checksums.txt

shasum -a 256 -c artifactweave_0.1.0_checksums.txt --ignore-missing
```

The first command proves the checksum file came from this repository's release workflow
for that tag; the second proves each archive you downloaded matches it.

The same release publishes the container image `ghcr.io/weaveplatform/artifactweave` for
linux/amd64 and linux/arm64, tagged with the version (and, for releases that are not
pre-releases, `<major>.<minor>` and `latest`). It is built from the same binaries on a
distroless base, runs as a non-root user with `/data` as its working directory and port
8081, and carries an SBOM and a provenance attestation. Verify it:

```console
cosign verify ghcr.io/weaveplatform/artifactweave:0.1.0 \
  --certificate-identity 'https://github.com/weaveplatform/artifactweave/.github/workflows/release.yml@refs/tags/v0.1.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker run --rm -p 8081:8081 -v artifactweave-data:/data \
  -v "$PWD/artifactweave.yaml:/etc/artifactweave/artifactweave.yaml:ro" \
  ghcr.io/weaveplatform/artifactweave:0.1.0
```

## Licence

MIT. JFrog's API definitions are proprietary and are cited, not vendored; this project
implements the documented wire behaviour independently.
