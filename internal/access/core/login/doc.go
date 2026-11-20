// Package login owns durable login-failure transitions.
//
// # Role
//
// Access module, core layer. Failures joins the account counter/status and a typed user.locked event.
//
// # Inputs
//
// A folded user name and the configured failed-password threshold.
//
// # Outputs
//
// Updated account state and one immutable event for a new lock transition.
//
// # Invariants
//
// The user guard serializes the transition; append failure rolls it back.
// No credential material enters an event. Authentication refusals keep their
// existing best-effort behavior after the owning transaction completes.
//
// # References
//
// P11 EVT-02 and decisions 0011 and 0044.
//
// # Status
//
// Internal service producer; no vendor webhook serialization.
//
// # Testing
//
// Shared store tests prove concurrent thresholds and append/mutation rollback.
package login
