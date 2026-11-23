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

P12 CFG-01 implements administrator-only `GET /artifactory/api/system/configuration`
from current state for the delivered sections listed in
[research 06](../06-system-configuration-and-descriptor.md#what-artifactweave-serves).
The export joins the owning APIs to one metadata transaction and shared authority
guard, rechecks current administrator authority there, and publishes XML only after
commit. Later descriptor sections extend this same read contract. P12 also owns
completion of the baseline YAML descriptor.
Full XML replacement by `POST`, Artifactory bootstrap-file compatibility and Access
bootstrap migration belong to P36. The separate deprecated raw security XML interface
has Security REST and Access bootstrap replacements; that does not remove general system
XML import from scope. Product-specific sections follow their feature phases in the
[capability roadmap](../../implementation/capability-roadmap.md).

PRJ-04 supplies the narrow `mailServer` dependency for quota notifications through
the same PATCH, config-apply and bootstrap path. Its documented singleton fields
merge into the existing mail configuration document; the password is separated
and sealed with the configured keyring. Settings and password are read together
under the shared guard before delivery starts. Disabled mail and string clearing
are explicit. CFG-01 includes these public mail settings in XML.

CFG-07 ([#1831](https://github.com/weaveplatform/artifactweave/issues/1831)) completes
`mailServer`, `backups` and `systemMessageConfig` on the same path, validated with the
rest of the document and written in its one transaction. `mailServer: null` deletes the
settings and their sealed password, as the pinned `artifactory_mail_server` resource
deletes them. Backup definitions are stored and exported in XSD `BackupType` order, with
schedules and repository exclusions checked against the document's final repositories.
Deleting a repository by any path drops it from every backup's exclusions in that
transaction, through the artifacts module's repository lifecycle; running backups stays
with P36. The system message is a singleton outside the XSD, so it is
not exported. `artifactweave mail test` sends Artifactory's test message from the stored
settings without holding a metadata transaction across SMTP.

P12 CFG-06 (#1830) applies `quotaConfig` through the same path. The section is
stored as one configuration document; the configuration module's quota core admits
growing writes against the measured hot blob volume inside each metadata
transaction and mails administrators through `mailServer`
([research 06](../06-system-configuration-and-descriptor.md#global-storage-quota)).

P12/#1835 gives remote-source proxy `domain` and `ntHost` runtime effects: NTLMv2
proxy-account domain and client workstation, through the existing resolver and sealed
password. Configuration storage is unchanged. The [transport contract](../../implementation/remote-http-transport.md)
records the supported exchange and explicit limits; P12/#1836 gives `redirectedToHosts`
its scoped redirect-credential effect, and P12/#1848 applies `domain`/`ntHost` to
single-replication push, where a valid `redirectedToHosts` is accepted without effect.

LDAP-01 ([#1832](https://github.com/weaveplatform/artifactweave/issues/1832)) applies
`security.ldapSettings` through PATCH, config apply and the bootstrap descriptor, writing through the access module's
LDAP service rather than its own copy of the rules, so the descriptor and
`/access/api/v1/ldap/settings` read back one connection. Its entries change field by field
and are validated with the rest of the document before any write; an entry set to null
deletes a connection; the whole-section null is refused. The XML export carries
neither the connections nor any repository entry, as 7.161.26's descriptor does not: it
stores repositories in its repository configuration store and LDAP connections in Access
([#1893](https://github.com/weaveplatform/artifactweave/issues/1893), research 06, "Keys
shared across sections").

CFG-02 ([#1826](https://github.com/weaveplatform/artifactweave/issues/1826)) adds the
descriptor's `urlBase`, through PATCH, config apply, the bootstrap descriptor and
`PUT /api/system/configuration/baseUrl`, and its single reverse proxy, through
`POST /api/system/configuration/webServer`; each REST write builds a descriptor patch
and applies it, so it is validated whole and written in one transaction. The two layers
stay apart: `base_url` in `artifactweave.yaml` keeps its flag > environment > file
precedence and is never written at run time, and a stored `urlBase` is tenant state that
takes precedence over it while set and is exported with the descriptor. Clearing
`urlBase` brings `base_url` back. The application layer also supplies the rules the
descriptor cannot see, such as the CDN hosts a `urlBase` may not sit above.

## Rationale

The Artifactory descriptor is the format Artifactory users already have in version
control and the one the `artifactory_*` configuration resources write through, so adopting
it preserves the GitOps workflow and supports migration of implemented sections. Keeping
it separate from the application configuration keeps the strict-YAML rule for operational settings while
allowing the descriptor's looser, JFrog-defined schema.

**Alternatives considered:**
- *One YAML for everything*: mixes the operator's settings with the tenant's resources
  and cannot be strict about one and compatible for the other.
- *A Kubernetes-style CRD format*: not what Artifactory users have, and nothing consumes it.

## Constraints

- An empty descriptor is destructive in Artifactory (it deletes every repository);
  artifactweave refuses an empty document and requires `null` per element.
- A section is accepted only once artifactweave applies it. Until then a descriptor that
  carries it answers 400 naming it, and nothing in the descriptor is applied, so a
  pipeline never reports success for configuration that is not in force
  ([research 06](../06-system-configuration-and-descriptor.md#what-artifactweave-serves)).
- A successful PATCH answers Artifactory's `<n> changes to config merged successfully`;
  `config apply` prints what it created, updated, deleted and left unchanged.
- `config apply` is idempotent and reports a diff; it is safe in a CI job.
- Secrets in descriptors are sealed on write ([0017](0017-sealed-columns-and-master-key.md)).
- Proxy and property-set documents are validated after merging and before any
  patch writes, including secret sealing. Proxy optional fields keep their
  documented string/boolean types; property-set visibility, property flags and
  predefined defaults are booleans. Property definitions and predefined-value
  objects accept only their supported fields, with dynamic property/value names
  kept as data. Both provider predefined-value maps and YAML lists (strings or
  `{value, defaultValue}` objects) are accepted. Existing null clearing, omitted
  fields and explicit false values retain their behavior. An invalid field
  refuses the whole descriptor, including a dry run ([#1839](https://github.com/weaveplatform/artifactweave/issues/1839)).

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

P12 temporary logger controls ([#1827](https://github.com/weaveplatform/artifactweave/issues/1827))
are deliberately outside descriptor persistence. Configuration core validates all
1–100 classes and the positive representable timer before publishing one bounded
session to the platform logging mechanism. Current credential and administrator
authority are rechecked under the access administration guard; failed authority
transactions publish no level change. DELETE, timer expiry and runtime shutdown
restore the original handler's current baseline. See [decision 0016](0016-observability-and-health.md)
for lifecycle ownership and [research 06](../06-system-configuration-and-descriptor.md)
for the primary contract and explicit local choices.

Every logger API operation rejects a context already inside this metadata
store's transaction before entering the logger lock or changing decisions/timers.
This prevents a transaction-scoped read waiting on a mutation that needs the
same store guard. Volatile changes do not join descriptor transactions or defer
an acknowledged success until an outer commit; only their own completed authority
transaction can permit publication.
