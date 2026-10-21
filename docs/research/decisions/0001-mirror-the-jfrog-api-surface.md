# 0001: Mirror the JFrog API surface

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

artifactweave replaces JFrog Artifactory for teams that want one self-hosted place to
store, proxy and distribute packages and container images. Those teams already have
clients, CI pipelines, the JFrog CLI and Terraform code written against Artifactory's
REST, Access and Event APIs and package protocols. P6–P8 initially implement eight
ecosystems; P29–P33 extend native format coverage.
The cheapest migration is one where none of that changes.

JFrog embeds OpenAPI 3.1 definitions in many endpoint documentation pages and marks
them proprietary; some contracts still require client-source or live-capture research.
The initial research counted roughly 300 endpoints across six products. Later inventories
expanded that snapshot; vendor add-ons and hosted services now require explicit
implementation or integration contracts under [0042](0042-capability-scope-and-supersession.md).

## Decision

Wire compatibility with Artifactory 7.x is the target product contract. The bullets
below describe that goal; current delivery is recorded in research 10 and the plan:

- The Artifactory REST API under `/artifactory/api`, the Access API under `/access/api`,
  the Event API under `/event/api`, the router health endpoint and the Terraform discovery
  document at the host root are served with JFrog's paths, methods, request and response
  shapes, status codes and error envelopes.
- The eight package protocols (Docker/OCI, npm, Go modules, Maven, PyPI, NuGet, Helm and
  Terraform providers and modules) are served under Artifactory's URL layouts.
- Every capability has an owning phase in the
  [capability roadmap](../../implementation/capability-roadmap.md). Current availability
  is recorded separately in [research 10](../10-scope-and-tiers.md): implemented,
  partial, generated 501 stub, prefix 501 stub, or missing route (normally 404).
  A shim remains a compatible projection over the same internal model. A stub or
  missing route is temporary implementation state, not a permanent product tier.
- Only a named, evidenced successor with a migration/compatibility path permits
  exclusion of an older interface. Hosted accounts, licences, proprietary transports,
  missing UI and absent client use are dependencies or research tasks, not exclusions.
- The existing `POST /jfconnect/api/v1/backoffice/metrics/log` compatibility sink
  returns 200 and discards telemetry. It does not implement P40's JFConnect entitlement,
  consumption or external account contracts.
- For every stubbed product the research records the observed client reaction (the JFrog
  CLI and the Terraform providers), cited from client source, so a stub is never assumed
  to be harmless.
- JFrog's OpenAPI definitions are cited by URL and never vendored. artifactweave's own
  `api/*.openapi.yaml` files are authored from the documented behaviour; paths, parameter
  and field names are facts about the wire protocol.
- Native artifactweave features are not gated by JFrog licence tiers. P40's genuine
  vendor-account or licence operations still require authorized vendor entitlements;
  an artifactweave analogue must identify its own issuer and observable effect.

## Rationale

Mirroring the surface rather than designing a cleaner API is what makes the replacement
drop-in: the JFrog CLI, the `jfrog/artifactory`, `jfrog/platform` and `jfrog/project`
Terraform providers and every native package client keep working, so there is no client
migration and no need to ship a CLI or provider of our own.

Explicit availability and phase ownership make incomplete capabilities diagnosable.
A temporary 501 or 404 stays visible until its owning phase supplies the promised
behavior; neither response closes the compatibility obligation.

**Alternatives considered:**
- *A native API with a compatibility layer on top*: two surfaces to document and test, and
  the compatibility layer would still have to be complete.
- *Vendoring JFrog's OpenAPI files*: faster to start, but the files are proprietary and
  the embedded definitions are thin and inconsistent across families.

## Constraints

- Compatibility is with the documented behaviour at the research baseline (2026-10-12).
  Behaviour that is undocumented and only observable on a live Artifactory is listed in
  [12-open-questions](../12-open-questions.md) and must be captured before it is fixed in
  code.
- JFrog's wire formats mix camelCase, snake_case and kebab-case field names and
  per-service error envelopes. These are reproduced, not normalised. Booleans follow what
  Artifactory sends rather than JFrog's docs: the repository flags documented as strings
  are written as booleans and accepted in either spelling, because the Terraform provider
  decodes them as booleans (#109,
  [03](../03-repository-configuration-model.md#typing-traps)).
- The first P13–P17 peer implementation uses artifactweave's own transport
  ([0021](0021-peer-trust-and-sync-protocol.md)). P37 owns cross-vendor synchronization
  and P40 external estate/bridge integration. Management parity alone cannot prove
  vendor interoperability.
- [0042](0042-capability-scope-and-supersession.md) supersedes the historical v1 cuts.
  Every remaining gap has a phase and a visible current response; unavailable vendor
  accounts or captures block the relevant qualification, not scope ownership.

## Verification

- The acceptance suite drives the real JFrog CLI and the real Terraform providers against
  the binary ([0015](0015-testing-and-acceptance.md)).
- A table-driven test asserts the status code and envelope of every endpoint in the
  inventory, with the research tables as the oracle.
- Current stubs have tests for envelope/status; their owning phases must replace them
  with real workflow proof. P20 adds an operation-level unmapped-gap guard.
- `TestSpecParity` compares `api/*.openapi.yaml` with a gitignored cache of JFrog's
  published definitions and fails on any gap not listed in `api/parity-baseline.json`;
  the baseline shrinks as endpoints land
  ([research 01, parity oracle](../01-jfrog-api-landscape.md#parity-oracle)).

## References

- [01-jfrog-api-landscape](../01-jfrog-api-landscape.md)
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [10-scope-and-tiers](../10-scope-and-tiers.md)
- [16-enterprise-products](../16-enterprise-products.md)
- <https://docs.jfrog.com/integrations/docs/openapi-specifications>
