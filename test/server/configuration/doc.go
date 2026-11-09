// Package configuration runs the configuration module's rules on the
// assembled server: the base URL that every absolute URL in a response is
// built from, behind a chain of proxies and when the operator configures it.
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
//     password is "pw", and the local repository libs-local
//     (fixture_test.go, statusFixture, which keeps the name of the root
//     fixture the tests used in internal/server).
//   - The X-Forwarded-Host, X-Forwarded-Proto and X-Forwarded-Port headers a
//     case sends, and the configured base URL a case sets.
//
// # Outputs
//
//   - Assertions only. TestAccURLsBehindAProxyChain checks that file-info
//     URIs, a Docker upload's Location on both prefixes and the registry's
//     token realm name the first entry of each forwarded header;
//     TestAccConfiguredBaseURLWinsOverForwardedHeaders checks that a
//     configured base URL comes before every forwarded header
//     (forwarded_test.go).
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     ("Base URL resolver").
//   - Research 17: docs/research/17-undocumented-shapes.md, "Base URL behind
//     proxies".
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/<module> holds each module's server-assembly suites).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     TST (https://github.com/weaveplatform/artifactweave/issues/945).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/249,
//     https://github.com/weaveplatform/artifactweave/issues/313 and
//     https://github.com/weaveplatform/artifactweave/issues/1066
//
// # Status
//
// Implemented P0, with the proxy-chain and configured base URL fixes of
// issues 249 and 313. P9d moved the tests out of internal/server.
package configuration
