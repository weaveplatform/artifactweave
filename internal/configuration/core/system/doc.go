// Package system validates and reads the mail-server settings, sends the
// administrator's test mail, administers bounded temporary logger sessions,
// and validates, stores in document form and renders the web server
// settings: urlBase and the reverse proxy with its nginx snippet.
//
// # Role
//
// configuration module, core layer.
//
// # Inputs
//
// The mailServer descriptor mapping and the shared configuration store, runner
// and authority guard. Secret values live separately in ConfigDocument.Secrets.
// LoggerDeps supplies real logging decisions, guarded current administration and
// an optional timer scheduler for deterministic expiration checks. The web
// server functions take configuration/api.ReverseProxy values, stored
// documents, and the CDN hosts a urlBase may not sit above.
//
// # Outputs
//
// Validated mail settings behind configuration/api.Mail. Every read captures
// public settings and the password under one guarded metadata transaction,
// completed before SMTP starts. MailTest sends Artifactory's test
// message through a MailSender after that read, refusing to start inside a
// transaction, and answers 7.x's confirmation or refusal texts; it opens no
// connection itself.
// Loggers validates temporary level requests, applies one in-memory session,
// restores current baseline decisions on expiry/delete and joins timers on Close.
// CheckReverseProxy and CheckURLBase refuse with configuration/api.ErrInvalid;
// NginxSnippet renders only values those checks admit, so nothing stored can
// change the snippet's nginx syntax. PublicURL caches the effective base URL
// for every request, dropped after a local write and refreshed every
// PublicURLRefresh for writes from another process.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 305 and 309.
//   - Plan:
//     docs/implementation/plan.md#p12-descriptor-export-system-configuration-ldap.
//   - Context map: the configuration row, restated in the module root's
//     doc.go; https://github.com/weaveplatform/artifactweave/issues/958 holds
//     the map.
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// PRJ-04 supplies the narrow documented mailServer dependency for quota warnings.
// P12 CFG-03 supplies actual process-local logger controls. P12 CFG-02 (#1826)
// supplies the base URL and reverse proxy settings. P12 CFG-07 (issue 1831)
// adds MailTest behind `artifactweave mail test`. Log collection and
// streaming stay P38.
//
// # Testing
//
//   - webserver_test.go: every reverse proxy refusal, the stored document's
//     round trip, urlBase refusals, the snippet's shapes and the PublicURL
//     cache.
//   - mailtest_test.go: the test message's subject, body, sender fallback
//     and Message-ID, and every refusal, none of which opens SMTP.
package system
