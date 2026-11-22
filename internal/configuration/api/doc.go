// Package api is the configuration module's published surface: the
// configuration descriptor's parse and apply behind an opaque plan, its XML export, the
// system value that other modules read, the narrow mail reader, temporary logger
// administration and the configuration settings.
//
// # Role
//
// configuration module, api layer. Other modules and the composition reach
// configuration only through this package, which imports only the standard
// library (decision 0044, dependency rules 5 and 6), so it declares its own
// results and sentinels. Its contracts are today's calls into the
// configuration module: `config apply` and the bootstrap descriptor in the
// composition, the GET/PATCH /api/system/configuration handlers, and the item
// routes' minimum download redirect size, the mail delivery settings reader and
// P12 process-local logger administration.
//
// # Inputs
//
// None: the package holds interfaces, results, settings and sentinels. The
// configuration module binds Descriptor, Export, System and Mail over the descriptor
// core and conditionally binds Loggers over the system core and a logging
// control; composition owns that binding's cleanup.
//
// # Outputs
//
//   - Descriptor: Parse reads a YAML descriptor into a Plan and touches
//     nothing else; Apply checks a plan against the current state and
//     writes it in one transaction, or with dryRun only checks it.
//   - Plan: a parsed descriptor, which callers hold without looking inside.
//   - Export: current delivered descriptor sections as protected XML from one
//     guarded metadata snapshot, published after commit.
//   - Result: what an apply created, updated, deleted and left alone, with
//     Merged, the PATCH endpoint's answer, and String, the summary `config
//     apply` prints.
//   - ErrInvalid: the sentinel that every refusal of a descriptor wraps.
//   - System: the minimum download redirect size the descriptor set.
//   - Mail: the current mail-server settings, with a guarded local snapshot
//     and password excluded from JSON. Only delivery opens that secret.
//   - Loggers: a temporary process-local level session and effective-level
//     reader. ErrInvalidLoggerSettings reports refused logger requests without
//     changing the descriptor's existing ErrInvalid answer.
//   - Settings: the configuration options the module's code reads.
//   - API: one implementation of Descriptor, Export, System, Mail and, when an actual
//     runtime logging mechanism is supplied, temporary Loggers.
//
// # Invariants
//
//   - Apply writes a whole plan in one transaction and joins the one its
//     context carries, so a plan with one bad entry, or one whose last
//     write fails, changes nothing (decision 0009).
//   - Parse reads no store and no key, so a caller checks a descriptor
//     before it builds a keyring or opens a store, and a descriptor error
//     is reported first.
//   - Every Loggers operation refuses a context inside its metadata store's
//     transaction before taking a logger lock. Its owning lifecycle closes the
//     session and joins timers; standalone router-owned bindings need OnClose.
//   - Texts reach clients and logs, so they stay byte-identical:
//     ErrInvalid's "descriptor: invalid configuration descriptor", and
//     Merged's "<n> changes to config merged successfully", which counts
//     the document's values, as Artifactory does.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" (configuration/api holds
//     Descriptor, System and Settings) and "Names the migration introduces".
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decision 0029:
//     docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     (the minimum redirect size).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Full package tree", lines 302 to 306 (configuration/api's config.go
//     and events.go), and lines 1111 to 1116 (descriptor apply as the one
//     sanctioned transaction across modules).
//   - Research 06: docs/research/06-system-configuration-and-descriptor.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     WEB (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1077
//
// # Status
//
// Added in P9d: Descriptor with Parse and Apply, Plan, Result, ErrInvalid, System
// with DownloadRedirectMinSize, Settings and API. P11 supplies the narrow
// Mail.Server reader used by notification delivery. P12 CFG-03 adds Loggers and
// ErrInvalidLoggerSettings. The module supplies that optional API when Logging
// is present, and the caller owns Module.Close. app's runtime owns it, while
// standalone routers require a cleanup registrar for internal bindings.
//
// P12 CFG-01 adds Export for the administrator-only XML GET; the configuration
// HTTP adapter calls it through the composition's binding. The target layout's
// System Get and Patch and events.go with ConfigurationChanged are not created.
//
// # Testing
//
// The package has no tests of its own. configuration/core/descriptor's
// tests check Result through the descriptor it describes:
// TestUnitApplyDownloadRedirectConfig its Merged answer and
// TestUnitApplyDryRun its String summary, and the PATCH suites check the
// answer the endpoint sends.
package api
