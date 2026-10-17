# artifactweave

An open source, self-hosted universal artifact repository written in Go, built to
replace JFrog Artifactory for teams that want one place to store, proxy and distribute
packages and container images.

- **Local, remote and virtual repositories** for Go modules, Docker/OCI, npm, Maven,
  PyPI, NuGet, Helm and Terraform providers.
- **One static binary** with pluggable blob storage (local disk, S3-compatible object
  storage) and metadata in SQLite or PostgreSQL.
- **Wire-compatible with Artifactory.** The JFrog CLI, the `jfrog/artifactory`,
  `jfrog/platform` and `jfrog/project` Terraform providers and the native package clients
  work unchanged. The REST, Access and Event APIs mirror JFrog's published definitions.
- **Enterprise features without licence tiers.** Federated repositories kept in step
  across instances, Distribution of signed release bundles to edge instances, cold-storage
  archiving to a cheaper blob tier with search and restore, Access Federation and topology
  across instances, and TypeScript Workers on platform events. Multi-instance features
  work between artifactweave instances.
- **GitOps first.** Repositories, access policies and retention rules are declared as
  code, in the Artifactory YAML descriptor format or through Terraform, with no ClickOps
  and no licence tiers.

## Status

Pre-release. The foundational research and the implementation plan are complete; the code
is being built phase by phase. See [docs/implementation/plan.md](docs/implementation/plan.md)
for the build status and [docs/research/](docs/research/README.md) for the research that
the design rests on.

## Documentation

| Path | What |
|---|---|
| [docs/research/](docs/research/README.md) | Endpoint inventories, package protocols, Terraform and CLI compatibility, scope tiers, reference materials, open questions |
| [docs/research/decisions/](docs/research/decisions/README.md) | Decision records |
| [docs/implementation/](docs/implementation/plan.md) | Phases, milestones, definition of done, repository layout |
| [docs/operations/](docs/operations/configuration-reference.md) | Configuration, reverse proxy, backup and restore |
| [docs/security/threat-model.md](docs/security/threat-model.md) | Threat model |

## Development

| Target | What |
|---|---|
| `make gate` | Everything CI runs: vet, lint, test, cover, vuln, build |
| `make test` | Unit tests with `-race -shuffle=on`, coverage to `cover/unit` |
| `make accept` | Acceptance features against the real binary (needs Docker) |
| `make cover` | Merges every `cover/*` directory and enforces `.testcoverage.yml` |
| `make generate` | Regenerates the API servers and clients from `api/*.openapi.yaml` |
| `make build` | Cross-compiles `artifactweave` for six platforms, CGO disabled |

The quality gate requires 95% merged coverage and 90% per package, blocking lint with
`.golangci.yml`, govulncheck and a cross-compile. Commits and PR titles follow Conventional
Commits; release-please derives versions and the changelog.

## Licence

MIT. JFrog's API definitions are proprietary and are cited, not vendored; this project
implements the documented wire behaviour independently.
