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
phases P2b and P4b. This status describes `main`; published release artifacts reflect the
code at their release tag. The [implementation plan](docs/implementation/plan.md) records
the completed work, client evidence and remaining scope. P9 is in progress: generic
HTTP remote retrieval, its shared transport/security/cache foundation and remote cache
maintenance, npm, Maven-family and Python remote retrieval are implemented.

| Capability | Current implementation |
|---|---|
| Local package protocols | Implemented for the eight format families above; NuGet includes v2/v3 and PDB symbol serving, and Terraform includes signed provider downloads and the state backend |
| Generic remote proxying | Implemented: authorized HTTP(S) cold fetch, validated cache, cache aliases, ranges/conditions and signed delivery; [operations guide](docs/operations/remote-repositories.md) |
| Remote cache maintenance | Implemented: metadata-only zap, atomic deletion/restore fencing and persistent unused-cache cleanup; [maintenance guide](docs/implementation/remote-cache-maintenance.md) |
| npm remote retrieval | Implemented: full/abbreviated metadata, version documents, dist-tags, filtered search and validated scoped/unscoped tarballs; [operations guide](docs/operations/npm-remote.md) |
| Maven-family remote retrieval | Implemented: native Maven cold/outage proof, authoritative metadata, checksum/admission policies, layouts and durable companions; [operations and client gaps](docs/operations/maven-remote-repositories.md) |
| Go upstream source | Implemented below HTTP routing; [source contract and P10 native virtual handoff](docs/implementation/go-upstream-source.md) |
| Python remote retrieval | Implemented: HTML/JSON indexes, Warehouse metadata, validated wheels/sdists, metadata sidecars and advertised signatures; native pip/uv cold and offline installs; [operations guide](docs/operations/pypi-remote.md) |
| Docker/OCI/Helm OCI remote retrieval | Implemented: authenticated manifests/indexes, verified blobs, tags/referrers, foreign-layer policy and offline cache reads; [operations guide](docs/operations/registry-remote.md) |
| NuGet v3 remote retrieval | Implemented: discovery, flat container, registration, filtered search/autocomplete, repository-signature resources and validated signed package bytes; [operations guide](docs/operations/nuget-remote.md). V2/symbol continuation remains #629 |
| Classic Helm remote retrieval | Implemented: authoritative upstream indexes, constrained rewritten chart URLs, validated archives and preserved provenance; [operations guide](docs/operations/helm-remote.md) |
| Other native-format remote adapters and virtual package resolution | Remaining P9 adapter tickets and P10; shared infrastructure and repository schemas do not enable these content paths by themselves |
| Project workflows, webhooks, cleanup policies and replication | Planned for P11; some supporting models and APIs already exist |
| Full system descriptor and LDAP | Planned for P12; the currently applied descriptor sections are listed in the configuration reference |
| Peer federation, Access Federation, Distribution, cold-storage workflows and Workers | Planned for P13–P17 |
| Initial hardening and live Artifactory comparison | Planned for P18 and P19; inferred compatibility details remain tracked in [#216](https://github.com/weaveplatform/artifactweave/issues/216) |
| Enterprise authentication and SCIM | Planned for P21/P22, including SAML, human OIDC/OAuth, Crowd, HTTP-SSO and browser approval |
| Evidence, Lifecycle/release bundles v2, AppTrust and security scanning | Planned for P23–P27, including Xray, Catalog, Advanced Security and Curation |
| Remaining protocol gaps and additional package ecosystems | Planned for P28–P33; all 28 additional repository package types have owners |
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
