// Command configurationproof exposes the canonical fixture secret proof checker.
//
// # Role
//
// Thin developer command over internal/devtools/configurationproof, invoked by
// test/terraform/run.sh; not part of the public server or release CLI.
//
// # Inputs
//
// Database path, target (proxy/<key>, mail_server/mailserver or ldap/<key>),
// lifecycle phase and evidence path as arguments;
// CONFIGURATION_PROOF_MASTER_KEY and CONFIGURATION_PROOF_PASSWORD from the
// owning driver's private environment.
//
// # Outputs
//
// Exit status 2 for usage, 1 for a failed proof and 0 for success. The engine
// owns read-only database inspection and credential-free JSON evidence; this
// wrapper prints only its redacted error or the completed phase.
//
// # References
//
// ADR 0017; ADR 0030; issues 1839, 1831 and 1837;
// internal/devtools/configurationproof.
//
// # Status
//
// Implemented for the P12 configuration and ldap-groups references: proxy
// and mail server passwords (issue 1831 added the mail target) and the LDAP
// connection's manager password (issue 1837).
package main
