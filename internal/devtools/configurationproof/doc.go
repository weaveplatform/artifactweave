// Package configurationproof checks actual proxy, mail server and LDAP
// connection secret storage in an owned Terraform fixture through the
// server's canonical configuration store/keyring.
//
// # Role
//
// devtools layer: the covered engine behind tools/configurationproof. It opens
// an existing SQLite database read-only; it never migrates or writes that store.
//
// # Inputs
//
// Database path, target (proxy/<key>, mail_server/mailserver or
// ldap/<key>), phase and evidence file; the fixture master key and
// expected password arrive only through CONFIGURATION_PROOF_MASTER_KEY and
// CONFIGURATION_PROOF_PASSWORD. The driver owns every input and database.
//
// # Outputs
//
// Run returns redacted errors and appends private JSON evidence. Evidence
// contains presence flags and ciphertext hashes, never plaintext or keys.
//
// # Invariants
//
// Create/update/refresh/retention require the expected actual server password
// and sealed storage. Clear requires absent secret rows; delete also requires
// the configuration document to be absent. No Terraform-state value stands in
// for an assertion against the application's actual store.
//
// # References
//
// ADR 0017; ADR 0030; issues 1839, 1831 and 1837; test/terraform/run.sh.
//
// # Status
//
// Implemented for the P12 configuration and ldap-groups references, which
// the Terraform provider conformance job runs against the pinned provider;
// issue 1831 added the mail server target beside the proxy, and issue 1837
// the LDAP connection's manager_password.
package configurationproof
