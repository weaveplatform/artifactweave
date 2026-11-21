// Package mail submits bounded SMTP messages without owning their business
// policy, recipient authorization or retry schedule.
//
// # Role
//
// Platform transport used by the composition root and module delivery owners.
// It depends only on the standard library and performs no metadata I/O.
//
// # Inputs
//
// Sender.Send takes current Server settings, a Message with a stable identity
// and date, and an optional final authorization callback before SMTP DATA.
// Sender.Timeout bounds all network work; RootCAs optionally extends trust.
//
// # Outputs
//
// Send returns nil after the server acknowledges the message body. Errors
// distinguish invalid input, failed delivery and the callback's own refusal.
// Server responses, credentials and message contents never appear in errors.
//
// # Invariants
//
// No metadata transaction spans network I/O. Cancellation closes the socket.
// TLS verifies the server certificate; requested encryption never falls back
// to plaintext. An uncertain DATA acknowledgement can require a retry with the
// same Message-ID; SMTP cannot promise exactly-once delivery.
//
// # References
//
//   - https://github.com/weaveplatform/artifactweave/issues/1747
//   - https://docs.jfrog.com/administration/docs/mail-server-configuration
//   - RFC 5321 (SMTP), RFC 5322 (message format), RFC 2047 (header encoding).
//
// # Status
//
// Implemented P11: bounded SMTP, STARTTLS, implicit TLS and pre-DATA checks.
// Full P12 mail administration remains owned by the configuration module.
//
// # Testing
//
// sender_test.go uses a fixture-owned loopback receiver to exercise delivery,
// encryption, cancellation, refusals and acknowledgement-loss retry identity.
package mail
