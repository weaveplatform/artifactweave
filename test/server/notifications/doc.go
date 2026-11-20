// Package notifications qualifies webhook subscription administration
// through the assembled application router and current access policy.
//
// # Role
//
// Server-assembly tests owned by shared CI in test_ownership.json.
//
// # Inputs
//
// Real user and token credentials, project memberships, both handler inputs and
// isolated in-memory, SQLite memory/file and PostgreSQL metadata stores.
//
// # Outputs
//
// Assertions for CRUD status/envelopes, safe projections, immutable ownership,
// secret retention and rollback, current roles and project/platform/action
// token boundaries. A local receiver counts any unexpected delivery request.
//
// # References
//
//   - api/src/event/paths-webhooks.yaml.
//   - docs/research/decisions/0011-webhooks.md.
//   - docs/research/decisions/0013-projects-and-environments.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1753.
//
// # Status
//
// P11 WEB-02 subscription administration. Delivery, test execution and
// troubleshooting retain their P19a owners; CRUD performs no receiver requests.
//
// # Testing
//
// TestAccWebhookSubscriptionSQLApplicationRoutes uses sqltest.RunModule with
// owned scratch schemas for PostgreSQL and a sealed keyring on every engine.
package notifications
