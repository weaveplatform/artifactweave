// Package logging supplies process-local level decisions for structured slog
// handlers. Configuration owns the administration policy and expiration.
//
// # Role
//
// Platform mechanism; it imports no module business policy.
//
// # Inputs
//
// The original slog sink, registered dotted component names and accepted levels.
//
// # Outputs
//
// Named loggers preserving the sink's attributes, groups and redaction. No
// timers, workers, configuration documents or credentials are retained here.
//
// # References
//
// Decision 0016: docs/research/decisions/0016-observability-and-health.md.
// Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// P12 supplies bounded runtime logger controls; P38 owns log streaming and
// support bundles, and P35 owns cross-node administration.
package logging
