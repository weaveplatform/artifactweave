# 0009: Configuration as code

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))

## Context

The project is built around GitOps: repositories, access policies and retention rules are
declared as code and managed through Terraform and the API. Artifactory already has a
declarative form, the YAML descriptor accepted by `PATCH /api/system/configuration`
(repositories, proxies, property sets, layouts, backups, security settings). Its bootstrap
file, `artifactory.config.import.yml`, applied once at first start, is a different and
smaller format: a license key, the base URL, proxies and the package types to create
default repositories for ([bootstrap YAML](https://docs.jfrog.com/installation/docs/artifactory-bootstrap-yaml-file.md),
[research 06](../06-system-configuration-and-descriptor.md#semantics)). artifactweave's
`--bootstrap` takes the PATCH descriptor instead (layer 2 below). The repository's own
conventions require strict YAML for the application configuration with flag, environment
and file precedence.

## Decision

Three layers feed one internal configuration:

1. **Application configuration** (`artifactweave.yaml`): listen addresses, base URL,
   database, blob store, token signing keys, master key source, bootstrap admin,
   compatibility version. Strict YAML that rejects unknown keys; flag > environment > file.
   This layer never contains repositories or principals.
2. **The Artifactory YAML descriptor**: the format in
   [06-system-configuration-and-descriptor](../06-system-configuration-and-descriptor.md)
   (`localRepositories`, `remoteRepositories`, `virtualRepositories`, `proxies`,
   `propertySets`, `repoLayouts`, `backups`, `security.*`, `mailServer` and the rest),
   accepted by `PATCH /artifactory/api/system/configuration` with `application/yaml`,
   by `artifactweave config apply <file>` and by `--bootstrap <file>` at start. A `null`
   value deletes an element. Secret values may be written as `${ENV_VAR}` references.
   `artifactweave config apply --dry-run` checks a file against the current state without
   applying it.
3. **The REST and Terraform APIs**, which are the same service layer the descriptor
   calls.

`GET /artifactory/api/system/configuration` renders the XML descriptor from the current
state. Full XML `POST` is deferred.

## Rationale

The Artifactory descriptor is the format Artifactory users already have in version
control and the one the `artifactory_*` configuration resources write through, so adopting
it gives GitOps for free and makes migration a file copy. Keeping it separate from the
application configuration keeps the strict-YAML rule for operational settings while
allowing the descriptor's looser, JFrog-defined schema.

**Alternatives considered:**
- *One YAML for everything*: mixes the operator's settings with the tenant's resources
  and cannot be strict about one and compatible for the other.
- *A Kubernetes-style CRD format*: not what Artifactory users have, and nothing consumes it.

## Constraints

- An empty descriptor is destructive in Artifactory (it deletes every repository);
  artifactweave refuses an empty document and requires `null` per element.
- `config apply` is idempotent and reports a diff; it is safe in a CI job.
- Secrets in descriptors are sealed on write ([0017](0017-sealed-columns-and-master-key.md)).

## Verification

- Descriptor round-trip tests: apply, read back as YAML and XML, compare.
- `artifactory_backup`, `artifactory_proxy`, `artifactory_property_set`,
  `artifactory_repository_layout` and `artifactory_general_security` Terraform acceptance
  through the PATCH endpoint.
- Strict-YAML tests for the application configuration and precedence tests for flag,
  environment and file.

## References

- [06-system-configuration-and-descriptor](../06-system-configuration-and-descriptor.md)
- <https://docs.jfrog.com/installation/docs/artifactory-yaml-configuration>
- <https://docs.jfrog.com/installation/docs/artifactory-bootstrap-yaml-file>
