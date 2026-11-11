// Package configuration runs the configuration module's rules on the
// assembled server: the base URL that every absolute URL in a response is
// built from, behind a chain of proxies and when the operator configures it;
// the health probes; the service id; and the YAML descriptor PATCH.
//
// # Role
//
// Test support: the server-assembly suites of the configuration module,
// which drive the production router through HTTP requests. The package's
// owner in tools/citestselection/test_ownership.json is shared, so a change
// to it selects full CI.
//
// # Inputs
//
//   - The production router from test/server/support/harness, over an
//     in-memory store seeded by test/server/support: the admin user, whose
//     password is "pw", and the local repository libs-local (fixture_test.go,
//     statusFixture, which keeps the name of the router guards' fixture in
//     internal/app).
//   - The X-Forwarded-Host, X-Forwarded-Proto and X-Forwarded-Port headers a
//     case sends, and the configured base URL a case sets.
//   - For the probes, a store and a binary store that fail on demand
//     (healthStore, healthBlobs); for the service id, a token service and
//     an instance id of their own; for the descriptor PATCH, a seeded world
//     whose repository listing fails on demand (repoWorld, which keeps the
//     root world's name).
//
// # Outputs
//
//   - Assertions only. TestAccURLsBehindAProxyChain checks that file-info
//     URIs, a Docker upload's Location on both prefixes and the registry's
//     token realm name the first entry of each forwarded header;
//     TestAccConfiguredBaseURLWinsOverForwardedHeaders checks that a
//     configured base URL comes before every forwarded header
//     (forwarded_test.go).
//   - The probes answer with the part that failed and never the store's
//     own error, and router health names the services by the last id read
//     (system_health_test.go); an administrator reads the instance id that
//     jf rt atc --grant-admin builds its scope from (service_id_test.go);
//     the descriptor PATCH applies, refuses and reports as Artifactory does
//     (descriptor_test.go).
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     ("Base URL resolver").
//   - Research 17: docs/research/17-undocumented-shapes.md, "Base URL behind
//     proxies".
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the descriptor PATCH); decision 0016:
//     docs/research/decisions/0016-observability-and-health.md (the probes).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/<module> holds each module's server-assembly suites).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/249,
//     https://github.com/weaveplatform/artifactweave/issues/313,
//     https://github.com/weaveplatform/artifactweave/issues/403,
//     https://github.com/weaveplatform/artifactweave/issues/1066 and
//     https://github.com/weaveplatform/artifactweave/issues/1081
//
// # Status
//
// Implemented P0, with the proxy-chain and configured base URL fixes of issues
// 249 and 313, and with the probes (issue 403), the service id and the
// descriptor PATCH in later phases. P9d moved the tests out of the server
// root.
package configuration
