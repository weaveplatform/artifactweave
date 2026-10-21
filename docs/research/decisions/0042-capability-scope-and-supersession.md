# 0042: Capability scope and supersession

Status: Accepted

Owner direction: **2026-10-21**. This records the scope rule the maintainer requested;
the P20–P41 ordering is the implementation planning baseline, not evidence of delivery.

## Context

The earlier "cut from v1" list combined scheduling decisions, missing contracts,
external integrations, client limitations and retired interfaces. Several capabilities
had no implementation phase. A generated schema or a predictable 501 response did
not close that gap. SAML, SCIM, Crowd, HTTP-SSO, Lifecycle, release bundles v2,
Evidence, AppTrust and security scanning must be planned explicitly.

## Decision

Every capability identified by the research, supported clients, repository model,
API inventories or compatibility audits is in scope unless a newer capability
supersedes it. Lack of a UI, proprietary hosting, implementation cost, an old client,
a missing schema or a vendor's end-of-life notice alone is not an exclusion.

The [capability roadmap](../../implementation/capability-roadmap.md) assigns the gaps
to P9–P41. P0–P8 remain complete against their recorded exits; P18/P19 qualify the
first delivery sequence, and P41 qualifies the expanded scope. Earlier decisions'
"v1 exclusion" language is historical where this decision replaces it.

For each capability, record the current behavior separately from the intended
contract, a primary implementation phase, dependencies and executable exit criteria.
A research uncertainty stays in its owning phase and blocks that capability's closure.
Moving work requires updating both the original phase and the receiving phase; "later"
without an owner is not an acceptable disposition.

Excluding a superseded interface requires a named replacement, primary evidence,
coverage of the original use case and an explicit migration or compatibility path.
The replacement can be planned, but its absence must remain visible. Supersession
does not erase the capability. Existing, useful compatibility interfaces may remain
in scope even when the newer implementation becomes the preferred path.

Hosted account, entitlement and cross-vendor APIs require an explicit integration
contract. Do not simulate a successful operation against a vendor-owned service when
no such operation occurred. Provider prerequisites and unavailable test environments
are recorded blockers, not grounds to remove a capability from the plan.

## Verification

- P20 reconciles research omissions, accepted repository types/classes, generated
  stubs, handwritten refusals, missing routes, parity gaps and client resources into
  an operation-level inventory with phase ownership.
- P20 adds a CI check for unmapped gaps and new client/API surface. The manual
  inventory in this change is a starting point, not a claim that this check exists.
- Every later phase proves successful native-client or provider behavior, denial and
  failure paths, and updates its capability status with evidence.
- P41 permits no unowned gap, and no remaining stub or missing route for a capability
  claimed complete. Supersession exceptions carry replacement and migration evidence.

## References

- [Implementation plan](../../implementation/plan.md)
- [Capability roadmap](../../implementation/capability-roadmap.md)
- [Scope and tiers](../10-scope-and-tiers.md)
