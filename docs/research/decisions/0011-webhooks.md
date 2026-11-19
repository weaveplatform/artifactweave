# 0011: Webhooks

Status: Proposed

Checked against: Artifactory 7.161.32

## Context

Artifactory's Event API exposes subscriptions over domains (artifact, artifact property,
docker, build, user and the lifecycle domains) with event types, criteria, and two handler
types: a plain webhook with an optional signing secret and custom headers, and a custom
webhook with a templated payload and named secrets. The `jfrog/artifactory` Terraform
provider has one resource per domain and handler type (24 in total) and validates event
type names client-side. From SaaS 7.172.1 ([release notes](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71721-saas)) the two GET subscription endpoints no longer
return a webhook's signing secret: `secret` is empty and a boolean `secret_configured` says
whether one is set. Self-managed 7.161.32 does not have this yet; artifactweave follows it
when webhooks are built, since it only removes a secret from a read.

## Decision

The planned `/event/api/v1/subscriptions` implementation uses the documented domains,
event types, criteria and both handler types from
[07-webhooks-retention-projects](../07-webhooks-retention-projects.md). Event type names
are stored and echoed verbatim. The custom handler renders its payload with Go
`text/template` and the documented functions (`{{.data.x}}`, `{{json .data}}`,
`{{.secrets.name}}`, `{{.userContext.id}}`). Secrets are sealed
([0017](0017-sealed-columns-and-master-key.md)) and redacted on read, with the documented
update semantics (omit keeps, a value rotates, an empty string wipes).

**Phase allocation (2026-11-19):** P11 owns subscription CRUD, both handler
configurations, criteria/template validation, sealed secrets and redacted reads. P19a
owns the execution contract after P19 observations: serialization, authentication/signing,
actor context, actual handler delivery, test and troubleshooting. P11 completes against
its administrative and event-foundation exits without awaiting that phase.

Delivery in P19a is asynchronous with retry and backoff; every attempt is logged so
`/event/api/v1/troubleshooting` can answer. Signing follows Artifactory: the
`X-JFrog-Event-Auth` header carries the raw secret when `use_secret_for_signing` is off
and the HMAC-SHA256 of the body when it is on; the exact format is captured from a real
delivery in P19 before it is fixed in P19a. These signing details remain **[I]**
until that observation; the implementation follows the captured contract.

Events are emitted by the item, docker, build and user services at the point of the state
change, never by HTTP handlers, so replication and future event consumers share the same
source.

## Rationale

Webhooks are the integration point CI systems depend on and the resource set the
Terraform provider is largest in; implementing the documented surface fully is cheaper
than explaining a subset. Emitting from services keeps the event stream complete whatever
the entry point.

**Alternatives considered:**
- *Workers (JFrog's serverless hooks)* in place of webhooks: Workers run user code on
  platform actions and are decided separately ([0026](0026-workers-runtime.md)); webhooks
  stay the integration point for external systems.

## Constraints

- P11 owns subscription administration and shared event foundations; P19a owns
  observed delivery behavior. Domains whose products are not yet implemented produce no events until their owning phases land: P24/P25 supply Lifecycle
  and AppTrust producers, while P15 supplies release-bundle and Distribution events
  ([0023](0023-release-bundles-and-distribution.md)). Those phases prove their committed
  producer events; P19a qualifies the delivered pre-observation producers through its transport, and later phases prove delivery
  through that existing transport; accepting a subscription alone does not complete
  an event domain ([capability roadmap](../../implementation/capability-roadmap.md)).
- Delivery order is per subscription, not global.

## Verification

- P11: subscription CRUD and event-type echo tests for every domain.
- P19a: delivery tests against a chi receiver with signing on and off, retries and the
  troubleshooting log.
- P11: all 24 `artifactory_*_webhook` and `artifactory_*_custom_webhook` resources in the
  Terraform acceptance fixture.

## References

- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), webhooks section
- <https://docs.jfrog.com/integrations/docs/webhook-event-types>
- <https://docs.jfrog.com/integrations/reference/jfrog-platform-webhooks-api/llms.txt>
